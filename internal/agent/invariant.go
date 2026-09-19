package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/invariant"
	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/task"
)

// Инварианты агента (день 14).
//
// Всё, что добавлялось раньше, влияло на запрос: профиль, стадия, память
// собирались в системный промпт и надеялись, что модель их учтёт. Инварианты
// — первое, что смотрит на **ответ**. Нарушенный ответ до пользователя
// не доходит; вместо него агент переспрашивает модель с объяснением, что
// именно нарушено, и отдаёт уже исправленный.
//
// Защита в два слоя, и оба обязательны:
//
//  1. правила уходят в системный промпт. Это инструкция, её можно нарушить —
//     но нарушают редко, и это бесплатно;
//  2. ответ проверяется отдельным вызовом. Это и есть та самая «внешняя
//     LLM как механизм сдержек и противовесов», о которой говорил курс.
//
// Повторов по умолчанию один. Курс предупреждал, что проверять всё моделью
// «может быть адски дорого»; бесконечный цикл на упрямом правиле — ровно
// этот случай. Если и повтор нарушает, ответ всё равно уходит пользователю,
// но с явной пометкой: молча отдать нарушающий ответ хуже, чем отдать его
// с предупреждением.

// Режимы инвариантов — поле Config.Invariants.
const (
	// InvariantOff — правил нет.
	InvariantOff = ""
	// InvariantPrompt — правила уходят в промпт, ответ не проверяется.
	// Бесплатно и работает в подавляющем большинстве случаев.
	InvariantPrompt = "prompt"
	// InvariantCheck — плюс проверка ответа отдельным вызовом и повтор
	// при нарушении. Один вызов на ход.
	InvariantCheck = "check"
)

// InvariantModes — порядок перебора в панели.
var InvariantModes = []string{InvariantOff, InvariantPrompt, InvariantCheck}

// InvariantLabel — имя режима для показа.
func InvariantLabel(mode string) string {
	switch mode {
	case InvariantPrompt:
		return "только в промпте"
	case InvariantCheck:
		return "промпт + проверка ответа"
	}
	return "без инвариантов"
}

// InvariantHint — пояснение под панелью.
func InvariantHint(mode string) string {
	switch mode {
	case InvariantPrompt:
		return "правила уходят в системный промпт как запрет; ответ не проверяется — бесплатно, но нарушение пройдёт незамеченным"
	case InvariantCheck:
		return "плюс отдельный вызов, который смотрит ответ и говорит, что нарушено; нарушивший ответ не доходит до пользователя — агент переспрашивает модель"
	}
	return "ограничений нет, модель отвечает как умеет"
}

// Invariants — набор правил агента или nil.
func (a *Agent) Invariants() *invariant.Set {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inv
}

// SetInvariants подключает набор. Зовётся пулом.
func (a *Agent) SetInvariants(s *invariant.Set) {
	a.mu.Lock()
	a.inv = s
	a.mu.Unlock()
}

// invariantBlock — правила для системного промпта.
func invariantBlock(cfg Config, set *invariant.Set, stage string) string {
	if set == nil || cfg.Invariants == InvariantOff {
		return ""
	}
	return invariant.Block(set.List(), stage)
}

