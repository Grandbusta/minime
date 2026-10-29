package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"minime/internal/bus"
	"minime/internal/perception"
)

const testToken = "123:SECRET"

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{base: srv.URL + "/bot" + testToken, http: srv.Client()}
}

func TestGetUpdatesDecodesResult(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["offset"].(float64) != 5 {
			t.Errorf("offset = %v", body["offset"])
		}
		io.WriteString(w, `{"ok":true,"result":[{"update_id":5,"message":{"message_id":9,"from":{"id":42},"chat":{"id":7},"date":1700000000,"text":"hi"}}]}`)
	})

	updates, err := c.GetUpdates(context.Background(), 5, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].Message.Text != "hi" || updates[0].Message.From.ID != 42 {
		t.Fatalf("updates = %+v", updates)
	}
}

func TestAPIErrorCarriesRetryAfter(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":3}}`)
	})

	err := c.SendMessage(context.Background(), 1, "x")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 429 || apiErr.RetryAfter != 3*time.Second {
		t.Fatalf("err = %v", err)
	}
}

func TestTransportErrorHidesToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	c := &Client{base: srv.URL + "/bot" + testToken, http: srv.Client()}
	srv.Close() // connection refused

	err := c.SendMessage(context.Background(), 1, "x")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error leaks token: %v", err)
	}
}

func TestSensorKeepsOnlyOwnerMessages(t *testing.T) {
	s := NewSensor(nil, []int64{42}, 1, bus.New[perception.Percept](1), slog.New(slog.NewTextHandler(io.Discard, nil)))

	owner := Update{UpdateID: 1, Message: &Message{MessageID: 9, From: &User{ID: 42, FirstName: "Sam"}, Chat: Chat{ID: 7}, Date: 1700000000, Text: "hi"}}
	p, ok := s.percept(owner)
	if !ok {
		t.Fatal("owner message dropped")
	}
	if p.Source != Source || p.ExternalID != "7:9" || p.Thread != "7" || p.Text != "hi" || p.Kind != "message" ||
		p.Speaker != "telegram:42" || p.SpeakerName != "Sam" {
		t.Fatalf("percept = %+v", p)
	}
	var pl MessagePayload
	if err := json.Unmarshal(p.Payload, &pl); err != nil || pl.ChatID != 7 || pl.UserID != 42 {
		t.Fatalf("payload = %s (%v)", p.Payload, err)
	}

	stranger := Update{UpdateID: 2, Message: &Message{MessageID: 10, From: &User{ID: 99}, Chat: Chat{ID: 8}, Text: "hey"}}
	if _, ok := s.percept(stranger); ok {
		t.Fatal("non-owner message kept")
	}
	if _, ok := s.percept(Update{UpdateID: 3}); ok {
		t.Fatal("update without message kept")
	}
}

func TestSplitLongMessages(t *testing.T) {
	para := strings.Repeat("word ", 300) // 1,500 runes
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
