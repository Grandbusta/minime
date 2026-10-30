// Package openai is the System 2 provider for OpenAI's Responses API.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"minime/internal/pricing"
	"minime/internal/providers/openaiclient"
	"minime/internal/providers/system2"
)

const (
	// defaultMaxOutputTokens is the room it has to answer, reasoning included:
	// a few times what its longest work needs (a promised plan, about 1,700).
	// Only what it writes is billed; the cap stops a runaway answer.
	defaultMaxOutputTokens = 8000
	requestTimeout         = 120 * time.Second // reasoning can take a while
)

type Client struct {
	api    openai.Client
	model  string
	price  pricing.Price // zero: cost is recorded as 0
	effort string        // reasoning effort; "" leaves the model's default
}

func New(apiKey, model, baseURL string, price pricing.Price, effort string) *Client {
	return &Client{api: openaiclient.New(apiKey, baseURL, requestTimeout), model: model, price: price, effort: effort}
}

// replyWithFacts is the Structured Outputs schema used with CollectFacts:
// strict mode guarantees the output matches it.
var replyWithFacts = map[string]any{
	"type": "object", "additionalProperties": false, "required": []string{"reply", "remember", "who_they_are", "reminders", "promises", "permissions", "corrections", "misread", "feedback", "lessons", "tastes", "goals"},
	"properties": map[string]any{
		"reply":        map[string]any{"type": "string"},
		"who_they_are": map[string]any{"type": "string"},
		"reminders": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"when", "what"},
			"properties": map[string]any{
				"when": map[string]any{"type": "string", "description": "local time, YYYY-MM-DDTHH:MM"},
				"what": map[string]any{"type": "string"},
			},
		}},
		"permissions": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"class", "verdict"},
			"properties": map[string]any{
				"class":   map[string]any{"type": "string"},
				"verdict": map[string]any{"type": "string", "enum": []string{"auto", "ask", "forbid"}},
			},
		}},
		"corrections": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"fact", "action", "text"},
			"properties": map[string]any{
				"fact":   map[string]any{"type": "string", "description": "the fact's ref from the note, e.g. f2"},
				"action": map[string]any{"type": "string", "enum": []string{"replace", "forget"}},
				"text":   map[string]any{"type": "string", "description": "the right fact, for replace; empty for forget"},
			},
		}},
		"misread": map[string]any{"type": "string"},
		"lessons": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"skill", "lesson"},
			"properties": map[string]any{
				"skill":  map[string]any{"type": "string"},
				"lesson": map[string]any{"type": "string"},
			},
		}},
		"goals": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"skill", "level", "goal", "done_when", "due", "dream_id", "new_dream"},
			"properties": map[string]any{
				"skill":     map[string]any{"type": "string"},
				"level":     map[string]any{"type": "integer", "description": "a new skill's starting level, 1 to 10; 0 for one you have"},
				"goal":      map[string]any{"type": "string"},
				"done_when": map[string]any{"type": "string"},
				"due":       map[string]any{"type": "string", "description": "YYYY-MM-DD"},
				"dream_id":  map[string]any{"type": "string", "description": "the dream it serves, or empty for a new one"},
				"new_dream": map[string]any{"type": "string", "description": "a new dream it serves, or empty"},
			},
		}},
		"tastes": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"kind", "likes", "taste"},
			"properties": map[string]any{
				"kind":  map[string]any{"type": "string"},
				"likes": map[string]any{"type": "boolean"},
				"taste": map[string]any{"type": "string"},
			},
		}},
		"feedback": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"skill", "level", "about"},
			"properties": map[string]any{
				"skill": map[string]any{"type": "string"},
				"level": map[string]any{"type": "integer"},
				"about": map[string]any{"type": "string"},
			},
		}},
		"promises": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"by", "what", "ability", "query"},
			"properties": map[string]any{
				"by":      map[string]any{"type": "string", "description": "local time, YYYY-MM-DDTHH:MM"},
				"what":    map[string]any{"type": "string"},
				"ability": map[string]any{"type": "string", "description": "an ability it needs, or empty"},
				"query":   map[string]any{"type": "string", "description": "what the ability is to look for, or empty"},
			},
		}},
		"remember": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"about", "fact"},
			"properties": map[string]any{
				"about": map[string]any{"type": "string", "enum": []string{string(system2.AboutSelf), string(system2.AboutSpeaker)}},
				"fact":  map[string]any{"type": "string"},
			},
		}},
	},
}

