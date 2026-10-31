// Package tools connects to MCP servers and offers the tools the config
// lists as abilities. Only reading tools exist yet: what a tool returns is
// fenced as material, never instructions, and every call is logged and
// counted against a daily cap. A local server is given only PATH, HOME and
// the variables the config names, never the rest of the environment.
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Grandbusta/minime/internal/ability"
	"github.com/Grandbusta/minime/internal/action"
	"github.com/Grandbusta/minime/internal/config"
	"github.com/Grandbusta/minime/internal/memory"
)

const (
	callTimeout    = 30 * time.Second
	connectTimeout = 60 * time.Second // a first npx run downloads its server
	maxResult      = 12000            // runes of a result it reads
	logKind        = "tool"
)

// Store is where calls are logged and counted (the web log).
type Store interface {
	LogWeb(ctx context.Context, kind, what string, costUSD float64, at time.Time) error
	WebSince(ctx context.Context, kind string, since time.Time) (int, error)
}

var ErrOverCap = errors.New("over today's limit of tool calls")

// Server is one MCP server's connection: made again once if it drops.
type Server struct {
	Name      string
	transport func() mcp.Transport
	mu        sync.Mutex
	session   *mcp.ClientSession
}

var impl = &mcp.Implementation{Name: "minime", Version: "1"}

func (s *Server) connect(ctx context.Context) (*mcp.ClientSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		return s.session, nil
	}
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	cs, err := mcp.NewClient(impl, nil).Connect(ctx, s.transport(), nil)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", s.Name, err)
	}
	s.session = cs
	return cs, nil
}

func (s *Server) drop(cs *mcp.ClientSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == cs {
		cs.Close()
		s.session = nil
	}
}

func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		s.session.Close()
		s.session = nil
	}
}

// call calls tool, connecting again once if the connection broke. A result
// the tool marks as an error is an error.
func (s *Server) call(ctx context.Context, tool string, args map[string]any) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var res *mcp.CallToolResult
	for try := 0; try < 2; try++ {
		cs, err := s.connect(ctx)
		if err != nil {
			return "", err
		}
		res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if err == nil {
			break
		}
		s.drop(cs)
		if try == 1 || ctx.Err() != nil {
			return "", err
		}
	}
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(t.Text)
		}
	}
	text := b.String()
	if res.IsError {
		return "", fmt.Errorf("%s says: %s", tool, cut(text, 300))
	}
	return cut(text, maxResult), nil
}

func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// Connected is a server, its reading tools (abilities) and its tools that
// change things (actions); Err says why it has none (it couldn't be reached,
// or a tool isn't there).
type Connected struct {
	Server   *Server
	Tools    []Tool
	Actions  []Action
	Watchers []Watcher
	Err      error
}

// Connect reaches every server, keeping those it can: a server it can't
// reach is reported in its Connected's Err, not a reason to stop. limit is
// the reading calls a day across all of them.
func Connect(ctx context.Context, cfg config.Tools, limit int, st Store, log *slog.Logger) []Connected {
	var out []Connected
	for _, sc := range cfg.Servers {
		s := &Server{Name: sc.Name, transport: transport(sc)}
		c := Connected{Server: s}
		c.Tools, c.Actions, c.Err = offered(ctx, s, sc, limit, st)
		if c.Err == nil {
			c.Watchers, c.Err = watchers(sc, c.Tools)
		}
		if c.Err != nil {
			log.Warn("tool server not used", "server", sc.Name, "err", c.Err)
		}
		out = append(out, c)
	}
	return out
}