// guard проверяет ответ и, если он нарушает правила, переспрашивает модель.
// Возвращает итоговый ответ (возможно, переписанный) и оставшиеся нарушения.
//
// Переспрашивание идёт отдельным запросом с той же историей и добавленным
// объяснением, а не «продолжением» разговора: нарушивший ответ в историю
// не попадает вовсе. Пользователь его не видел — значит, и модель в следующий
// раз видеть не должна, иначе она будет опираться на то, чего не было.
func (a *Agent) guard(ctx context.Context, cfg Config, set *invariant.Set, stage string,
	base []llm.Message, text, answer string, turn *Turn, on func(Event),
) (string, []invariant.Violation) {
	if set == nil || cfg.Invariants != InvariantCheck {
		return answer, nil
	}
	list := set.List()
	if !invariant.Applies(list, stage) {
		return answer, nil
	}
	tries := set.RetriesN()

	for attempt := 0; ; attempt++ {
		vs := a.checkAnswer(ctx, cfg, list, stage, answer, turn, on)
		if len(vs) == 0 {
			if attempt > 0 {
				on(Event{Kind: EventInvariant, Label: "инварианты: ответ исправлен и прошёл проверку"})
			}
			return answer, nil
		}
		if attempt >= tries {
			// Повторы кончились. Ответ уходит, но с пометкой: молча отдать
			// нарушающий ответ хуже, чем отдать его с предупреждением.
			on(Event{Kind: EventInvariant, Label: "инварианты нарушены и после повтора — ответ уходит как есть",
				Content: violationsText(vs)})
			return answer, vs
		}

		on(Event{Kind: EventInvariant,
			Label:   fmt.Sprintf("инварианты нарушены (%d) — переспрашиваю", len(vs)),
			Content: violationsText(vs)})

		req := llm.Request{Messages: append(append([]llm.Message(nil), base...),
			llm.Message{Role: llm.RoleUser, Content: text},
			llm.Message{Role: llm.RoleAssistant, Content: answer},
			llm.Message{Role: llm.RoleUser, Content: invariant.Explain(vs)})}
		cfg.Apply(&req)
		start := time.Now()
		resp, err := a.client.Chat(ctx, req)
		a.record(req, resp, err, "повтор по инвариантам", true)
		a.accountAux(resp, err)
		if err != nil {
			on(Event{Kind: EventInvariant, Label: "повтор не удался — ответ уходит как есть", Content: err.Error()})
			return answer, vs
		}
		turn.AuxCalls++
		turn.AuxPrompt += resp.Usage.PromptTokens
		turn.AuxCompletion += resp.Usage.CompletionTokens
		answer = resp.Content
		on(Event{Kind: EventInvariant, Label: "ответ переписан",
			Usage: resp.Usage, CostUSD: resp.CostUSD, Latency: time.Since(start)})
	}
}

// checkAnswer — один вызов проверки: все правила и ответ, обратно список
// нарушенных.
//
// Сбой не считается нарушением: иначе упавший запрос или не-JSON в ответе
// заставляли бы переписывать нормальные ответы, и один сбой сети превращался
// бы в два лишних вызова и испорченный ответ.
func (a *Agent) checkAnswer(ctx context.Context, cfg Config, list []invariant.Invariant, stage, answer string,
	turn *Turn, on func(Event),
) []invariant.Violation {
	req := llm.Request{
		Model: cfg.Model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: invariant.CheckSystem},
			{Role: llm.RoleUser, Content: invariant.CheckPrompt(list, stage, answer)},
		},
		Temperature:    llm.F(0),
		Thinking:       &llm.Thinking{Type: "disabled"},
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}
	resp, err := a.client.Chat(ctx, req)
	a.record(req, resp, err, "проверка инвариантов", true)
	a.accountAux(resp, err)
	if err != nil {
		on(Event{Kind: EventInvariant, Label: "проверка не удалась — считаем, что нарушений нет", Content: err.Error()})
		return nil
	}
	turn.AuxCalls++
	turn.AuxPrompt += resp.Usage.PromptTokens
	turn.AuxCompletion += resp.Usage.CompletionTokens

	vs, err := invariant.ParseCheck(resp.Content, list, stage)
	if err != nil {
		on(Event{Kind: EventInvariant, Label: "проверка ответила не JSON — считаем, что нарушений нет", Content: err.Error()})
		return nil
	}
	return vs
}

func violationsText(vs []invariant.Violation) string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.String())
	}
	return strings.Join(out, "\n")
}

// stageOf — стадия задачи или пусто. Инварианты умеют действовать только
// на некоторых стадиях, и им нужно знать, где мы.
func stageOf(t *task.Task) string {
	if t == nil {
		return ""
	}
	return string(t.State)
}
