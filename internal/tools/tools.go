// Package tools connects to MCP servers and offers the tools the config
// lists as abilities. Only reading tools exist yet: what a tool returns is
// fenced as material, never instructions, and every call is logged and
// counted against a daily cap. A local server is given only PATH, HOME and
// the variables the config names, never the rest of the environment.
package tools

import (
	"context"
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
	"github.com/Grandbusta/minime/internal/config"
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

// Connected is a server and the tools it offers, as abilities; Err says why
// it has none (it couldn't be reached, or a tool isn't there).
type Connected struct {
	Server *Server
	Tools  []Tool
	Err    error
}

// Connect reaches every server, keeping those it can: a server it can't
// reach is reported in its Connected's Err, not a reason to stop. limit is
// the calls a day across all of them.
func Connect(ctx context.Context, cfg config.Tools, limit int, st Store, log *slog.Logger) []Connected {
	var out []Connected
	for _, sc := range cfg.Servers {
		s := &Server{Name: sc.Name, transport: transport(sc)}
		c := Connected{Server: s}
		c.Tools, c.Err = offered(ctx, s, sc, limit, st)
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
func offered(ctx context.Context, s *Server, sc config.ToolServer, limit int, st Store) ([]Tool, error) {
	cs, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	have := map[string]*mcp.Tool{}
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list %s's tools: %w", s.Name, err)
		}
		have[t.Name] = t
	}
	var out []Tool
	for _, name := range slices.Sorted(maps.Keys(sc.Tools)) {
		t, ok := have[name]
		if !ok {
			return nil, fmt.Errorf("%s has no tool %q (it has %s)", s.Name, name, strings.Join(slices.Sorted(maps.Keys(have)), ", "))
		}
		if a := t.Annotations; a != nil && a.DestructiveHint != nil && *a.DestructiveHint && !a.ReadOnlyHint {
			return nil, fmt.Errorf("%s says %s can change things, so it can't be a read tool", s.Name, name)
		}
		tool, err := newTool(s, t, sc.About, limit, st)
		if err != nil {
			return nil, err
		}
		out = append(out, tool)
	}
	all := make([]ability.Ability, len(out)) // shared: each tool can be followed by any of them
	for i := range out {
		out[i].siblings = &all
	}
	for i := range out {
		all[i] = out[i]
	}
	return out, nil
}

// Tool is one of a server's tools, offered as an ability. Its query is the
// tool's arguments as a JSON object, checked against the tool's own schema.
type Tool struct {
	server   *Server
	about    string
	siblings *[]ability.Ability // the server's tools, this one too
	tool     *mcp.Tool
	schema   *jsonschema.Schema
	resolved *jsonschema.Resolved
	limit    int
	store    Store
}

func newTool(s *Server, t *mcp.Tool, about string, limit int, st Store) (Tool, error) {
	raw, err := json.Marshal(t.InputSchema)
	if err != nil {
		return Tool{}, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return Tool{}, fmt.Errorf("%s's schema: %w", t.Name, err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return Tool{}, fmt.Errorf("%s's schema: %w", t.Name, err)
	}
	return Tool{server: s, about: about, tool: t, schema: &schema, resolved: resolved, limit: limit, store: st}, nil
}

func (t Tool) Name() string { return t.server.Name + "." + t.tool.Name }

func (t Tool) Risk() ability.Risk { return ability.UsesTools }

func (t Tool) Next() []ability.Ability {
	if t.siblings == nil {
		return nil
	}
	return *t.siblings
}

// Describe is the tool's own description and what to give it.
func (t Tool) Describe() string {
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
	query := "query: {} (nothing to give)"
	if len(params) > 0 {
		query = "query: a JSON object with " + strings.Join(params, "; ")
	}
	from := t.server.Name
	if t.about != "" {
		from += ": " + t.about
	}
	return fmt.Sprintf("%s (a tool from %s); %s", cut(strings.TrimSpace(t.tool.Description), 300), from, query)
}

func (t Tool) Prepare(ctx context.Context, a ability.Act) (ability.Material, error) {
	args := map[string]any{}
	if q := strings.TrimSpace(a.Query()); q != "" {
		if err := json.Unmarshal([]byte(q), &args); err != nil {
			return ability.Material{}, fmt.Errorf("%s: its query isn't a JSON object: %w", t.Name(), err)
		}
	}
	if err := t.resolved.Validate(args); err != nil {
		return ability.Material{}, fmt.Errorf("%s: %w", t.Name(), err)
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
