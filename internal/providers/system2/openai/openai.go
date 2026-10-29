// Package openai is the System 2 provider for OpenAI's Responses API (POST /v1/responses).
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"minime/internal/pricing"
	"minime/internal/providers/openaihttp"
	"minime/internal/providers/system2"
)

const (
	defaultMaxOutputTokens = 1000
	requestTimeout         = 120 * time.Second // reasoning can take a while
)

type Client struct {
	api    *openaihttp.Client
	model  string
	price  pricing.Price // zero: cost is recorded as 0
	effort string        // reasoning effort; "" leaves the model's default
}

func New(apiKey, model, baseURL string, price pricing.Price, effort string) *Client {
	return &Client{api: openaihttp.New(apiKey, baseURL, requestTimeout), model: model, price: price, effort: effort}
}

type wireRequest struct {
	Model           string        `json:"model"`
	Instructions    string        `json:"instructions,omitempty"`
	Input           []wireMessage `json:"input"`
	MaxOutputTokens int           `json:"max_output_tokens"`
	Store           bool          `json:"store"` // false: OpenAI keeps no copy for later retrieval
	Text            *wireText     `json:"text,omitempty"`
	Reasoning       *struct {
		Effort string `json:"effort"`
	} `json:"reasoning,omitempty"`
}

type wireText struct {
	Format wireFormat `json:"format"`
}

type wireFormat struct {
	Type   string         `json:"type"` // json_schema
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

// replyWithFacts is the Structured Outputs format used with CollectFacts:
// strict mode guarantees the output matches this schema.
var replyWithFacts = &wireText{Format: wireFormat{
	Type: "json_schema", Name: "reply_with_facts", Strict: true,
	Schema: map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"reply", "remember", "who_they_are", "reminders", "promises", "permissions", "corrections", "misread", "feedback", "lessons"},
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
	},
}}

type wireMessage struct {
	Role    string `json:"role"` // user | assistant | developer
	Content string `json:"content"`
}

// wireInput lays the conversation out as chat turns, then the note (as
// developer context), then the message to answer, if any.
func wireInput(req system2.Request) []wireMessage {
	var in []wireMessage
	for _, t := range req.History {
		role := "user"
		if t.Role == system2.RoleAgent {
			role = "assistant"
		}
		in = append(in, wireMessage{Role: role, Content: t.Text})
	}
	if req.Note != "" {
		in = append(in, wireMessage{Role: "developer", Content: req.Note})
	}
	if req.Message == "" { // the agent speaks first: the note says why
		return in
	}
	return append(in, wireMessage{Role: "user", Content: req.Message})
}

type wireResponse struct {
	Model  string `json:"model"`
	Status string `json:"status"` // completed | incomplete | failed | ...
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Output []struct {
		Type    string `json:"type"` // message | reasoning | ...
		Content []struct {
			Type    string `json:"type"` // output_text | refusal
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		InputTokens        int `json:"input_tokens"`
		InputTokensDetails struct {
			CachedTokens     int `json:"cached_tokens"`
			CacheWriteTokens int `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
		OutputTokens        int `json:"output_tokens"`
		OutputTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

func (c *Client) Reason(ctx context.Context, req system2.Request) (system2.Response, error) {
	max := req.MaxOutputTokens
	if max == 0 {
		max = defaultMaxOutputTokens
	}
	body := wireRequest{Model: c.model, Instructions: req.Instructions, Input: wireInput(req), MaxOutputTokens: max}
	switch {
	case req.CollectFacts:
		body.Text = replyWithFacts
	case req.Format != nil:
		body.Text = &wireText{Format: wireFormat{Type: "json_schema", Name: req.Format.Name, Strict: true, Schema: req.Format.Schema}}
	}
	if c.effort != "" {
		body.Reasoning = &struct {
			Effort string `json:"effort"`
		}{c.effort}
	}
	var w wireResponse
	err := c.api.Post(ctx, "/responses", body, &w)
	if err != nil {
		return system2.Response{}, err
	}
	if w.Error != nil {
		return system2.Response{}, fmt.Errorf("openai responses: %s: %s", w.Error.Code, w.Error.Message)
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
		reason := w.Status
		if w.IncompleteDetails != nil {
			reason += " (" + w.IncompleteDetails.Reason + ")"
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
		Usage:       system2.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, ReasoningTokens: u.OutputTokensDetails.ReasoningTokens},
		CostUSD: c.price.Cost(pricing.Usage{
			InputTokens: u.InputTokens, CachedTokens: u.InputTokensDetails.CachedTokens,
			CacheWriteTokens: u.InputTokensDetails.CacheWriteTokens, OutputTokens: u.OutputTokens,
		}),
	}, nil
}
