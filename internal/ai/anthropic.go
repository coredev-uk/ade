package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
)

const anthropicVersion = "2023-06-01"

// anthropic drives the Anthropic Messages API. The API reports tokens, not
// cost, so cost is tokens × the profile's price table (computed).
type anthropic struct {
	p    config.AIProfile
	key  string
	base string
	hc   *http.Client
}

func newAnthropic(p config.AIProfile, key string, hc *http.Client) *anthropic {
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	return &anthropic{p: p, key: key, base: base, hc: hc}
}

func (d *anthropic) Name() string { return "anthropic" }

func (d *anthropic) headers(req *http.Request) {
	req.Header.Set("x-api-key", d.key)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Content-Type", "application/json")
}

func (d *anthropic) Complete(ctx context.Context, c Call) (Response, error) {
	max := c.MaxTokens
	if max <= 0 {
		max = 1024
	}
	raw, err := json.Marshal(map[string]any{
		"model":      c.Model,
		"max_tokens": max,
		"system":     c.System,
		"messages":   []map[string]string{{"role": "user", "content": c.Prompt}},
	})
	if err != nil {
		return Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.base+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	d.headers(req)
	resp, err := d.hc.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("anthropic: %w", redactKey(err, d.key))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Response{}, fmt.Errorf("anthropic: read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		if stop := classifyStop("provider", data); stop != nil {
			return Response{}, stop
		}
		return Response{}, fmt.Errorf("anthropic: HTTP %d: %s", resp.StatusCode, oneLine(string(data)))
	}
	var out struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			In         int `json:"input_tokens"`
			Out        int `json:"output_tokens"`
			CacheRead  int `json:"cache_read_input_tokens"`
			CacheWrite int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return Response{}, fmt.Errorf("anthropic: decode response: %w", err)
	}
	var text strings.Builder
	for _, b := range out.Content {
		if b.Type == "text" {
			text.WriteString(b.Text)
		}
	}
	r := Response{
		Text:     text.String(),
		Model:    firstNonEmpty(out.Model, c.Model),
		Usage:    Usage{In: out.Usage.In + out.Usage.CacheWrite, Out: out.Usage.Out, Cache: out.Usage.CacheRead},
		DestHost: hostOf(d.base),
		Bytes:    len(raw),
	}
	if price, ok := d.p.Prices[c.Model]; ok {
		r.CostUSD = (float64(out.Usage.In)*price.Input + float64(out.Usage.Out)*price.Output +
			float64(out.Usage.CacheRead)*price.CacheRead + float64(out.Usage.CacheWrite)*price.CacheWrite) / 1e6
		r.CostSource = CostComputed
	} else {
		r.CostSource = CostUnknown
	}
	return r, nil
}

// Prices: the Messages API publishes none; the profile's table is the source.
func (d *anthropic) Prices(context.Context) (map[string]config.Price, error) { return nil, nil }

func (d *anthropic) Models(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base+"/v1/models?limit=100", nil)
	if err != nil {
		return nil, err
	}
	d.headers(req)
	return listModels(d.hc, req, "anthropic", d.key)
}

func hostOf(base string) string {
	if u, err := url.Parse(base); err == nil {
		return u.Host
	}
	return ""
}

// computeCost prices usage. Input excludes cache reads, which have their own
// rate; a model with no cache price bills them at the input rate.
func computeCost(p config.Price, u Usage) float64 {
	cache := p.CacheRead
	if cache == 0 {
		cache = p.Input
	}
	in := u.In
	if in >= u.Cache { // OpenAI-style usage counts cached tokens inside prompt_tokens
		in -= u.Cache
	}
	return (float64(in)*p.Input + float64(u.Out)*p.Output + float64(u.Cache)*cache) / 1e6
}
