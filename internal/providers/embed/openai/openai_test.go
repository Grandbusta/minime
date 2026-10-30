package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"minime/internal/pricing"
)

func TestEmbed(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" || r.Header.Get("Authorization") != "Bearer sk-x" {
			t.Errorf("request %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		json.NewDecoder(r.Body).Decode(&got)
		// Out of order, as the API may send them.
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"text-embedding-3-small","data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}],
			"usage":{"prompt_tokens":1000000}}`)
	}))
	defer srv.Close()
	c := New("sk-x", "text-embedding-3-small", srv.URL, pricing.Price{Input: 0.02})
	out, err := c.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if got["model"] != "text-embedding-3-small" || len(got["input"].([]any)) != 2 {
		t.Errorf("sent %v", got)
	}
	if out.Vectors[0][0] != 1 || out.Vectors[1][1] != 1 || out.CostUSD != 0.02 || out.Model != "openai/text-embedding-3-small" {
		t.Errorf("embedded = %+v", out)
	}
}

func TestEmbedMissingVector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":3}}`)
	}))
	defer srv.Close()
	if _, err := New("k", "m", srv.URL, pricing.Price{}).Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Error("embedded with a vector missing; want an error")
	}
}
