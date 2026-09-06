package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client — клиент к любому OpenAI-совместимому /chat/completions.
// DeepSeek, OpenRouter, z.ai, локальный vLLM/llama.cpp — один и тот же код,
// меняется только BaseURL и ключ.
type Client struct {
	name    string
	baseURL string
	apiKey  string
	http    *http.Client
	pricing map[string]ModelInfo
}

type ClientOption func(*Client)

func WithHTTPClient(h *http.Client) ClientOption { return func(c *Client) { c.http = h } }

func WithPricing(p map[string]ModelInfo) ClientOption {
	return func(c *Client) { c.pricing = p }
}

func New(name, baseURL, apiKey string, opts ...ClientOption) *Client {
	c := &Client{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 5 * time.Minute},
		pricing: map[string]ModelInfo{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Client) Name() string { return c.name }

// ---- проводные типы ----

type wireReq struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Temperature    *float64        `json:"temperature,omitempty"`
	TopP           *float64        `json:"top_p,omitempty"`
	MaxTokens      *int            `json:"max_tokens,omitempty"`
	Stop           []string        `json:"stop,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	Thinking       *Thinking       `json:"thinking,omitempty"`
	Seed           *int            `json:"seed,omitempty"`
	Stream         bool            `json:"stream,omitempty"`
	StreamOptions  *streamOpts     `json:"stream_options,omitempty"`
}

type streamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireMsg struct {
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	Reasoning        string `json:"reasoning"`
}

type wireChoice struct {
	Index        int     `json:"index"`
	Message      wireMsg `json:"message"`
	Delta        wireMsg `json:"delta"`
	FinishReason string  `json:"finish_reason"`
}

type wireUsage struct {
	PromptTokens            int `json:"prompt_tokens"`
	CompletionTokens        int `json:"completion_tokens"`
	TotalTokens             int `json:"total_tokens"`
	PromptCacheHitTokens    int `json:"prompt_cache_hit_tokens"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type wireResp struct {
	ID      string       `json:"id"`
	Model   string       `json:"model"`
	Choices []wireChoice `json:"choices"`
	Usage   *wireUsage   `json:"usage"`
	Error   *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// APIError несёт HTTP-код и тело — важно для заданий, где мы намеренно
// вылезаем за границы параметров (например temperature > 2).
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("api %d: %s", e.Status, strings.TrimSpace(e.Body))
}

func (c *Client) buildWire(req Request, stream bool) wireReq {
	w := wireReq{
		Model:          req.Model,
		Messages:       req.Messages,
		Temperature:    req.Temperature,
		TopP:           req.TopP,
		MaxTokens:      req.MaxTokens,
		Stop:           req.Stop,
		ResponseFormat: req.ResponseFormat,
		Thinking:       req.Thinking,
		Seed:           req.Seed,
		Stream:         stream,
	}
	if stream {
		w.StreamOptions = &streamOpts{IncludeUsage: true}
	}
	return w
}

func (c *Client) post(ctx context.Context, path string, body any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+c.apiKey)
	return c.http.Do(r)
}

func (c *Client) Chat(ctx context.Context, req Request) (*Response, error) {
	start := time.Now()
	resp, err := c.post(ctx, "/chat/completions", c.buildWire(req, false))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	var wr wireResp
	if err := json.Unmarshal(raw, &wr); err != nil {
		return nil, fmt.Errorf("decode response: %w (body: %.400s)", err, raw)
	}
	if wr.Error != nil {
		return nil, &APIError{Status: resp.StatusCode, Body: wr.Error.Message}
	}
	if len(wr.Choices) == 0 {
		return nil, fmt.Errorf("empty choices (body: %.400s)", raw)
	}
	ch := wr.Choices[0]
	out := &Response{
		Model:        wr.Model,
		Content:      ch.Message.Content,
		Reasoning:    firstNonEmpty(ch.Message.ReasoningContent, ch.Message.Reasoning),
		FinishReason: ch.FinishReason,
		Latency:      time.Since(start),
		Raw:          raw,
	}
	if wr.Usage != nil {
		out.Usage = toUsage(wr.Usage)
	}
	out.CostUSD = c.Cost(out.Model, out.Usage)
	return out, nil
}

func (c *Client) ChatStream(ctx context.Context, req Request, onChunk func(Chunk) error) (*Response, error) {
	start := time.Now()
	resp, err := c.post(ctx, "/chat/completions", c.buildWire(req, true))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}

	var content, reasoning strings.Builder
	out := &Response{Model: req.Model}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var wr wireResp
		if err := json.Unmarshal([]byte(payload), &wr); err != nil {
			continue
		}
		if wr.Model != "" {
			out.Model = wr.Model
		}
		if wr.Usage != nil {
			out.Usage = toUsage(wr.Usage)
		}
		for _, ch := range wr.Choices {
			d := ch.Delta
			r := firstNonEmpty(d.ReasoningContent, d.Reasoning)
			if d.Content != "" || r != "" {
				content.WriteString(d.Content)
				reasoning.WriteString(r)
				if onChunk != nil {
					if err := onChunk(Chunk{Content: d.Content, Reasoning: r}); err != nil {
						return nil, err
					}
				}
			}
			if ch.FinishReason != "" {
				out.FinishReason = ch.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out.Content = content.String()
	out.Reasoning = reasoning.String()
	out.Latency = time.Since(start)
	out.CostUSD = c.Cost(out.Model, out.Usage)
	if onChunk != nil {
		if err := onChunk(Chunk{Done: true}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// Cost считает стоимость по прайсу из конфига. Кэш-хиты тарифицируются дешевле.
func (c *Client) Cost(model string, u Usage) float64 {
	mi, ok := c.pricing[model]
	if !ok {
		return 0
	}
	cached := u.CachedPromptTokens
	fresh := u.PromptTokens - cached
	if fresh < 0 {
		fresh = u.PromptTokens
		cached = 0
	}
	const m = 1_000_000.0
	return float64(fresh)/m*mi.InPer1M +
		float64(cached)/m*mi.CachedIn1M +
		float64(u.CompletionTokens)/m*mi.OutPer1M
}

func toUsage(w *wireUsage) Usage {
	cached := w.PromptCacheHitTokens
	if cached == 0 {
		cached = w.PromptTokensDetails.CachedTokens
	}
	return Usage{
		PromptTokens:       w.PromptTokens,
		CompletionTokens:   w.CompletionTokens,
		ReasoningTokens:    w.CompletionTokensDetails.ReasoningTokens,
		CachedPromptTokens: cached,
		TotalTokens:        w.TotalTokens,
	}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
