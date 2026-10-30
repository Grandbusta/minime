// Package openai is the embedding provider for OpenAI's Embeddings API (POST /v1/embeddings).
package openai

import (
	"context"
	"fmt"
	"time"

	"minime/internal/pricing"
	"minime/internal/providers/embed"
	"minime/internal/providers/openaihttp"
)

const requestTimeout = 30 * time.Second

type Client struct {
	api   *openaihttp.Client
	model string
	price pricing.Price // zero: cost is recorded as 0
}

func New(apiKey, model, baseURL string, price pricing.Price) *Client {
	return &Client{api: openaihttp.New(apiKey, baseURL, requestTimeout), model: model, price: price}
}

func (c *Client) Embed(ctx context.Context, texts []string) (embed.Embedded, error) {
	if len(texts) == 0 {
		return embed.Embedded{}, nil
	}
	var resp struct {
		Model string `json:"model"`
		Data  []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := c.api.Post(ctx, "/embeddings", map[string]any{"model": c.model, "input": texts}, &resp); err != nil {
		return embed.Embedded{}, err
	}
	out := embed.Embedded{Vectors: make([][]float32, len(texts)), Model: "openai/" + c.model,
		CostUSD: c.price.Cost(pricing.Usage{InputTokens: resp.Usage.PromptTokens})}
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return embed.Embedded{}, fmt.Errorf("openai /embeddings: index %d out of range", d.Index)
		}
		out.Vectors[d.Index] = d.Embedding
	}
	for i, v := range out.Vectors {
		if len(v) == 0 {
			return embed.Embedded{}, fmt.Errorf("openai /embeddings: no vector for text %d", i)
		}
	}
	return out, nil
}
