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
		"type": "object", "additionalProperties": false, "required": []string{"reply", "remember", "who_they_are"},
		"properties": map[string]any{
			"reply":        map[string]any{"type": "string"},
			"who_they_are": map[string]any{"type": "string"},
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
// developer context), then the message to answer.
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
	if req.CollectFacts {
		body.Text = replyWithFacts
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
		Provider:   "openai/" + w.Model,
		Text:       reply,
		Remember:   remember,
		WhoTheyAre: whoTheyAre,
		Usage:      system2.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, ReasoningTokens: u.OutputTokensDetails.ReasoningTokens},
		CostUSD: c.price.Cost(pricing.Usage{
			InputTokens: u.InputTokens, CachedTokens: u.InputTokensDetails.CachedTokens,
			CacheWriteTokens: u.InputTokensDetails.CacheWriteTokens, OutputTokens: u.OutputTokens,
		}),
	}, nil
}
