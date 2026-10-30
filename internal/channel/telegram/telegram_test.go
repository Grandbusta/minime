package telegram

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Grandbusta/minime/internal/perception"
)

const testToken = "123:SECRET"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// owned is a channel with one owner, 42, that only reads updates.
var owned = &Channel{owners: map[int64]bool{42: true}, log: quiet}

func newChannel(t *testing.T, url string, out chan<- perception.Percept) *Channel {
	c, err := New(testToken, []int64{42}, time.Second, out, quiet, bot.WithServerURL(url))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// fakeAPI is the Bot API: it records each call (method and form fields) and
// answers with the result reply gives, or {} by default.
type fakeAPI struct {
	mu    sync.Mutex
	calls []map[string]string
	reply func(method string) (status int, result string)
}

func (f *fakeAPI) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if !strings.HasPrefix(r.URL.Path, "/bot"+testToken+"/") {
			t.Errorf("path %s", r.URL.Path)
		}
		r.ParseMultipartForm(1 << 20)
		call := map[string]string{"method": method}
		for k, v := range r.Form {
			call[k] = v[0]
		}
		f.mu.Lock()
		f.calls = append(f.calls, call)
		f.mu.Unlock()
		status, result := http.StatusOK, `{"message_id":77,"date":0,"chat":{"id":7,"type":"private"}}`
		if f.reply != nil {
			status, result = f.reply(method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != http.StatusOK {
			io.WriteString(w, `{"ok":false,"error_code":`+itoa(status)+`,"description":"`+result+`"}`)
			return
		}
		io.WriteString(w, `{"ok":true,"result":`+result+`}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeAPI) sent(method string) []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]string
	for _, c := range f.calls {
		if c["method"] == method {
			out = append(out, c)
		}
	}
	return out
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestChannelKeepsOnlyOwnerMessages(t *testing.T) {
	s := owned

	owner := &models.Update{ID: 1, Message: &models.Message{ID: 9, From: &models.User{ID: 42, FirstName: "Sam"}, Chat: models.Chat{ID: 7},
		Date: 1700000000, Text: "hi"}}
	p, ok := s.percept(owner)
	if !ok {
		t.Fatal("owner message dropped")
	}
	if p.Source != Source || p.ExternalID != "7:9" || p.Thread != "7" || p.Text != "hi" || p.Kind != "message" ||
		p.Speaker != "telegram:42" || p.SpeakerName != "Sam" || !p.At.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("percept = %+v", p)
	}
	var pl MessagePayload
	if err := json.Unmarshal(p.Payload, &pl); err != nil || pl.ChatID != 7 || pl.MessageID != 9 || pl.UserID != 42 {
		t.Fatalf("payload = %s (%v)", p.Payload, err)
	}

	stranger := &models.Update{ID: 2, Message: &models.Message{ID: 10, From: &models.User{ID: 99}, Chat: models.Chat{ID: 8}, Text: "hey"}}
	if _, ok := s.percept(stranger); ok {
		t.Fatal("non-owner message kept")
	}
	if _, ok := s.percept(&models.Update{ID: 3}); ok {
		t.Fatal("update without message kept")
	}
}

func TestChannelRunsOnTheBotAPI(t *testing.T) {
	api := &fakeAPI{}
	served := false
	api.reply = func(method string) (int, string) {
		switch method {
		case "getMe":
			return http.StatusOK, `{"id":1,"is_bot":true,"first_name":"Bot","username":"test_bot"}`
		case "getUpdates":
			api.mu.Lock()
			defer api.mu.Unlock()
			if served {
				return http.StatusOK, `[]`
			}
			served = true
			return http.StatusOK, `[{"update_id":5,"message":{"message_id":9,"date":1700000000,"text":"hello","chat":{"id":7,"type":"private"},
				"from":{"id":42,"is_bot":false,"first_name":"Sam"}}},
				{"update_id":6,"message":{"message_id":10,"date":1700000001,"text":"spam","chat":{"id":8,"type":"private"},
				"from":{"id":99,"is_bot":false,"first_name":"X"}}}]`
		}
		return http.StatusOK, `true`
	}
	srv := api.serve(t)
	got := make(chan perception.Percept, 4)
	s := newChannel(t, srv.URL, got)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go s.Run(ctx)
	select {
	case p := <-got:
		if p.Text != "hello" || p.Speaker != "telegram:42" {
			t.Errorf("percept = %+v", p)
		}
	case <-ctx.Done():
		t.Fatal("no percept from the owner's message")
	}
}

func TestChannelStopsOnABadToken(t *testing.T) {
	api := &fakeAPI{reply: func(string) (int, string) { return http.StatusUnauthorized, "Unauthorized" }}
	srv := api.serve(t)
	s := newChannel(t, srv.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Run(ctx); err == nil || !strings.Contains(err.Error(), "rejected the bot token") {
		t.Errorf("err = %v", err)
	}
}

func TestSplitLongMessages(t *testing.T) {
	para := strings.Repeat("word ", 300)
	text := "Here it is.\n\n" + para + "\n\n" + para + "\n\n" + para + "end"
	parts := split(text, 4096)
	if len(parts) != 2 || strings.Join(strings.Fields(strings.Join(parts, " ")), " ") != strings.Join(strings.Fields(text), " ") {
		t.Errorf("%d parts, or words lost", len(parts))
	}
	for _, p := range parts {
		if n := len([]rune(p)); n > 4096 || n == 0 {
			t.Errorf("part of %d runes", n)
		}
	}
	if !strings.HasPrefix(parts[1], "word") || !strings.HasSuffix(parts[0], "word") {
		t.Errorf("not cut at a paragraph: %q / %q", parts[0][len(parts[0])-10:], parts[1][:10])
	}
	if got := split("short", 4096); len(got) != 1 || got[0] != "short" {
		t.Errorf("short = %q", got)
	}
	if got := split(strings.Repeat("é", 10000), 4096); len(got) != 3 || len([]rune(got[0])) != 4096 {
		t.Errorf("no breaks: %d parts", len(got))
	}
}

func tap(from int64, data string) *models.Update {
	return &models.Update{ID: 1, CallbackQuery: &models.CallbackQuery{ID: "cb1", From: models.User{ID: from, FirstName: "Busta"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 5, Chat: models.Chat{ID: 7}}}, Data: data}}
}

func TestChannelTurnsButtonTapsIntoAnswers(t *testing.T) {
	s := owned
	p, ok := s.percept(tap(42, "gate:01ABC:y"))
	if !ok || p.Kind != perception.KindApproval || p.Text != "" || p.Speaker != "telegram:42" || p.ExternalID != "callback:cb1" || p.Thread != "7" {
		t.Fatalf("percept = %+v, %v", p, ok)
	}
	var a perception.Answer
	if err := json.Unmarshal(p.Payload, &a); err != nil || a.Approval != "01ABC" || !a.Approve {
		t.Errorf("answer = %+v, %v", a, err)
	}
	if p, _ := s.percept(tap(42, "gate:01ABC:n")); !strings.Contains(string(p.Payload), `"approve":false`) {
		t.Errorf("deny = %s", p.Payload)
	}
	for _, u := range []*models.Update{tap(99, "gate:01ABC:y"), tap(42, "other:1"), tap(42, "gate:01ABC:maybe")} {
		if _, ok := s.percept(u); ok {
			t.Errorf("kept %+v", u.CallbackQuery)
		}
	}
}

func TestAskAndSettle(t *testing.T) {
	api := &fakeAPI{}
	c := newChannel(t, api.serve(t).URL, nil)
	p := perception.Percept{Source: Source, Payload: json.RawMessage(`{"chat_id":7,"message_id":1,"user_id":42}`)}
	where, err := c.Ask(context.Background(), p, "Can I?", "01ABC")
	if err != nil || string(where) != `{"chat_id":7,"message_id":77}` {
		t.Fatalf("ask = %s, %v", where, err)
	}
	sent := api.sent("sendMessage")
	if len(sent) != 1 || sent[0]["chat_id"] != "7" || sent[0]["text"] != "Can I?" ||
		!strings.Contains(sent[0]["reply_markup"], `"callback_data":"gate:01ABC:y"`) || !strings.Contains(sent[0]["reply_markup"], `"callback_data":"gate:01ABC:n"`) {
		t.Errorf("sent %v", sent)
	}
	if err := c.Settle(context.Background(), where, "Can I?\n\n✅ You said yes."); err != nil {
		t.Fatal(err)
	}
	edited := api.sent("editMessageText")
	if len(edited) != 1 || edited[0]["message_id"] != "77" || edited[0]["reply_markup"] != "" || !strings.Contains(edited[0]["text"], "You said yes") {
		t.Errorf("settled %v", edited)
	}
}

func TestCallButtons(t *testing.T) {
	s := owned
	for data, want := range map[string]int{"call:01ABC:1": 1, "call:01ABC:x": -1} {
		p, ok := s.percept(tap(42, data))
		var pick perception.Pick
		if !ok || p.Kind != perception.KindCall || json.Unmarshal(p.Payload, &pick) != nil || pick.Call != "01ABC" || pick.Option != want {
			t.Errorf("%s: percept = %+v, %v; pick %+v", data, p, ok, pick)
		}
	}
	for _, u := range []*models.Update{tap(99, "call:01ABC:1"), tap(42, "call:01ABC:two"), tap(42, "call::1"), tap(42, "call:01ABC:-3")} {
		if _, ok := s.percept(u); ok {
			t.Errorf("kept %+v", u.CallbackQuery)
		}
	}

	api := &fakeAPI{}
	c := newChannel(t, api.serve(t).URL, nil)
	p := perception.Percept{Source: Source, Payload: json.RawMessage(`{"chat_id":7,"message_id":1,"user_id":42}`)}
	where, err := c.Choose(context.Background(), p, "What would you do?", "01ABC", 3, 1)
	if err != nil || string(where) != `{"chat_id":7,"message_id":77}` {
		t.Fatalf("choose = %s, %v", where, err)
	}
	kb := api.sent("sendMessage")[0]["reply_markup"]
	for _, want := range []string{`"text":"2 ★","callback_data":"call:01ABC:1"`, `"text":"3","callback_data":"call:01ABC:2"`, `"callback_data":"call:01ABC:x"`} {
		if !strings.Contains(kb, want) {
			t.Errorf("keyboard %s is missing %s", kb, want)
		}
	}
}
