// Package store пишет журнал всех вызовов LLM в JSONL.
// Один файл на запуск — его удобно и приложить к заданию, и разобрать скриптом.
package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/safronov-a1exander/advent/internal/llm"
)

// Record — одна строка журнала: что отправили, что получили, сколько стоило.
type Record struct {
	TS    time.Time `json:"ts"`
	RunID string    `json:"run_id"`
	// Agent — id агента, сделавшего вызов. Пусто у сценариев и разовых запросов.
	Agent     string        `json:"agent,omitempty"`
	Scenario  string        `json:"scenario,omitempty"`
	Variant   string        `json:"variant,omitempty"`
	Step      int           `json:"step,omitempty"`
	Attempt   int           `json:"attempt,omitempty"`
	Provider  string        `json:"provider"`
	Model     string        `json:"model"`
	Params    Params        `json:"params"`
	Messages  []llm.Message `json:"messages"`
	Content   string        `json:"content"`
	Reasoning string        `json:"reasoning,omitempty"`
	Finish    string        `json:"finish_reason"`
	Usage     llm.Usage     `json:"usage"`
	LatencyMS int64         `json:"latency_ms"`
	CostUSD   float64       `json:"cost_usd"`
	Error     string        `json:"error,omitempty"`
}

// Params — снимок параметров запроса в человекочитаемом виде.
type Params struct {
	Temperature    *float64 `json:"temperature,omitempty"`
	TopP           *float64 `json:"top_p,omitempty"`
	MaxTokens      *int     `json:"max_tokens,omitempty"`
	Stop           []string `json:"stop,omitempty"`
	ResponseFormat string   `json:"response_format,omitempty"`
	Thinking       string   `json:"thinking,omitempty"`
	Seed           *int     `json:"seed,omitempty"`
}

func ParamsOf(r llm.Request) Params {
	p := Params{
		Temperature: r.Temperature,
		TopP:        r.TopP,
		MaxTokens:   r.MaxTokens,
		Stop:        r.Stop,
		Seed:        r.Seed,
	}
	if r.ResponseFormat != nil {
		p.ResponseFormat = r.ResponseFormat.Type
	}
	if r.Thinking != nil {
		p.Thinking = r.Thinking.Type
	}
	return p
}

type Writer struct {
	mu    sync.Mutex
	f     *os.File
	runID string
	path  string
}

// NewWriter создаёт runs/<runID>.jsonl.
func NewWriter(dir, runID string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, runID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f, runID: runID, path: path}, nil
}

func NewRunID(prefix string) string {
	if prefix == "" {
		prefix = "run"
	}
	return fmt.Sprintf("%s-%s", prefix, time.Now().Format("20060102-150405"))
}

func (w *Writer) Path() string  { return w.path }
func (w *Writer) RunID() string { return w.runID }

func (w *Writer) Append(r Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if r.TS.IsZero() {
		r.TS = time.Now()
	}
	r.RunID = w.runID
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := w.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return w.f.Sync()
}

func (w *Writer) Close() error { return w.f.Close() }

// Load читает журнал обратно — для отчётов и сравнений.
func Load(path string) ([]Record, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Record
	dec := json.NewDecoder(bytes.NewReader(b))
	for dec.More() {
		var r Record
		if err := dec.Decode(&r); err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}
