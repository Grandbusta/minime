package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Grandbusta/minime/internal/pricing"
	"github.com/Grandbusta/minime/internal/providers/system2"
)

func newTestClient(t *testing.T, price pricing.Price, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After-Ms", "1") // the SDK's retries, without the wait
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return New("sk-test", "gpt-test", srv.URL, price, "")
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
			"text":"{\"reply\":\"Forest green, I think.\",\"who_they_are\":\" Sam is Tobi's cousin. \",\"remember\":[{\"about\":\"self\",\"fact\":\"Tobi's favourite colour is forest green\"},{\"about\":\"speaker\",\"fact\":\"Sam goes to Grace Chapel\"},{\"about\":\"speaker\",\"fact\":\"  \"},{\"about\":\"nobody\",\"fact\":\"x\"}],\"reminders\":[{\"when\":\"2026-10-30T09:00\",\"what\":\"call mum\"},{\"when\":\" \",\"what\":\"x\"}],\"permissions\":[{\"class\":\"reach_out\",\"verdict\":\"auto\"},{\"class\":\" \",\"verdict\":\"auto\"}],\"corrections\":[{\"fact\":\"f2\",\"action\":\"replace\",\"text\":\"Sam lives in Lekki\"},{\"fact\":\"f3\",\"action\":\"forget\",\"text\":\"\"},{\"fact\":\"f4\",\"action\":\"replace\",\"text\":\" \"}],\"misread\":\" ask_for_help \",\"send\":{\"work\":\" [w2] \",\"as\":\"PDF\"},\"lessons\":[{\"skill\":\"lyric writing\",\"lesson\":\" Keep hooks short \"},{\"skill\":\"\",\"lesson\":\"x\"}],\"feedback\":[{\"skill\":\"lyric writing\",\"level\":3,\"about\":\"the verse\"},{\"skill\":\"x\",\"level\":11,\"about\":\"\"}],\"promises\":[{\"by\":\"2026-10-30T20:00\",\"what\":\" Tobi writes Sam a verse \",\"ability\":\"\",\"query\":\"\",\"as\":\" PDF \"},{\"by\":\"\",\"what\":\"x\",\"ability\":\"\",\"query\":\"\"}]}"}]}]}`)
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
		!slices.Equal(resp.Promises, []system2.Promise{{By: "2026-10-30T20:00", What: "Tobi writes Sam a verse", As: "pdf"}}) ||
		resp.Send != (system2.Send{Work: "w2", As: "pdf"}) ||
		!slices.Equal(resp.Permissions, []system2.Permission{{Class: "reach_out", Verdict: "auto"}}) ||
		!slices.Equal(resp.Corrections, []system2.Correction{{Fact: "f2", Text: "Sam lives in Lekki"}, {Fact: "f3", Forget: true}}) ||
		resp.Misread != "ask_for_help" || !slices.Equal(resp.Feedback, []system2.Feedback{{Skill: "lyric writing", Level: 3, About: "the verse"}}) ||
		!slices.Equal(resp.Lessons, []system2.LessonLearned{{Skill: "lyric writing", Text: "Keep hooks short"}}) {
		t.Errorf("resp = %+v", resp)
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

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type wireText struct {
	Format struct {
		Type   string         `json:"type"`
		Name   string         `json:"name"`
		Strict bool           `json:"strict"`
		Schema map[string]any `json:"schema"`
	} `json:"format"`
}

func TestACutOffAnswerIsTriedAgainWithMoreRoom(t *testing.T) {
	var asked []int
	c := newTestClient(t, pricing.Price{}, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxOutputTokens int `json:"max_output_tokens"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		asked = append(asked, body.MaxOutputTokens)
		if len(asked) == 1 {
			io.WriteString(w, `{"model":"m","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},
				"output":[{"type":"message","content":[{"type":"output_text","text":"{\"title\":\"A pl"}]}]}`)
			return
		}
		io.WriteString(w, `{"model":"m","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"title\":\"A plan\"}"}]}]}`)
	})
	resp, err := c.Reason(context.Background(), system2.Request{Message: "x", MaxOutputTokens: 500,
		Format: &system2.Format{Name: "work", Schema: map[string]any{"type": "object"}}})
	if err != nil || resp.Text != `{"title":"A plan"}` || len(asked) != 2 || asked[0] != 500 || asked[1] != 1000 {
		t.Errorf("resp = %q, %v; asked with %v", resp.Text, err, asked)
	}
}

func TestACutOffAnswerIsAnError(t *testing.T) {
	calls := 0
	c := newTestClient(t, pricing.Price{}, func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `{"model":"m","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},
			"output":[{"type":"message","content":[{"type":"output_text","text":"{\"title\":\"A plan\",\"text\":\"Week one: lear"}]}]}`)
	})
	_, err := c.Reason(context.Background(), system2.Request{Message: "x", Format: &system2.Format{Name: "work", Schema: map[string]any{"type": "object"}}})
	if !errors.Is(err, system2.ErrCutOff) || !strings.Contains(err.Error(), "max_output_tokens") || calls != 2 {
		t.Errorf("err = %v after %d calls; want it cut off after trying twice", err, calls)
	}
}
