// Package llm описывает провайдеро-независимый интерфейс общения с LLM
// и типы запроса/ответа, которых хватает на все задания стенда.
package llm

import (
	"context"
	"encoding/json"
	"time"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// ResponseFormat — контроль структуры ответа на стороне API.
// Type: "text" | "json_object" (у части провайдеров ещё "json_schema").
type ResponseFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"json_schema,omitempty"`
}

// Thinking — переключатель режима рассуждений. У DeepSeek: {"type":"disabled"}
// убирает reasoning-токены из счёта max_tokens.
type Thinking struct {
	Type string `json:"type"`
}

// Request — то, что мы отправляем. Указатели там, где важно отличать
// «не задано» от нулевого значения (temperature=0 — валидное значение).
type Request struct {
	Model          string
	Messages       []Message
	Temperature    *float64
	TopP           *float64
	MaxTokens      *int
	Stop           []string
	ResponseFormat *ResponseFormat
	Thinking       *Thinking
	Seed           *int
	Stream         bool
}

type Usage struct {
	PromptTokens       int `json:"prompt_tokens"`
	CompletionTokens   int `json:"completion_tokens"`
	ReasoningTokens    int `json:"reasoning_tokens"`
	CachedPromptTokens int `json:"cached_prompt_tokens"`
	TotalTokens        int `json:"total_tokens"`
}

type Response struct {
	Model        string          `json:"model"`
	Content      string          `json:"content"`
	Reasoning    string          `json:"reasoning,omitempty"`
	FinishReason string          `json:"finish_reason"`
	Usage        Usage           `json:"usage"`
	Latency      time.Duration   `json:"latency_ns"`
	CostUSD      float64         `json:"cost_usd"`
	Raw          json.RawMessage `json:"-"`
}

// Chunk — единица потоковой выдачи.
type Chunk struct {
	Content   string
	Reasoning string
	Done      bool
}

type ModelInfo struct {
	ID          string  `json:"id" yaml:"id"`
	Label       string  `json:"label" yaml:"label"`
	Tier        string  `json:"tier" yaml:"tier"` // weak | medium | strong
	InPer1M     float64 `json:"in_per_1m" yaml:"in_per_1m"`
	CachedIn1M  float64 `json:"cached_in_per_1m" yaml:"cached_in_per_1m"`
	OutPer1M    float64 `json:"out_per_1m" yaml:"out_per_1m"`
	MaxContext  int     `json:"max_context" yaml:"max_context"`
	Reasoning   bool    `json:"reasoning" yaml:"reasoning"`
	Description string  `json:"description" yaml:"description"`
}

type Provider interface {
	Name() string
	Chat(ctx context.Context, req Request) (*Response, error)
	ChatStream(ctx context.Context, req Request, onChunk func(Chunk) error) (*Response, error)
	ListModels(ctx context.Context) ([]string, error)
}

func F(v float64) *float64 { return &v }
func I(v int) *int         { return &v }
