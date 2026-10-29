package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"minime/internal/pricing"
	"minime/internal/providers/system2"
)

func newTestClient(t *testing.T, price pricing.Price, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("sk-test", "gpt-test", srv.URL, price, "")
	c.api.RetryWait = time.Millisecond
	return c
}

func TestReasonSendsRequestAndJoinsText(t *testing.T) {
	c := newTestClient(t, pricing.Price{Input: 1, Output: 4, CachedInput: 0.1}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body struct {
			Model           string        `json:"model"`
			Instructions    string        `json:"instructions"`
			Input           []wireMessage `json:"input"`
			MaxOutputTokens int           `json:"max_output_tokens"`
			Store           *bool         `json:"store"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "gpt-test" || body.Instructions != "be brief" ||
			body.MaxOutputTokens != defaultMaxOutputTokens || body.Store == nil || *body.Store {
			t.Errorf("body = %+v", body)
		}
		want := []wireMessage{
			{"user", "I want help finding a dentist"},
			{"assistant", "Which city?"},
			{"developer", "the fast read was unsure"},
			{"user", "Lagos"},
		}
		if !slices.Equal(body.Input, want) {
			t.Errorf("input = %+v, want %+v", body.Input, want)
		}
		io.WriteString(w, `{"model":"gpt-test-2026","status":"completed","output":[
			{"type":"reasoning","content":[]},
			{"type":"message","content":[{"type":"output_text","text":"Hello "},{"type":"output_text","text":"there."}]}],
			"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":200},"output_tokens":500,"output_tokens_details":{"reasoning_tokens":100}}}`)
	})

	resp, err := c.Reason(context.Background(), system2.Request{
		Instructions: "be brief",
		History: []system2.Turn{
			{Role: system2.RolePerson, Text: "I want help finding a dentist"},
			{Role: system2.RoleAgent, Text: "Which city?"},
		},
		Note:    "the fast read was unsure",
		Message: "Lagos",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Hello there." || resp.Provider != "openai/gpt-test-2026" || resp.Usage.ReasoningTokens != 100 {
		t.Errorf("resp = %+v", resp)
	}
	if want := (800*1.0 + 200*0.1 + 500*4.0) / 1e6; resp.CostUSD < want-1e-12 || resp.CostUSD > want+1e-12 {
		t.Errorf("cost = %v, want %v", resp.CostUSD, want)
	}
}

func TestCollectFactsUsesStructuredOutput(t *testing.T) {
	c := newTestClient(t, pricing.Price{}, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text *wireText `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Text == nil || body.Text.Format.Type != "json_schema" || !body.Text.Format.Strict {
			t.Errorf("text format = %+v", body.Text)
		}
		io.WriteString(w, `{"model":"m","status":"completed","output":[{"type":"message","content":[{"type":"output_text",
			"text":"{\"reply\":\"Forest green, I think.\",\"who_they_are\":\" Sam is Tobi's cousin. \",\"remember\":[{\"about\":\"self\",\"fact\":\"Tobi's favourite colour is forest green\"},{\"about\":\"speaker\",\"fact\":\"Sam goes to Grace Chapel\"},{\"about\":\"speaker\",\"fact\":\"  \"},{\"about\":\"nobody\",\"fact\":\"x\"}],\"reminders\":[{\"when\":\"2026-10-30T09:00\",\"what\":\"call mum\"},{\"when\":\" \",\"what\":\"x\"}],\"promises\":[{\"by\":\"2026-10-30T20:00\",\"what\":\" Tobi writes Sam a verse \",\"ability\":\"\",\"query\":\"\"},{\"by\":\"\",\"what\":\"x\",\"ability\":\"\",\"query\":\"\"}]}"}]}]}`)
	})
	resp, err := c.Reason(context.Background(), system2.Request{Message: "What's your favourite colour?", CollectFacts: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []system2.Memory{
		{About: system2.AboutSelf, Text: "Tobi's favourite colour is forest green"},
		{About: system2.AboutSpeaker, Text: "Sam goes to Grace Chapel"},
	}
	if resp.Text != "Forest green, I think." || !slices.Equal(resp.Remember, want) || resp.WhoTheyAre != "Sam is Tobi's cousin." ||
		!slices.Equal(resp.Reminders, []system2.Reminder{{When: "2026-10-30T09:00", What: "call mum"}}) ||
		!slices.Equal(resp.Promises, []system2.Promise{{By: "2026-10-30T20:00", What: "Tobi writes Sam a verse"}}) {
		t.Errorf("resp = %+v", resp)
	}
}

func TestFormatAsksForThatSchema(t *testing.T) {
	c := newTestClient(t, pricing.Price{}, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text *wireText `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if f := body.Text; f == nil || f.Format.Name != "page" || !f.Format.Strict || f.Format.Schema["type"] != "object" {
			t.Errorf("text format = %+v", body.Text)
		}
		io.WriteString(w, `{"model":"m","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":" {\"about\":\"hi\"} "}]}]}`)
	})
	resp, err := c.Reason(context.Background(), system2.Request{Message: "x",
		Format: &system2.Format{Name: "page", Schema: map[string]any{"type": "object"}}})
	if err != nil || resp.Text != `{"about":"hi"}` {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
}

func TestSpeakingFirstSendsNoUserMessage(t *testing.T) {
	c := newTestClient(t, pricing.Price{}, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []wireMessage `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if want := []wireMessage{{"developer", "remind Sam to call mum"}}; !slices.Equal(body.Input, want) {
			t.Errorf("input = %+v, want %+v", body.Input, want)
		}
		io.WriteString(w, `{"model":"m","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Hey Sam!"}]}]}`)
	})
	if _, err := c.Reason(context.Background(), system2.Request{Note: "remind Sam to call mum"}); err != nil {
		t.Fatal(err)
	}
}

