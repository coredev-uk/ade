package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/coredev-uk/ade/internal/billing"
	"github.com/coredev-uk/ade/internal/config"
)

// openAI drives any OpenAI-compatible chat completions endpoint: OpenAI
// itself, Moonshot, OpenRouter, and LiteLLM (where the per-request cost
// arrives in the x-litellm-response-cost header).
type openAI struct {
	p    config.AIProfile
	key  string
	base string
	hc   *http.Client
	// noSchema is set once the endpoint refused a response_format: later
	// structured calls go out as plain text without the failed round trip.
	noSchema atomic.Bool
}

func newOpenAI(p config.AIProfile, key string, hc *http.Client) *openAI {
	return &openAI{p: p, key: key, base: strings.TrimRight(p.BaseURL, "/"), hc: hc}
}

func (d *openAI) Name() string { return "openai-compat" }

func (d *openAI) host() string {
	if u, err := url.Parse(d.base); err == nil {
		return u.Host
	}
	return ""
}

// errSchemaRejected means the endpoint refused the response_format of a
// structured call (an old gateway, or a model without JSON-schema support).
var errSchemaRejected = errors.New("response_format not supported")

// Complete sends the call. A call with a Schema asks for response_format
// json_schema; when the gateway or its model rejects that, the call is
// repeated once without it and the answer is parsed from text.
func (d *openAI) Complete(ctx context.Context, c Call) (Response, error) {
	structured := c.Schema != nil && !d.noSchema.Load()
	r, err := d.complete(ctx, c, structured)
	if structured && errors.Is(err, errSchemaRejected) {
		d.noSchema.Store(true)
		return d.complete(ctx, c, false)
	}
	return r, err
}

func (d *openAI) complete(ctx context.Context, c Call, structured bool) (Response, error) {
	body := map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": c.System},
			{"role": "user", "content": c.Prompt},
		},
	}
	if c.MaxTokens > 0 {
		body["max_tokens"] = c.MaxTokens
	}
	if structured {
		body["response_format"] = map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": c.Schema.Name, "schema": c.Schema.JSON},
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.base+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.key)
	resp, err := d.hc.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("openai-compat: %w", redactKey(err, d.key))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Response{}, fmt.Errorf("openai-compat: read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		if stop := classifyStop("gateway", data); stop != nil {
			return Response{}, stop
		}
		if structured && rejectsSchema(resp.StatusCode, data) {
			return Response{}, errSchemaRejected
		}
		return Response{}, fmt.Errorf("openai-compat: HTTP %d: %s", resp.StatusCode, oneLine(string(data)))
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt  int `json:"prompt_tokens"`
			Compl   int `json:"completion_tokens"`
			Details struct {
				Cached int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return Response{}, fmt.Errorf("openai-compat: decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return Response{}, fmt.Errorf("openai-compat: response has no choices")
	}
	r := Response{
		Text:     out.Choices[0].Message.Content,
		Model:    firstNonEmpty(out.Model, c.Model),
		Usage:    Usage{In: out.Usage.Prompt, Out: out.Usage.Compl, Cache: out.Usage.Details.Cached},
		DestHost: d.host(),
		Bytes:    len(raw),

		Structured: structured,
	}
	if g := gatewayInfo(resp.Header); g != nil {
		r.Gateway = g
	}
	if cost, ok := headerFloat(resp.Header, "x-litellm-response-cost"); ok {
		r.CostUSD, r.CostSource = cost, CostGateway
		return r, nil
	}
	if price, ok := d.p.Prices[c.Model]; ok {
		r.CostUSD, r.CostSource = computeCost(price, r.Usage), CostComputed
		return r, nil
	}
	r.CostSource = CostUnknown
	return r, nil
}

// rejectsSchema reports whether an error response says the request's
// response_format is what it refused.
func rejectsSchema(status int, body []byte) bool {
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return false
	}
	t := strings.ToLower(string(body))
	for _, w := range []string{"response_format", "json_schema", "structured output", "structured_output"} {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

func gatewayInfo(h http.Header) *GatewayInfo {
	spend, ok1 := headerFloat(h, "x-litellm-key-spend")
	max, ok2 := headerFloat(h, "x-litellm-key-max-budget") // the literal "None" when uncapped
	if !ok1 && !ok2 {
		return nil
	}
	g := &GatewayInfo{}
	if ok1 {
		g.KeySpend = &spend
	}
	if ok2 {
		g.KeyMaxBudget = &max
	}
	return g
}

func headerFloat(h http.Header, name string) (float64, bool) {
	v := strings.TrimSpace(h.Get(name))
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil
}

// Prices reads LiteLLM's /model_group/info. A plain OpenAI-compatible
// endpoint has no such route; that is "no published prices", not an error.
func (d *openAI) Prices(ctx context.Context) (map[string]config.Price, error) {
	src := billing.NewLiteLLM(d.base, d.key, d.hc)
	prices, err := src.ModelPrices(ctx)
	if err != nil {
		return nil, nil //nolint:nilerr // optional route
	}
	return prices, nil
}

func (d *openAI) Models(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.key)
	return listModels(d.hc, req, "openai-compat", d.key)
}

func listModels(hc *http.Client, req *http.Request, who, key string) ([]string, error) {
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", who, redactKey(err, key))
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s: list models: HTTP %d: %s", who, resp.StatusCode, oneLine(string(data)))
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s: decode models: %w", who, err)
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// redactKey keeps a credential out of a transport error's text.
func redactKey(err error, key string) error {
	if key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), key, "[redacted]"))
}