func transport(sc config.ToolServer) func() mcp.Transport {
	if sc.URL != "" {
		return func() mcp.Transport {
			t := &mcp.StreamableClientTransport{Endpoint: sc.URL}
			if sc.AuthEnv != "" {
				t.HTTPClient = &http.Client{Transport: bearer{token: os.Getenv(sc.AuthEnv)}}
			}
			return t
		}
	}
	return func() mcp.Transport {
		cmd := exec.Command(sc.Command[0], sc.Command[1:]...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
		for _, name := range sc.Env {
			cmd.Env = append(cmd.Env, name+"="+os.Getenv(name))
		}
		return &mcp.CommandTransport{Command: cmd}
	}
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// offered lists the server's tools and keeps those the config names, each
// checked against what the server says it does: a tool it marks as
// destructive is never a reading tool, whatever the config says.
func offered(ctx context.Context, s *Server, sc config.ToolServer, limit int, st Store) ([]Tool, []Action, error) {
	cs, err := s.connect(ctx)
	if err != nil {
		return nil, nil, err
	}
	have := map[string]*mcp.Tool{}
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, nil, fmt.Errorf("list %s's tools: %w", s.Name, err)
		}
		have[t.Name] = t
	}
	var (
		reads []Tool
		acts  []Action
	)
	for _, name := range slices.Sorted(maps.Keys(sc.Tools)) {
		t, ok := have[name]
		if !ok {
			return nil, nil, fmt.Errorf("%s has no tool %q (it has %s)", s.Name, name, strings.Join(slices.Sorted(maps.Keys(have)), ", "))
		}
		sp, err := newSpec(s, t, sc.About, st)
		if err != nil {
			return nil, nil, err
		}
		kind := sc.Tools[name]
		if kind != config.ToolRead {
			acts = append(acts, Action{spec: sp, class: action.Class(kind)})
			continue
		}
		if a := t.Annotations; a != nil && a.DestructiveHint != nil && *a.DestructiveHint && !a.ReadOnlyHint {
			return nil, nil, fmt.Errorf("%s says %s can change things, so it can't be a read tool", s.Name, name)
		}
		reads = append(reads, Tool{spec: sp, limit: limit})
	}
	all := make([]ability.Ability, len(reads)) // shared: each reading tool can be followed by any of them
	for i := range reads {
		reads[i].siblings = &all
	}
	for i := range reads {
		all[i] = reads[i]
	}
	return reads, acts, nil
}

// spec is what every tool has: its server, its definition and its schema.
type spec struct {
	server   *Server
	about    string
	tool     *mcp.Tool
	schema   *jsonschema.Schema
	resolved *jsonschema.Resolved
	store    Store
}

func newSpec(s *Server, t *mcp.Tool, about string, st Store) (spec, error) {
	raw, err := json.Marshal(t.InputSchema)
	if err != nil {
		return spec{}, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return spec{}, fmt.Errorf("%s's schema: %w", t.Name, err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return spec{}, fmt.Errorf("%s's schema: %w", t.Name, err)
	}
	return spec{server: s, about: about, tool: t, schema: &schema, resolved: resolved, store: st}, nil
}

func (t spec) Name() string { return t.server.Name + "." + t.tool.Name }

// describe is the tool's own description and what to give it, as given.
func (t spec) describe(given string) string {
	var params []string
	for _, name := range slices.Sorted(maps.Keys(t.schema.Properties)) {
		p := t.schema.Properties[name]
		param := name + " (" + strings.Join(append([]string{p.Type}, p.Types...), "|")
		if slices.Contains(t.schema.Required, name) {
			param += ", required"
		}
		param += ")"
		if d := strings.TrimSpace(p.Description); d != "" {
			param += ": " + cut(d, 120)
		}
		params = append(params, param)
	}
	query := "{} (nothing to give)"
	if len(params) > 0 {
		query = "a JSON object with " + strings.Join(params, "; ")
	}
	from := t.server.Name
	if t.about != "" {
		from += ": " + t.about
	}
	return fmt.Sprintf("%s (a tool from %s); %s: %s", cut(strings.TrimSpace(t.tool.Description), 300), from, given, query)
}

// args reads raw (a JSON object, or "" for none) and checks it against the
// tool's schema.
func (t spec) args(raw string) (map[string]any, error) {
	args := map[string]any{}
	if raw = strings.TrimSpace(raw); raw != "" {
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return nil, fmt.Errorf("%s: its arguments aren't a JSON object: %w", t.Name(), err)
		}
	}
	if err := t.resolved.Validate(args); err != nil {
		return nil, fmt.Errorf("%s: %w", t.Name(), err)
	}
	return args, nil
}

// Tool is a reading tool, offered as an ability: its query is its
// arguments.
type Tool struct {
	spec
	siblings *[]ability.Ability // the server's reading tools, this one too
	limit    int
}

func (t Tool) Risk() ability.Risk { return ability.UsesTools }

func (t Tool) Describe() string { return t.describe("query") }

func (t Tool) Next() []ability.Ability {
	if t.siblings == nil {
		return nil
	}
	return *t.siblings
}

func (t Tool) Prepare(ctx context.Context, a ability.Act) (ability.Material, error) {
	args, err := t.args(a.Query())
	if err != nil {
		return ability.Material{}, err
	}
	if err := t.under(ctx, a); err != nil {
		return ability.Material{}, err
	}
	text, err := t.server.call(ctx, t.tool.Name, args)
	if logErr := t.store.LogWeb(ctx, logKind, t.Name()+" "+cut(strings.TrimSpace(a.Query()), 500), 0, a.Now.UTC()); logErr != nil {
		return ability.Material{}, logErr
	}
	if err != nil {
		return ability.Material{}, err
	}
	note := fmt.Sprintf("\n\nYou used %s (%s) with %s; it returned this.%s", t.Name(), t.server.Name, orNone(a.Query()), ability.Fence(text))
	return ability.Material{Note: note, Project: a.Project}, nil
}