func TestPlainRequestHasNoFormat(t *testing.T) {
	c := newTestClient(t, pricing.Price{}, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["text"]; ok {
			t.Errorf("unexpected text format: %v", body["text"])
		}
		io.WriteString(w, `{"model":"m","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"plain"}]}]}`)
	})
	resp, err := c.Reason(context.Background(), system2.Request{Message: "x"})
	if err != nil || resp.Text != "plain" || resp.Remember != nil {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
}

func TestCollectFactsRefusalIsPlainText(t *testing.T) {
	c := newTestClient(t, pricing.Price{}, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"model":"m","status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"I can't help with that."}]}]}`)
	})
	resp, err := c.Reason(context.Background(), system2.Request{Message: "x", CollectFacts: true})
	if err != nil || resp.Text != "I can't help with that." || resp.Remember != nil {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
}

func TestCollectFactsBadJSONIsAnError(t *testing.T) {
	c := newTestClient(t, pricing.Price{}, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"model":"m","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"not json"}]}]}`)
	})
	if _, err := c.Reason(context.Background(), system2.Request{Message: "x", CollectFacts: true}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestReasoningEffortIsSentWhenSet(t *testing.T) {
	var got []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body["reasoning"])
		io.WriteString(w, `{"model":"m","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	}))
	t.Cleanup(srv.Close)

	for _, effort := range []string{"low", ""} {
		if _, err := New("sk", "m", srv.URL, pricing.Price{}, effort).Reason(context.Background(), system2.Request{Message: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if r, ok := got[0].(map[string]any); !ok || r["effort"] != "low" {
		t.Errorf("effort low: reasoning = %v", got[0])
	}
	if got[1] != nil {
		t.Errorf("no effort set: reasoning = %v, want it left out", got[1])
	}
}

func TestNoPricesMeansZeroCost(t *testing.T) {
	c := newTestClient(t, pricing.Price{}, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"model":"m","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],
			"usage":{"input_tokens":10,"output_tokens":5}}`)
	})
	resp, err := c.Reason(context.Background(), system2.Request{Message: "x"})
	if err != nil || resp.CostUSD != 0 {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
}

func TestRefusalIsReturnedAsText(t *testing.T) {
	c := newTestClient(t, pricing.Price{}, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"model":"m","status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"I can't help with that."}]}]}`)
	})
	resp, err := c.Reason(context.Background(), system2.Request{Message: "x"})
	if err != nil || resp.Text != "I can't help with that." {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
}

func TestEmptyIncompleteResponseIsAnError(t *testing.T) {
	c := newTestClient(t, pricing.Price{}, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"model":"m","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},
			"output":[{"type":"reasoning","content":[]}]}`)
	})
	if _, err := c.Reason(context.Background(), system2.Request{Message: "x"}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestResponseErrorObjectIsAnError(t *testing.T) {
	c := newTestClient(t, pricing.Price{}, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"model":"m","status":"failed","error":{"code":"server_error","message":"boom"},"output":[]}`)
	})
	if _, err := c.Reason(context.Background(), system2.Request{Message: "x"}); err == nil {
		t.Fatal("expected an error")
	}
}
