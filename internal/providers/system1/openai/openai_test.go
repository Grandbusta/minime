package openai

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/openai/openai-go/v3"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/pricing"
	"github.com/Grandbusta/minime/internal/providers/system1"
)

var questions = []system1.Question{
	{Name: "intent", Type: decision.QuestionChoice, Instructions: "What does `message` want?",
		Choices: []system1.Choice{{Value: "remember", Description: "a fact"}, {Value: "other"}}},
	{Name: "urgent", Type: decision.QuestionPredicate, Instructions: "Is `message` urgent?"},
	{Name: "severity", Type: decision.QuestionScore, Instructions: "How bad?",
		Levels: []system1.Level{{Label: "low"}, {Label: "high", Description: "blocking"}}},
}

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After-Ms", "1") // the SDK's retries, without the wait
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return New("sk-test", "", srv.URL, pricing.Price{Input: 0.10, Output: 0.50, CachedInput: 0.01, CacheWrite: 0.125})
}

const okResponse = `{
  "model": "gpt-6-luna",
  "answers": [
    {"type": "choice", "name": "intent", "choice": "remember", "confidence": 0.9,
     "probabilities": [{"value": "remember", "probability": 0.95}, {"value": "other", "probability": 0.05}]},
    {"type": "predicate", "name": "urgent", "probability": 0.2},
    {"type": "score", "name": "severity", "score": 0.3, "confidence": 0.6,
     "probabilities": [{"value": 0, "label": "low", "probability": 0.7}, {"value": 1, "label": "high", "probability": 0.3}]}
  ],
  "usage": {"input_tokens": 1000, "input_tokens_details": {"cached_tokens": 200, "cache_write_tokens": 0}, "output_tokens": 0}
}`

func TestDecideSendsRequestAndMapsAnswers(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/decisions" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("path %s, auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body struct {
			Model     string            `json:"model"`
			Input     []json.RawMessage `json:"input"`
			Questions []map[string]any  `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != DefaultModel || len(body.Questions) != 3 || len(body.Input) != 1 {
			t.Errorf("body = %+v", body)
		}
		if body.Questions[0]["type"] != "choice" || len(body.Questions[0]["choices"].([]any)) != 2 ||
			body.Questions[2]["levels"] == nil || body.Questions[1]["choices"] != nil {
			t.Errorf("questions = %v", body.Questions)
		}
		io.WriteString(w, okResponse)
	})

	resp, err := c.Decide(context.Background(), system1.Request{
		State: map[string]any{"message": "my wifi password is hunter2"}, Questions: questions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "openai/gpt-6-luna" || resp.Usage.InputTokens != 1000 || resp.Usage.CachedTokens != 200 {
		t.Errorf("resp = %+v", resp)
	}
	// 800 uncached × $0.10 + 200 cached × $0.01 per 1M; output is free here.
	if want := (800*0.10 + 200*0.01) / 1e6; resp.CostUSD < want-1e-12 || resp.CostUSD > want+1e-12 {
		t.Errorf("cost = %v", resp.CostUSD)
	}
	intent := resp.Answers["intent"]
	if intent.Choice != "remember" || intent.Confidence != 0.9 || intent.Probabilities["other"] != 0.05 {
		t.Errorf("intent = %+v", intent)
	}
	if resp.Answers["urgent"].Probability != 0.2 {
		t.Errorf("urgent = %+v", resp.Answers["urgent"])
	}
	if sev := resp.Answers["severity"]; sev.Score != 0.3 || sev.Probabilities["0"] != 0.7 {
		t.Errorf("severity = %+v", sev)
	}
}

func TestStringStateIsSentAsText(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Input != "hello" {
			t.Errorf("input = %q", body.Input)
		}
		io.WriteString(w, `{"model":"gpt-6-luna","answers":[{"type":"predicate","name":"urgent","probability":0.1}]}`)
	})
	if _, err := c.Decide(context.Background(), system1.Request{State: "hello", Questions: questions[1:2]}); err != nil {
		t.Fatal(err)
	}
}

func TestRefusalIsKeptPerQuestion(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"model":"gpt-6-luna","answers":[
			{"type":"refusal","name":"intent"},
			{"type":"predicate","name":"urgent","probability":0.4},
			{"type":"refusal","name":null}]}`)
	})
	resp, err := c.Decide(context.Background(), system1.Request{State: "x", Questions: questions})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Answers["intent"].Refused || !resp.Answers["severity"].Refused || resp.Answers["urgent"].Probability != 0.4 {
		t.Errorf("answers = %+v", resp.Answers)
	}
}

func TestBooleanChoiceValues(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"model":"gpt-6-luna","answers":[{"type":"choice","name":"intent","choice":true,"confidence":1,
			"probabilities":[{"value":true,"probability":1},{"value":false,"probability":0}]}]}`)
	})
	resp, err := c.Decide(context.Background(), system1.Request{State: "x", Questions: questions[:1]})
	if err != nil {
		t.Fatal(err)
	}
	if a := resp.Answers["intent"]; a.Choice != "true" || a.Probabilities["false"] != 0 {
		t.Errorf("intent = %+v", a)
	}
}

func TestRetriesRateLimitThenSucceeds(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"slow down","type":"rate_limit_error"}}`)
			return
		}
		io.WriteString(w, `{"model":"gpt-6-luna","answers":[{"type":"predicate","name":"urgent","probability":0.1}]}`)
	})
	if _, err := c.Decide(context.Background(), system1.Request{State: "x", Questions: questions[1:2]}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestClientErrorIsNotRetried(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`)
	})
	_, err := c.Decide(context.Background(), system1.Request{State: "x", Questions: questions[1:2]})
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 || apiErr.Code != "invalid_api_key" {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestAnswerNameMismatchIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"model":"gpt-6-luna","answers":[{"type":"predicate","name":"something_else","probability":0.1}]}`)
	})
	if _, err := c.Decide(context.Background(), system1.Request{State: "x", Questions: questions[1:2]}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestAnswerCountMismatchIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"model":"gpt-6-luna","answers":[]}`)
	})
	if _, err := c.Decide(context.Background(), system1.Request{State: "x", Questions: questions[1:2]}); err == nil {
		t.Fatal("expected an error")
	}
}
