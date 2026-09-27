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
	// RoleTool — результат инструмента, который клиент вернул модели (день 17).
	RoleTool Role = "tool"
)

type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
	// ToolCalls — модель просит вызвать инструменты вместо ответа (день 17).
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID — на какой вызов отвечает сообщение с ролью tool.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// ReasoningContent — рассуждение, с которым модель попросила инструмент.
	// DeepSeek в режиме рассуждений требует вернуть его внутри того же хода,
	// иначе отвечает 400; в остальных случаях поле пустое и не уходит.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// Tool — описание функции, которую модель может попросить вызвать.
// Для MCP-инструмента это его имя, описание и схема аргументов как есть.
type Tool struct {
	Type     string       `json:"type"` // всегда "function"
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolCall — просьба модели вызвать функцию. Arguments — JSON-объект
// строкой: модель пишет его сама, и он бывает битым.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
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
	// Tools — функции, доступные модели в этом запросе (день 17). Их схема
	// уходит в каждый запрос и оплачивается как обычный вход.
	Tools []Tool
}

type Usage struct {
	PromptTokens       int `json:"prompt_tokens"`
	CompletionTokens   int `json:"completion_tokens"`
	ReasoningTokens    int `json:"reasoning_tokens"`
	CachedPromptTokens int `json:"cached_prompt_tokens"`
	TotalTokens        int `json:"total_tokens"`
}

type Response struct {
	Model        string `json:"model"`
	Content      string `json:"content"`
	Reasoning    string `json:"reasoning,omitempty"`
	FinishReason string `json:"finish_reason"`
	// ToolCalls — модель не ответила, а попросила вызвать инструменты.
	ToolCalls []ToolCall      `json:"tool_calls,omitempty"`
	Usage     Usage           `json:"usage"`
	Latency   time.Duration   `json:"latency_ns"`
	CostUSD   float64         `json:"cost_usd"`
	Raw       json.RawMessage `json:"-"`
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
	// ParamsB — размер модели в миллиардах параметров, если известен.
	// Вместе с квантизацией и размером контекста это и есть «класс» модели.
	ParamsB float64 `json:"params_b" yaml:"params_b"`
	Quant   string  `json:"quant" yaml:"quant"`
	URL     string  `json:"url" yaml:"url"` // страница модели — для ссылок в отчёте
}

type Provider interface {
	Name() string
	Chat(ctx context.Context, req Request) (*Response, error)
	ChatStream(ctx context.Context, req Request, onChunk func(Chunk) error) (*Response, error)
	ListModels(ctx context.Context) ([]string, error)
}

func F(v float64) *float64 { return &v }
func I(v int) *int         { return &v }