// Action is a tool that changes things: it is never run as it is proposed,
// only once the gate (its class) allows it.
type Action struct {
	spec
	class action.Class
}

func (a Action) Describe() string { return a.describe("its arguments") }

// Class is the autonomy class the gate checks before it runs.
func (a Action) Class() action.Class { return a.class }

// Destructive is true unless its server says it only adds: a tool that says
// nothing is taken to be destructive.
func (a Action) Destructive() bool {
	h := a.tool.Annotations
	return h == nil || !h.ReadOnlyHint && (h.DestructiveHint == nil || *h.DestructiveHint)
}

// Check reads raw as the tool's arguments, without calling it.
func (a Action) Check(raw string) error {
	_, err := a.args(raw)
	return err
}

// Do runs it with raw as its arguments, logging the call.
func (a Action) Do(ctx context.Context, raw string, at time.Time) (string, error) {
	args, err := a.args(raw)
	if err != nil {
		return "", err
	}
	text, err := a.server.call(ctx, a.tool.Name, args)
	if logErr := a.store.LogWeb(ctx, "action", a.Name()+" "+cut(strings.TrimSpace(raw), 500), 0, at.UTC()); logErr != nil {
		return "", logErr
	}
	return text, err
}

// under checks that today's calls, across every server, are under the cap.
func (t Tool) under(ctx context.Context, a ability.Act) error {
	l := a.Now.In(a.Zone)
	midnight := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, a.Zone)
	n, err := t.store.WebSince(ctx, logKind, midnight)
	if err != nil {
		return err
	}
	if n >= t.limit {
		return fmt.Errorf("%d calls today: %w", n, ErrOverCap)
	}
	return nil
}

func orNone(q string) string {
	if q = strings.TrimSpace(q); q == "" || q == "{}" {
		return "nothing"
	}
	return q
}

// Watcher looks with one of a server's reading tools on a schedule
// (config.Watch) and returns what it found, item by item.
type Watcher struct {
	tool      Tool
	args      map[string]any
	every     time.Duration
	items, id string
}

func watchers(sc config.ToolServer, reads []Tool) ([]Watcher, error) {
	var out []Watcher
	for _, w := range sc.Watch {
		i := slices.IndexFunc(reads, func(t Tool) bool { return t.tool.Name == w.Tool })
		if i < 0 {
			return nil, fmt.Errorf("%s: watch %s isn't one of its tools", sc.Name, w.Tool)
		}
		args := w.Args
		if args == nil {
			args = map[string]any{}
		}
		if err := reads[i].resolved.Validate(args); err != nil {
			return nil, fmt.Errorf("%s: watch %s: %w", sc.Name, w.Tool, err)
		}
		out = append(out, Watcher{tool: reads[i], args: args, every: w.Every, items: w.Items, id: w.ID})
	}
	return out, nil
}

func (w Watcher) Name() string         { return w.tool.Name() }
func (w Watcher) Every() time.Duration { return w.every }

// Look calls the tool and splits what it returns into items: their ID is
// their id field, or else a hash of what they say, so one seen before is
// known again. Looks are logged but not
// counted against the day's calls: they run on their own schedule.
func (w Watcher) Look(ctx context.Context, at time.Time) ([]memory.Watched, error) {
	text, err := w.tool.server.call(ctx, w.tool.tool.Name, w.args)
	if logErr := w.tool.store.LogWeb(ctx, "watch", w.Name(), 0, at.UTC()); logErr != nil {
		return nil, logErr
	}
	if err != nil {
		return nil, err
	}
	return split(text, w.items, w.id), nil
}

// split is text's items: those in the list at path in its JSON, or the whole
// text as one item if it isn't JSON with a list there.
func split(text, path, idField string) []memory.Watched {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	whole := []memory.Watched{{ID: hash(text), Text: text, Whole: true}}
	var v any
	if json.Unmarshal([]byte(text), &v) != nil {
		return whole
	}
	for _, k := range strings.Split(path, ".") {
		if k == "" {
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			return whole
		}
		v = m[k]
	}
	list, ok := v.([]any)
	if !ok {
		return whole
	}
	var out []memory.Watched
	for _, e := range list {
		raw, _ := json.MarshalIndent(e, "", "  ")
		item := memory.Watched{ID: hash(string(raw)), Text: string(raw)}
		if m, ok := e.(map[string]any); ok && idField != "" && m[idField] != nil {
			item.ID = fmt.Sprint(m[idField])
		}
		out = append(out, item)
	}
	return out
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
