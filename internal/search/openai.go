package search

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"

	"github.com/Grandbusta/minime/internal/pricing"
	"github.com/Grandbusta/minime/internal/providers/openaiclient"
)

type OpenAI struct {
	api   openai.Client
	model string
	price pricing.Price
}

func NewOpenAI(apiKey, model, baseURL string, price pricing.Price) *OpenAI {
	return &OpenAI{api: openaiclient.New(apiKey, baseURL, 90*time.Second), model: model, price: price}
}

const searchInstructions = `You find the most useful pages on the web for someone learning, using web search.
List up to %d pages, one per line, exactly as: title | url | one sentence on why it's worth reading.
Prefer pages that teach or show real examples (guides, articles, documentation, well-known works) over shops, forums and listicles. Only list pages you found with web search. No other text.`

// Search asks the model to search the web and list the best pages. Only
// pages its search actually retrieved (the search's sources, or the answer's
// citations) are kept, so no URL is made up.
func (o *OpenAI) Search(ctx context.Context, query string, n int) (Found, error) {
	w, err := o.api.Responses.New(ctx, responses.ResponseNewParams{
		Model:        o.model,
		Instructions: param.NewOpt(fmt.Sprintf(searchInstructions, n)),
		Input:        responses.ResponseNewParamsInputUnion{OfString: param.NewOpt("Search the web for: " + query)},
		Tools:        []responses.ToolUnionParam{{OfWebSearch: &responses.WebSearchToolParam{Type: responses.WebSearchToolTypeWebSearch}}},
		Include:      []responses.ResponseIncludable{responses.ResponseIncludableWebSearchCallActionSources},
		Store:        param.NewOpt(false),
	})
	if err != nil {
		return Found{}, fmt.Errorf("openai web search: %w", err)
	}
	cost := o.price.Cost(pricing.Usage{InputTokens: int(w.Usage.InputTokens), CachedTokens: int(w.Usage.InputTokensDetails.CachedTokens),
		OutputTokens: int(w.Usage.OutputTokens)})

	cited := map[string]string{} // without tracking parameters → as found
	var text strings.Builder
	for _, item := range w.Output {
		if item.Type == "web_search_call" {
			for _, src := range item.Action.Sources {
				if strings.HasPrefix(src.URL, "https://") {
					cited[plain(src.URL)] = src.URL
				}
			}
			continue
		}
		if item.Type != "message" {
			continue
		}
		for _, c := range item.Content {
			text.WriteString(c.Text + "\n")
			for _, a := range c.Annotations {
				if a.Type == "url_citation" && strings.HasPrefix(a.URL, "https://") {
					cited[plain(a.URL)] = a.URL
				}
			}
		}
	}
	var results []Result
	seen := map[string]bool{}
	for _, line := range strings.Split(text.String(), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) < 2 || len(results) >= n {
			continue
		}
		title := strings.TrimLeft(strings.TrimSpace(parts[0]), "-*0123456789. ")
		link := strings.Trim(strings.TrimSpace(parts[1]), "<>()[]")
		got, ok := cited[plain(link)]
		if !ok || seen[got] {
			continue // not from its search: never trust a URL a model wrote by itself
		}
		seen[got] = true
		why := ""
		if len(parts) > 2 {
			why = strings.TrimSpace(strings.Join(parts[2:], "|"))
		}
		results = append(results, Result{Title: title, URL: got, Snippet: why})
	}
	if len(results) == 0 {
		for _, u := range cited { // it cited pages but didn't list them as asked
			if len(results) >= n {
				break
			}
			results = append(results, Result{Title: u, URL: u})
		}
	}
	if len(results) == 0 {
		return Found{CostUSD: cost}, fmt.Errorf("search %q: %w", query, ErrNothing)
	}
	return Found{Results: results, CostUSD: cost}, nil
}

// plain is a URL without its tracking parameters and trailing slash, for matching.
func plain(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(k, "utm_") {
			q.Del(k)
		}
	}
	u.RawQuery, u.Fragment = q.Encode(), ""
	return strings.TrimRight(u.String(), "/")
}
