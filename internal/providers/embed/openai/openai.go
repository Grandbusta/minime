// Package openai is the embedding provider for OpenAI's Embeddings API.
package openai

import (
	"context"
	"fmt"
	"time"

	"github.com/openai/openai-go/v3"

	"minime/internal/pricing"
	"minime/internal/providers/embed"
	"minime/internal/providers/openaiclient"
)

const requestTimeout = 30 * time.Second

type Client struct {
	api   openai.Client
	model string
	price pricing.Price // zero: cost is recorded as 0
}

func New(apiKey, model, baseURL string, price pricing.Price) *Client {
	return &Client{api: openaiclient.New(apiKey, baseURL, requestTimeout), model: model, price: price}
}

func (c *Client) Embed(ctx context.Context, texts []string) (embed.Embedded, error) {
	if len(texts) == 0 {
		return embed.Embedded{}, nil
	}
	resp, err := c.api.Embeddings.New(ctx, openai.EmbeddingNewParams{Model: c.model,
		Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts}})
	if err != nil {
		return embed.Embedded{}, fmt.Errorf("openai embeddings: %w", err)
	}
	out := embed.Embedded{Vectors: make([][]float32, len(texts)), Model: "openai/" + c.model,
		CostUSD: c.price.Cost(pricing.Usage{InputTokens: int(resp.Usage.PromptTokens)})}
	for _, d := range resp.Data {
		if d.Index < 0 || int(d.Index) >= len(texts) {
			return embed.Embedded{}, fmt.Errorf("openai embeddings: index %d out of range", d.Index)
		}
		v := make([]float32, len(d.Embedding))
		for i, x := range d.Embedding {
			v[i] = float32(x)
		}
		out.Vectors[d.Index] = v
	}
	for i, v := range out.Vectors {
		if len(v) == 0 {
			return embed.Embedded{}, fmt.Errorf("openai embeddings: no vector for text %d", i)
		}
	}
	return out, nil
}
