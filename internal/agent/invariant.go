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
// Порядок проверки задан ценой:
//
//  1. быстрые фильтры (подстроки) — всегда, они бесплатны и ловят
//     очевидное сразу;
//  2. смысловая проверка отдельной моделью — если фильтры ничего не нашли.
//     Она смотрит ВСЕ правила, включая те, у которых фильтр есть: пустой
//     фильтр не значит «нарушения нет», он значит «очевидного не видно».
//     Тратить вызов на ответ, уже забракованный фильтром, незачем.
//
// Повторов по умолчанию один. Курс предупреждал, что проверять всё моделью
// «может быть адски дорого и нивелировать весь эффект от ИИ»; бесконечный
// цикл на упрямом правиле — ровно этот случай. Если и повтор нарушает,
// ответ всё равно уходит пользователю, но с явной пометкой: молча отдать
// нарушающий ответ хуже, чем отдать его с предупреждением.

// Режимы инвариантов — поле Config.Invariants.
const (
	// InvariantOff — правил нет.
	InvariantOff = ""
	// InvariantPrompt — правила уходят в промпт, но ответ не проверяется.
	// Это то, что большинство и делает: дёшево и работает в 99% случаев.
	InvariantPrompt = "prompt"
	// InvariantCheck — правила в промпте плюс быстрые фильтры и повтор
	// при нарушении. Бесплатно, но ловит только очевидное.
	InvariantCheck = "check"
	// InvariantJudge — то же плюс смысловая проверка каждого правила
	// отдельной моделью. Единственный режим, который проверяет правила
	// без фильтров, — и единственный, который стоит вызова на каждый ход.
	InvariantJudge = "judge"
)

// InvariantModes — порядок перебора в панели.
var InvariantModes = []string{InvariantOff, InvariantPrompt, InvariantCheck, InvariantJudge}

// InvariantLabel — имя режима для показа.
func InvariantLabel(mode string) string {
	switch mode {
	case InvariantPrompt:
		return "только в промпте"
	case InvariantCheck:
		return "промпт + быстрые фильтры"
	case InvariantJudge:
		return "промпт + фильтры + смысловая проверка"
	}
	return "без инвариантов"
}

// InvariantHint — пояснение под панелью.
func InvariantHint(mode string) string {
	switch mode {
	case InvariantPrompt:
		return "правила уходят в системный промпт как запрет; ответ не проверяется — дёшево, но нарушение пройдёт незамеченным"
	case InvariantCheck:
		return "плюс быстрые фильтры по подстрокам: ловят очевидное бесплатно, но правила без фильтра не проверяются вовсе"
	case InvariantJudge:
		return "плюс смысловая проверка каждого правила отдельной моделью — вызов на каждый ход, зато проверяется то, что подстрокой не выразишь"
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
	if set == nil || cfg.Invariants == InvariantOff || cfg.Invariants == InvariantPrompt {
		return answer, nil
	}
	list := set.List()
	tries := set.RetriesN()

	for attempt := 0; ; attempt++ {
		vs := invariant.Filter(answer, list, stage)
		// Смысловая проверка — только если фильтры ничего не нашли: тратить
		// вызов на уже забракованный ответ незачем.
		if len(vs) == 0 && cfg.Invariants == InvariantJudge && invariant.NeedsJudge(list, stage) {
			vs = a.judge(ctx, cfg, list, stage, answer, turn, on)
		}
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

// judge — смысловая проверка правил отдельной моделью.
// Сбой не считается нарушением: иначе упавший запрос заставлял бы
// переписывать нормальные ответы.
func (a *Agent) judge(ctx context.Context, cfg Config, list []invariant.Invariant, stage, answer string,
	turn *Turn, on func(Event),
) []invariant.Violation {
	req := llm.Request{
		Model: cfg.Model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: invariant.JudgeSystem},
			{Role: llm.RoleUser, Content: invariant.JudgePrompt(list, stage, answer)},
		},
		Temperature:    llm.F(0),
		Thinking:       &llm.Thinking{Type: "disabled"},
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}
	resp, err := a.client.Chat(ctx, req)
	a.record(req, resp, err, "проверка инвариантов", true)
	a.accountAux(resp, err)
	if err != nil {
		on(Event{Kind: EventInvariant, Label: "смысловая проверка не удалась — считаем, что нарушений нет", Content: err.Error()})
		return nil
	}
	turn.AuxCalls++
	turn.AuxPrompt += resp.Usage.PromptTokens
	turn.AuxCompletion += resp.Usage.CompletionTokens

	vs, err := invariant.ParseJudge(resp.Content, list, stage)
	if err != nil {
		on(Event{Kind: EventInvariant, Label: "смысловая проверка ответила не JSON — считаем, что нарушений нет", Content: err.Error()})
		return nil
	}
	return vs
}

func violationsText(vs []invariant.Violation) string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		s := v.String()
		if v.ByJudge {
			s += " [смысловая проверка]"
		}
		out = append(out, s)
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
