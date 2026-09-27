package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/mcp"
)

// Инструменты MCP (день 17).
//
// Агент сам не знает ни курсов, ни выписки — знает, куда за ними сходить.
// Цикл хода с инструментами такой:
//
//  1. запрос уходит модели вместе со схемой функций выданных серверов;
//  2. модель вместо ответа просит вызвать функции — одну или несколько;
//  3. агент вызывает их через MCP и возвращает результаты сообщениями tool;
//  4. модель спрашивают снова — со всей историей хода и той же схемой.
//
// Повторяется, пока модель не ответит текстом. Каждый круг — отдельный
// вызов API, и схема в нём оплачивается заново: то самое «98% на
// обслуживание» из лекции недели.
//
// В историю разговора попадает вопрос и итоговый ответ, а не сырые
// результаты инструментов. Вызовы и результаты видны в ленте, в журнале
// и в отчёте, но следующий ход не тащит их за собой: иначе один запрос
// выписки раздувал бы каждый последующий вопрос.

// Toolbox — откуда агент берёт инструменты. *mcp.Hub подходит как есть.
type Toolbox interface {
	Functions(ctx context.Context, servers []string) ([]llm.Tool, error)
	Call(ctx context.Context, fn, args string, allowed []string) mcp.Outcome
}

// MaxToolRounds — сколько кругов вызовов даётся на один ход. Модель,
// которая зациклилась на инструментах, иначе тратила бы деньги бесконечно;
// на последнем круге функции у неё забираются, и она обязана ответить.
const MaxToolRounds = 8

// SetToolbox подключает MCP-серверы. Какие из них выданы агенту, решает
// его конфиг.
func (p *Pool) SetToolbox(tb Toolbox) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tools = tb
	for _, a := range p.agents {
		a.mu.Lock()
		a.tools = tb
		a.mu.Unlock()
	}
}

// functions — функции для модели на этот ход. Недоступный сервер не ломает
// ход: агент говорит, что инструментов нет, и отвечает без них.
func (a *Agent) functions(ctx context.Context, cfg Config, on func(Event)) []llm.Tool {
	a.mu.Lock()
	tb := a.tools
	a.mu.Unlock()
	if tb == nil || len(cfg.MCP) == 0 {
		return nil
	}
	funcs, err := tb.Functions(ctx, cfg.MCP)
	if err != nil {
		on(Event{Kind: EventToolResult, Label: "MCP недоступен", Content: err.Error(), Failed: true})
		return nil
	}
	return funcs
}

// toolLoop — финальный шаг хода с инструментами. Сам пишет каждый круг
// в журнал и в учёт хода.
func (a *Agent) toolLoop(ctx context.Context, cfg Config, req llm.Request, funcs []llm.Tool,
	st ChainStep, chained bool, turn *Turn, reply *Reply, on func(Event)) (*llm.Response, error) {
	a.mu.Lock()
	tb := a.tools
	a.mu.Unlock()

	msgs := append([]llm.Message(nil), req.Messages...)
	// Промежуточный круг не должен уходить в ленту кусками ответа:
	// текст, если он есть рядом с вызовами, — это «сейчас посмотрю»,
	// а не ответ.
	quiet := st
	quiet.Final = false

	for round := 0; ; round++ {
		req.Messages = msgs
		req.Tools = funcs
		last := round >= MaxToolRounds
		if last {
			req.Tools = nil
			req.Messages = append(msgs, llm.Message{Role: llm.RoleUser,
				Content: "Лимит вызовов инструментов на этот вопрос исчерпан. Ответь по тому, что уже получено."})
		}
		resp, err := a.call(ctx, req, quiet, false, on)
		a.record(req, resp, err, st.Label, chained || round > 0)
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		a.learn(rawEstimateMessages(req.Messages)+rawEstimateTools(req.Tools), resp.Usage.PromptTokens)
		a.mu.Unlock()
		turn.Calls++
		turn.Prompt += resp.Usage.PromptTokens
		turn.Completion += resp.Usage.CompletionTokens
		turn.Reasoning += resp.Usage.ReasoningTokens
		turn.Cached += resp.Usage.CachedPromptTokens
		if round > 0 {
			turn.ToolRounds++
		}

		if len(resp.ToolCalls) == 0 || last {
			on(Event{Kind: EventChunk, Content: resp.Content})
			return resp, nil
		}
		if s := strings.TrimSpace(resp.Content); s != "" {
			on(Event{Kind: EventStep, Label: "по ходу", Content: s,
				Usage: resp.Usage, CostUSD: resp.CostUSD, Latency: resp.Latency})
		}

		asst := llm.Message{Role: llm.RoleAssistant, Content: resp.Content, ToolCalls: resp.ToolCalls}
		if cfg.Thinking != "disabled" {
			asst.ReasoningContent = resp.Reasoning
		}
		msgs = append(msgs, asst)
		for _, tc := range resp.ToolCalls {
			label := displayName(tc.Function.Name)
			on(Event{Kind: EventToolCall, Label: label, Content: tc.Function.Arguments})
			out := tb.Call(ctx, tc.Function.Name, tc.Function.Arguments, cfg.MCP)
			turn.ToolCalls++
			reply.Tools = append(reply.Tools, out)
			on(Event{Kind: EventToolResult, Label: label, Content: out.Text, Latency: out.Latency, Failed: out.IsError})
			content := out.Text
			if out.IsError {
				content = "ОШИБКА: " + content
			}
			msgs = append(msgs, llm.Message{Role: llm.RoleTool, ToolCallID: tc.ID, Content: content})
		}
	}
}

// displayName — «rates__convert» → «rates.convert»: так читать проще,
// а модели нужна форма с подчёркиванием.
func displayName(fn string) string {
	if s, t, ok := strings.Cut(fn, mcp.Sep); ok {
		return s + "." + t
	}
	return fn
}

// ToolTrace — вызовы хода одной строкой для отчётов: «rates.convert → rates.exchange_rate».
func ToolTrace(out []mcp.Outcome) string {
	var parts []string
	for _, o := range out {
		n := o.Server + "." + o.Tool
		if o.Server == "" {
			n = o.Func
		}
		if o.IsError {
			n += " ✗"
		}
		parts = append(parts, n)
	}
	return strings.Join(parts, " → ")
}

// mcpSummary — подпись для Config.Summary.
func mcpSummary(servers []string) string {
	return fmt.Sprintf("mcp: %s", strings.Join(servers, ", "))
}
