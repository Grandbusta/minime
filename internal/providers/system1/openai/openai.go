// Package openai is the System 1 provider for OpenAI's Decisions API (POST /v1/decisions).
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"minime/internal/decision"
	"minime/internal/pricing"
	"minime/internal/providers/openaihttp"
	"minime/internal/providers/system1"
)

const (
	DefaultModel   = "gpt-6-luna" // the only model the Decisions API supports today
	requestTimeout = 30 * time.Second
)

type Client struct {
	api   *openaihttp.Client
	model string
	price pricing.Price // the model's input and cache rates; Decisions output is free
}

// New returns a client. Empty model and baseURL fall back to the defaults.
func New(apiKey, model, baseURL string, price pricing.Price) *Client {
	if model == "" {
		model = DefaultModel
	}
	return &Client{api: openaihttp.New(apiKey, baseURL, requestTimeout), model: model, price: price}
}

func (c *Client) Decide(ctx context.Context, req system1.Request) (system1.Response, error) {
	var resp wireResponse
	if err := c.api.Post(ctx, "/decisions", c.wireRequest(req), &resp); err != nil {
		return system1.Response{}, err
	}
	return toResponse(req, resp, c.price)
}

// --- request ---

type wireRequest struct {
	Model     string         `json:"model"`
	Input     any            `json:"input"`
	Questions []wireQuestion `json:"questions"`
}

type wireQuestion struct {
	Type         string       `json:"type"`
	Name         string       `json:"name"`
	Instructions string       `json:"instructions"`
	Choices      []wireChoice `json:"choices,omitempty"`
	Levels       []wireLevel  `json:"levels,omitempty"`
}

type wireChoice struct {
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

type wireLevel struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func (c *Client) wireRequest(req system1.Request) wireRequest {
	w := wireRequest{Model: c.model, Input: wireInput(req.State)}
	for _, q := range req.Questions {
		wq := wireQuestion{Type: string(q.Type), Name: q.Name, Instructions: q.Instructions}
		for _, ch := range q.Choices {
			wq.Choices = append(wq.Choices, wireChoice{Value: ch.Value, Description: ch.Description})
		}
		for _, l := range q.Levels {
			wq.Levels = append(wq.Levels, wireLevel{Label: l.Label, Description: l.Description})
		}
		w.Questions = append(w.Questions, wq)
	}
	return w
}

// wireInput sends a string state as plain text and anything else as a JSON
// part of a user message, so its structure reaches the model intact.
func wireInput(state any) any {
	if s, ok := state.(string); ok {
		return s
	}
	return []map[string]any{{
		"role":    "user",
		"content": []map[string]any{{"type": "input_json", "data": state}},
	}}
}

// --- response ---

type wireResponse struct {
	Model   string       `json:"model"`
	Answers []wireAnswer `json:"answers"`
	Usage   struct {
		InputTokens        int `json:"input_tokens"`
		InputTokensDetails struct {
			CachedTokens     int `json:"cached_tokens"`
			CacheWriteTokens int `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}

type wireAnswer struct {
	Type          string            `json:"type"` // predicate | choice | score | refusal
	Name          *string           `json:"name"`
	Probability   float64           `json:"probability"`
	Choice        json.RawMessage   `json:"choice"` // string or boolean
	Score         float64           `json:"score"`
	Confidence    float64           `json:"confidence"`
	Probabilities []wireProbability `json:"probabilities"`
}

type wireProbability struct {
	Value       json.RawMessage `json:"value"` // choice: string or boolean; score: level index
	Probability float64         `json:"probability"`
}

func toResponse(req system1.Request, w wireResponse, price pricing.Price) (system1.Response, error) {
	if len(w.Answers) != len(req.Questions) {
		return system1.Response{}, fmt.Errorf("openai decisions: got %d answers for %d questions", len(w.Answers), len(req.Questions))
	}
	d := w.Usage.InputTokensDetails
	resp := system1.Response{
		Provider: "openai/" + w.Model,
		Answers:  make(map[string]decision.Answer, len(w.Answers)),
		Usage:    system1.Usage{InputTokens: w.Usage.InputTokens, CachedTokens: d.CachedTokens},
		CostUSD: price.Cost(pricing.Usage{ // output tokens are not charged on /v1/decisions
			InputTokens: w.Usage.InputTokens, CachedTokens: d.CachedTokens, CacheWriteTokens: d.CacheWriteTokens,
		}),
	}
	// Answers come back in question order. The echoed name (null if the model
	// refused an unnamed question) double-checks that pairing.
	for i, wa := range w.Answers {
		q := req.Questions[i]
		if wa.Name != nil && *wa.Name != q.Name {
			return system1.Response{}, fmt.Errorf("openai decisions: answer %d is for %q, expected %q", i, *wa.Name, q.Name)
		}
		a := decision.Answer{Type: q.Type}
		switch wa.Type {
		case "refusal":
			a.Refused = true
		case "predicate":
			a.Probability = wa.Probability
		case "choice":
			a.Choice = rawValue(wa.Choice)
			a.Confidence = wa.Confidence
			a.Probabilities = probabilities(wa.Probabilities)
		case "score":
			a.Score = wa.Score
			a.Confidence = wa.Confidence
			a.Probabilities = probabilities(wa.Probabilities)
		default:
			return system1.Response{}, fmt.Errorf("openai decisions: unknown answer type %q for %q", wa.Type, q.Name)
		}
		if !a.Refused && wa.Type != string(q.Type) {
			return system1.Response{}, fmt.Errorf("openai decisions: %q answered as %s, asked as %s", q.Name, wa.Type, q.Type)
		}
		resp.Answers[q.Name] = a
	}
	return resp, nil
}

func probabilities(ps []wireProbability) map[string]float64 {
	m := make(map[string]float64, len(ps))
	for _, p := range ps {
		m[rawValue(p.Value)] = p.Probability
	}
	return m
}

// rawValue turns a JSON string, boolean or number into its plain text form.
func rawValue(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(bytes.TrimSpace(raw)) // true, false, 2
}
