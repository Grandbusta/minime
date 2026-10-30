// Package system1 defines the provider-neutral interface to fast decision
// models. Each provider lives in its own subpackage.
package system1

import (
	"context"

	"github.com/Grandbusta/minime/internal/decision"
)

type DecisionModel interface {
	Decide(ctx context.Context, req Request) (Response, error)
}

type Request struct {
	// State is the evidence every question is about: a string, or any value
	// that marshals to a JSON object (or array of objects).
	State     any
	Questions []Question // independent questions, all answered in one call
}

type Question struct {
	Name         string                `yaml:"name"`
	Type         decision.QuestionType `yaml:"type"`
	Instructions string                `yaml:"instructions"`
	Choices      []Choice              `yaml:"choices"` // choice only, 2–255
	Levels       []Level               `yaml:"levels"`  // score only, 2–10, lowest first
}

type Choice struct {
	Value       string `yaml:"value"`
	Description string `yaml:"description"`
}

type Level struct {
	Label       string `yaml:"label"`
	Description string `yaml:"description"`
}

type Response struct {
	Provider string
	Answers  map[string]decision.Answer // by question name
	Usage    Usage
	CostUSD  float64
}

type Usage struct {
	InputTokens  int
	CachedTokens int
}
