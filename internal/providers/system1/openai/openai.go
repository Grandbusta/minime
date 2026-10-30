// Package openai is the System 1 provider for OpenAI's Decisions API.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"

	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/pricing"
	"github.com/Grandbusta/minime/internal/providers/openaiclient"
	"github.com/Grandbusta/minime/internal/providers/system1"
)

const (
	DefaultModel   = "gpt-6-luna" // the only model the Decisions API supports today
	requestTimeout = 30 * time.Second
)

type Client struct {
	api   openai.Client
	model string
	price pricing.Price
}

// Empty model and baseURL fall back to the defaults.
func New(apiKey, model, baseURL string, price pricing.Price) *Client {
	if model == "" {
		model = DefaultModel
	}
	return &Client{api: openaiclient.New(apiKey, baseURL, requestTimeout), model: model, price: price}
}

func (c *Client) Decide(ctx context.Context, req system1.Request) (system1.Response, error) {
	params, err := c.params(req)
	if err != nil {
		return system1.Response{}, err
	}
	resp, err := c.api.Decisions.New(ctx, params)
	if err != nil {
		return system1.Response{}, fmt.Errorf("openai decisions: %w", err)
	}
	return toResponse(req, resp, c.price)
}

func (c *Client) params(req system1.Request) (openai.DecisionNewParams, error) {
	p := openai.DecisionNewParams{Model: c.model}
	input, err := wireInput(req.State)
	if err != nil {
		return p, err
	}
	p.Input = input
	for _, q := range req.Questions {
		var wq openai.DecisionNewParamsQuestionUnion
		switch q.Type {
		case decision.QuestionPredicate:
			wq.OfPredicate = &openai.DecisionNewParamsQuestionPredicate{Name: param.NewOpt(q.Name),
				Instructions: openai.DecisionNewParamsQuestionPredicateInstructionsUnion{OfString: param.NewOpt(q.Instructions)}}
		case decision.QuestionChoice:
			ch := &openai.DecisionNewParamsQuestionChoice{Name: param.NewOpt(q.Name),
				Instructions: openai.DecisionNewParamsQuestionChoiceInstructionsUnion{OfString: param.NewOpt(q.Instructions)}}
			for _, c := range q.Choices {
				wc := openai.DecisionNewParamsQuestionChoiceChoice{
					Value: openai.DecisionNewParamsQuestionChoiceChoiceValueUnion{OfString: param.NewOpt(c.Value)}}
				if c.Description != "" {
					wc.Description = param.NewOpt(c.Description)
				}
				ch.Choices = append(ch.Choices, wc)
			}
			wq.OfChoice = ch
		case decision.QuestionScore:
			sc := &openai.DecisionNewParamsQuestionScore{Name: param.NewOpt(q.Name),
				Instructions: openai.DecisionNewParamsQuestionScoreInstructionsUnion{OfString: param.NewOpt(q.Instructions)}}
			for _, l := range q.Levels {
				wl := openai.DecisionNewParamsQuestionScoreLevel{Label: l.Label}
				if l.Description != "" {
					wl.Description = param.NewOpt(l.Description)
				}
				sc.Levels = append(sc.Levels, wl)
			}
			wq.OfScore = sc
		default:
			return p, fmt.Errorf("openai decisions: question %q has unknown type %q", q.Name, q.Type)
		}
		p.Questions = append(p.Questions, wq)
	}
	return p, nil
}

// wireInput sends a string state as plain text and anything else as a JSON
// part of a user message, so its structure reaches the model intact.
func wireInput(state any) (openai.DecisionNewParamsInputUnion, error) {
	if s, ok := state.(string); ok {
		return openai.DecisionNewParamsInputUnion{OfString: param.NewOpt(s)}, nil
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return openai.DecisionNewParamsInputUnion{}, fmt.Errorf("openai decisions: state: %w", err)
	}
	var data shared.DecisionInputJSONDataUnionParam
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		err = json.Unmarshal(raw, &data.OfArrayOfValues)
	} else {
		err = json.Unmarshal(raw, &data.OfAnyMap)
	}
	if err != nil {
		return openai.DecisionNewParamsInputUnion{}, fmt.Errorf("openai decisions: state must be an object or a list of objects: %w", err)
	}
	part := openai.DecisionInputPartUnionParam{OfInputJSON: &shared.DecisionInputJSONParam{Data: data}}
	return openai.DecisionNewParamsInputUnion{OfDecisionInputMessageArray: []openai.DecisionInputMessageParam{{
		Content: openai.DecisionInputMessageContentUnionParam{OfParts: []openai.DecisionInputPartUnionParam{part}}}}}, nil
}

func toResponse(req system1.Request, w *openai.Decision, price pricing.Price) (system1.Response, error) {
	if len(w.Answers) != len(req.Questions) {
		return system1.Response{}, fmt.Errorf("openai decisions: got %d answers for %d questions", len(w.Answers), len(req.Questions))
	}
	u, d := w.Usage, w.Usage.InputTokensDetails
	resp := system1.Response{
		Provider: "openai/" + w.Model,
		Answers:  make(map[string]decision.Answer, len(w.Answers)),
		Usage:    system1.Usage{InputTokens: int(u.InputTokens), CachedTokens: int(d.CachedTokens)},
		CostUSD: price.Cost(pricing.Usage{ // output tokens are not charged on Decisions
			InputTokens: int(u.InputTokens), CachedTokens: int(d.CachedTokens), CacheWriteTokens: int(d.CacheWriteTokens),
		}),
	}
	// Answers come back in question order. The echoed name (empty if the model
	// refused an unnamed question) double-checks that pairing.
	for i, wa := range w.Answers {
		q := req.Questions[i]
		if wa.Name != "" && wa.Name != q.Name {
			return system1.Response{}, fmt.Errorf("openai decisions: answer %d is for %q, expected %q", i, wa.Name, q.Name)
		}
		a := decision.Answer{Type: q.Type}
		switch wa.Type {
		case "refusal":
			a.Refused = true
		case "predicate":
			a.Probability = wa.Probability
		case "choice":
			ch := wa.AsChoice()
			a.Choice, a.Confidence = rawValue(ch.Choice.RawJSON()), ch.Confidence
			a.Probabilities = make(map[string]float64, len(ch.Probabilities))
			for _, p := range ch.Probabilities {
				a.Probabilities[rawValue(p.Value.RawJSON())] = p.Probability
			}
		case "score":
			sc := wa.AsScore()
			a.Score, a.Confidence = sc.Score, sc.Confidence
			a.Probabilities = make(map[string]float64, len(sc.Probabilities))
			for _, p := range sc.Probabilities {
				a.Probabilities[strconv.FormatInt(p.Value, 10)] = p.Probability
			}
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

func rawValue(raw string) string {
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err == nil {
		return s
	}
	return string(bytes.TrimSpace([]byte(raw))) // true, false
}