// input lays the conversation out as chat turns, then the note (as
// developer context), then the message to answer, if any.
func input(req system2.Request) responses.ResponseInputParam {
	var in responses.ResponseInputParam
	for _, t := range req.History {
		role := responses.EasyInputMessageRoleUser
		if t.Role == system2.RoleAgent {
			role = responses.EasyInputMessageRoleAssistant
		}
		in = append(in, responses.ResponseInputItemParamOfMessage(t.Text, role))
	}
	if req.Note != "" {
		in = append(in, responses.ResponseInputItemParamOfMessage(req.Note, responses.EasyInputMessageRoleDeveloper))
	}
	if req.Message == "" { // the agent speaks first: the note says why
		return in
	}
	return append(in, responses.ResponseInputItemParamOfMessage(req.Message, responses.EasyInputMessageRoleUser))
}

// jsonFormat asks for output matching schema, strictly.
func jsonFormat(name string, schema map[string]any) responses.ResponseTextConfigParam {
	return responses.ResponseTextConfigParam{Format: responses.ResponseFormatTextConfigUnionParam{
		OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{Name: name, Schema: schema, Strict: param.NewOpt(true)}}}
}

func (c *Client) Reason(ctx context.Context, req system2.Request) (system2.Response, error) {
	max := req.MaxOutputTokens
	if max == 0 {
		max = defaultMaxOutputTokens
	}
	params := responses.ResponseNewParams{Model: c.model, Input: responses.ResponseNewParamsInputUnion{OfInputItemList: input(req)},
		MaxOutputTokens: param.NewOpt(int64(max)), Store: param.NewOpt(false)} // false: OpenAI keeps no copy for later retrieval
	if req.Instructions != "" {
		params.Instructions = param.NewOpt(req.Instructions)
	}
	switch {
	case req.CollectFacts:
		params.Text = jsonFormat("reply_with_facts", replyWithFacts)
	case req.Format != nil:
		params.Text = jsonFormat(req.Format.Name, req.Format.Schema)
	}
	if c.effort != "" {
		params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(c.effort)}
	}
	// Cut off (it ran out of room): the text is unfinished, and structured
	// output is broken JSON, so it's no answer. Out of room, it tries once
	// more with twice as much.
	structured := req.CollectFacts || req.Format != nil
	var w *responses.Response
	for attempt := 1; ; attempt++ {
		var err error
		if w, err = c.api.Responses.New(ctx, params); err != nil {
			return system2.Response{}, fmt.Errorf("openai responses: %w", err)
		}
		if w.Error.Code != "" {
			return system2.Response{}, fmt.Errorf("openai responses: %s: %s", w.Error.Code, w.Error.Message)
		}
		if w.Status != responses.ResponseStatusIncomplete || !structured {
			break
		}
		if attempt == 1 && w.IncompleteDetails.Reason == "max_output_tokens" {
			params.MaxOutputTokens = param.NewOpt(params.MaxOutputTokens.Value * 2)
			continue
		}
		return system2.Response{}, fmt.Errorf("openai responses: %w (%s)", system2.ErrCutOff, w.IncompleteDetails.Reason)
	}

	var text, refusal strings.Builder
	for _, o := range w.Output {
		if o.Type != "message" {
			continue // reasoning items and the like carry no reply text
		}
		for _, part := range o.Content {
			switch part.Type {
			case "output_text":
				text.WriteString(part.Text)
			case "refusal":
				refusal.WriteString(part.Refusal)
			}
		}
	}
	reply := strings.TrimSpace(text.String())
	var (
		remember   []system2.Memory
		whoTheyAre string
		reminders  []system2.Reminder
		promises   []system2.Promise
		perms      []system2.Permission
		fixes      []system2.Correction
		misread    string
		feedback   []system2.Feedback
		lessons    []system2.LessonLearned
		tastes     []system2.Taste
		goals      []system2.GoalTaken
	)
	switch {
	case refusal.Len() > 0:
		reply = strings.TrimSpace(refusal.String()) // a refusal is plain text, never the JSON format
	case req.CollectFacts && reply != "":
		var out struct {
			Reply      string `json:"reply"`
			WhoTheyAre string `json:"who_they_are"`
			Remember   []struct {
				About string `json:"about"`
				Fact  string `json:"fact"`
			} `json:"remember"`
			Reminders []struct {
				When string `json:"when"`
				What string `json:"what"`
			} `json:"reminders"`
			Promises []struct {
				By      string `json:"by"`
				What    string `json:"what"`
				Ability string `json:"ability"`
				Query   string `json:"query"`
			} `json:"promises"`
			Permissions []struct {
				Class   string `json:"class"`
				Verdict string `json:"verdict"`
			} `json:"permissions"`
			Corrections []struct {
				Fact   string `json:"fact"`
				Action string `json:"action"`
				Text   string `json:"text"`
			} `json:"corrections"`
			Misread  string `json:"misread"`
			Feedback []struct {
				Skill string `json:"skill"`
				Level int    `json:"level"`
				About string `json:"about"`
			} `json:"feedback"`
			Lessons []struct {
				Skill  string `json:"skill"`
				Lesson string `json:"lesson"`
			} `json:"lessons"`
			Tastes []struct {
				Kind  string `json:"kind"`
				Likes bool   `json:"likes"`
				Taste string `json:"taste"`
			} `json:"tastes"`
			Goals []struct {
				Skill    string `json:"skill"`
				Level    int    `json:"level"`
				Goal     string `json:"goal"`
				DoneWhen string `json:"done_when"`
				Due      string `json:"due"`
				DreamID  string `json:"dream_id"`
				NewDream string `json:"new_dream"`
			} `json:"goals"`
		}
		if err := json.Unmarshal([]byte(reply), &out); err != nil {
			return system2.Response{}, fmt.Errorf("openai responses: reply is not the requested JSON: %w", err)
		}
		reply = strings.TrimSpace(out.Reply)
		whoTheyAre = strings.TrimSpace(out.WhoTheyAre)
		for _, f := range out.Remember {
			about := system2.About(f.About)
			if text := strings.TrimSpace(f.Fact); text != "" && (about == system2.AboutSelf || about == system2.AboutSpeaker) {
				remember = append(remember, system2.Memory{About: about, Text: text})
			}
		}
		for _, r := range out.Reminders {
			if when, what := strings.TrimSpace(r.When), strings.TrimSpace(r.What); when != "" && what != "" {
				reminders = append(reminders, system2.Reminder{When: when, What: what})
			}
		}
		for _, p := range out.Permissions {
			if class := strings.TrimSpace(p.Class); class != "" {
				perms = append(perms, system2.Permission{Class: class, Verdict: strings.TrimSpace(p.Verdict)})
			}
		}
		for _, c := range out.Corrections {
			fact, text := strings.TrimSpace(c.Fact), strings.TrimSpace(c.Text)
			if fact != "" && (c.Action == "forget" || text != "") {
				fixes = append(fixes, system2.Correction{Fact: fact, Forget: c.Action == "forget", Text: text})
			}
		}
		misread = strings.TrimSpace(out.Misread)
		for _, l := range out.Lessons {
			if skill, text := strings.TrimSpace(l.Skill), strings.TrimSpace(l.Lesson); skill != "" && text != "" {
				lessons = append(lessons, system2.LessonLearned{Skill: skill, Text: text})
			}
		}
		for _, g := range out.Goals {
			if skill, text := strings.TrimSpace(g.Skill), strings.TrimSpace(g.Goal); skill != "" && text != "" {
				goals = append(goals, system2.GoalTaken{Skill: skill, Level: g.Level, Text: text, DoneWhen: strings.TrimSpace(g.DoneWhen),
					Due: strings.TrimSpace(g.Due), DreamID: strings.TrimSpace(g.DreamID), NewDream: strings.TrimSpace(g.NewDream)})
			}
		}
		for _, t := range out.Tastes {
			if text := strings.TrimSpace(t.Taste); text != "" {
				tastes = append(tastes, system2.Taste{Kind: strings.TrimSpace(t.Kind), Likes: t.Likes, Text: text})
			}
		}
		for _, f := range out.Feedback {
			if skill := strings.TrimSpace(f.Skill); skill != "" && f.Level >= 1 && f.Level <= 10 {
				feedback = append(feedback, system2.Feedback{Skill: skill, Level: f.Level, About: strings.TrimSpace(f.About)})
			}
		}
		for _, p := range out.Promises {
			if by, what := strings.TrimSpace(p.By), strings.TrimSpace(p.What); by != "" && what != "" {
				promises = append(promises, system2.Promise{By: by, What: what, Ability: strings.TrimSpace(p.Ability),
					Query: strings.TrimSpace(p.Query)})
			}
		}
	}
	if reply == "" {
		reason := string(w.Status)
		if w.IncompleteDetails.Reason != "" {
			reason += " (" + string(w.IncompleteDetails.Reason) + ")"
		}
		return system2.Response{}, errors.New("openai responses: no reply text, status " + reason)
	}

	u := w.Usage
	return system2.Response{
		Provider:    "openai/" + w.Model,
		Text:        reply,
		Remember:    remember,
		WhoTheyAre:  whoTheyAre,
		Reminders:   reminders,
		Promises:    promises,
		Permissions: perms,
		Corrections: fixes,
		Misread:     misread,
		Feedback:    feedback,
		Lessons:     lessons,
		Tastes:      tastes,
		Goals:       goals,
		Usage: system2.Usage{InputTokens: int(u.InputTokens), OutputTokens: int(u.OutputTokens),
			ReasoningTokens: int(u.OutputTokensDetails.ReasoningTokens)},
		CostUSD: c.price.Cost(pricing.Usage{
			InputTokens: int(u.InputTokens), CachedTokens: int(u.InputTokensDetails.CachedTokens),
			CacheWriteTokens: int(u.InputTokensDetails.CacheWriteTokens), OutputTokens: int(u.OutputTokens),
		}),
	}, nil
}
