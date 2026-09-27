package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/mcp"
)

// scriptLLM отвечает по очереди заготовленными ответами и запоминает запросы.
type scriptLLM struct {
	mu    sync.Mutex
	reqs  []llm.Request
	resps []*llm.Response
}

func (s *scriptLLM) Name() string                                 { return "script" }
func (s *scriptLLM) ListModels(context.Context) ([]string, error) { return nil, nil }
func (s *scriptLLM) Chat(_ context.Context, req llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	if len(s.resps) == 0 {
		return &llm.Response{Content: "итог", Usage: llm.Usage{PromptTokens: 10}}, nil
	}
	r := s.resps[0]
	s.resps = s.resps[1:]
	return r, nil
}
func (s *scriptLLM) ChatStream(ctx context.Context, req llm.Request, _ func(llm.Chunk) error) (*llm.Response, error) {
	return s.Chat(ctx, req)
}

func wants(id, fn, args string) *llm.Response {
	tc := llm.ToolCall{ID: id, Type: "function"}
	tc.Function.Name, tc.Function.Arguments = fn, args
	return &llm.Response{ToolCalls: []llm.ToolCall{tc}, FinishReason: "tool_calls", Usage: llm.Usage{PromptTokens: 100}}
}

type fakeBox struct {
	calls   []string
	allowed [][]string
}

func (f *fakeBox) Functions(_ context.Context, servers []string) ([]llm.Tool, error) {
	var out []llm.Tool
	for _, s := range servers {
		out = append(out, llm.Tool{Type: "function", Function: llm.ToolFunction{Name: s + "__convert"}})
	}
	return out, nil
}

func (f *fakeBox) Call(_ context.Context, fn, args string, allowed []string) mcp.Outcome {
	f.calls = append(f.calls, fn+" "+args)
	f.allowed = append(f.allowed, allowed)
	if fn != "rates__convert" {
		return mcp.Outcome{Func: fn, IsError: true, Text: "нет такой функции"}
	}
	return mcp.Outcome{Func: fn, Server: "rates", Tool: "convert", Args: args, Text: "10 USD = 840 RUB"}
}

func TestToolLoopCallsAndFeedsResultBack(t *testing.T) {
	m := &scriptLLM{resps: []*llm.Response{
		wants("c1", "rates__convert", `{"amount":10,"from":"USD"}`),
		{Content: "Это 840 рублей.", Usage: llm.Usage{PromptTokens: 130, CompletionTokens: 5}},
	}}
	box := &fakeBox{}
	pool := NewPool(m, "t", nil)
	pool.SetToolbox(box)
	a := pool.Spawn(Config{Name: "b", Model: "m", MCP: []string{"rates"}})

	var kinds []EventKind
	reply, err := a.Ask(context.Background(), "сколько 10 долларов?", func(e Event) { kinds = append(kinds, e.Kind) })
	if err != nil {
		t.Fatal(err)
	}
	if reply.Final.Content != "Это 840 рублей." {
		t.Fatalf("ответ: %q", reply.Final.Content)
	}
	if len(box.calls) != 1 || box.allowed[0][0] != "rates" {
		t.Fatalf("вызовы: %v %v", box.calls, box.allowed)
	}
	// Второй запрос несёт вызов модели и результат с тем же id.
	second := m.reqs[1].Messages
	tool := second[len(second)-1]
	if tool.Role != llm.RoleTool || tool.ToolCallID != "c1" || !strings.Contains(tool.Content, "840") {
		t.Fatalf("результат не вернулся модели: %+v", tool)
	}
	if len(m.reqs[0].Tools) != 1 || len(m.reqs[1].Tools) != 1 {
		t.Fatal("схема должна уходить в каждом круге")
	}
	turn := a.Turns()[0]
	if turn.Calls != 2 || turn.ToolCalls != 1 || turn.ToolRounds != 1 || turn.Prompt != 230 {
		t.Fatalf("учёт хода: %+v", turn)
	}
	// В историю — вопрос и ответ, без сырых вызовов.
	if h := a.History(); len(h) != 2 || len(h[1].ToolCalls) != 0 {
		t.Fatalf("история: %+v", h)
	}
	if ToolTrace(reply.Tools) != "rates.convert" {
		t.Fatalf("трасса: %q", ToolTrace(reply.Tools))
	}
	var sawCall, sawResult bool
	for _, k := range kinds {
		sawCall = sawCall || k == EventToolCall
		sawResult = sawResult || k == EventToolResult
	}
	if !sawCall || !sawResult {
		t.Fatalf("события: %v", kinds)
	}
}

// Модель придумала функцию — ход не ломается: ошибка уходит ей же.
func TestToolErrorGoesBackToModel(t *testing.T) {
	m := &scriptLLM{resps: []*llm.Response{
		wants("c1", "bank__transfer", `{}`),
		{Content: "Не могу перевести."},
	}}
	pool := NewPool(m, "t", nil)
	pool.SetToolbox(&fakeBox{})
	a := pool.Spawn(Config{Name: "b", Model: "m", MCP: []string{"rates"}})
	reply, err := a.Ask(context.Background(), "переведи", nil)
	if err != nil {
		t.Fatal(err)
	}
	last := m.reqs[1].Messages[len(m.reqs[1].Messages)-1]
	if !strings.HasPrefix(last.Content, "ОШИБКА") || !reply.Tools[0].IsError {
		t.Fatalf("ошибка не дошла до модели: %+v", last)
	}
}

// Зациклившаяся модель: на последнем круге функции забираются.
func TestToolRoundsAreLimited(t *testing.T) {
	var resps []*llm.Response
	for i := 0; i < MaxToolRounds+5; i++ {
		resps = append(resps, wants("c", "rates__convert", `{}`))
	}
	m := &scriptLLM{resps: resps}
	pool := NewPool(m, "t", nil)
	pool.SetToolbox(&fakeBox{})
	a := pool.Spawn(Config{Name: "b", Model: "m", MCP: []string{"rates"}})
	if _, err := a.Ask(context.Background(), "?", nil); err != nil {
		t.Fatal(err)
	}
	if len(m.reqs) != MaxToolRounds+1 {
		t.Fatalf("запросов: %d, ждали %d", len(m.reqs), MaxToolRounds+1)
	}
	if last := m.reqs[len(m.reqs)-1]; len(last.Tools) != 0 {
		t.Fatal("на последнем круге функций быть не должно")
	}
}

func TestNoMCPNoTools(t *testing.T) {
	m := &scriptLLM{}
	pool := NewPool(m, "t", nil)
	pool.SetToolbox(&fakeBox{})
	a := pool.Spawn(Config{Name: "b", Model: "m"})
	a.Ask(context.Background(), "?", nil)
	if len(m.reqs[0].Tools) != 0 {
		t.Fatal("агенту без серверов функции не положены")
	}
}
