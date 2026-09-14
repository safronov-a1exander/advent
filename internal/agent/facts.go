package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/llm"
)

// Sticky Facts (день 10).
//
// Вместо сводки всего разговора агент ведёт короткий блок фактов
// «ключ: значение» — цель, ограничения, предпочтения, решения, договорённости.
// После каждого сообщения пользователя отдельный вызов обновляет блок, а в
// запрос уходят факты и последние N сообщений.
//
// Отличие от summary принципиальное: сводка пересказывает разговор и с каждым
// пересказом копит искажения (на девятом дне она сама пересчитала остаток
// и ошиблась). Факты не пересказываются — каждый ключ либо остаётся как был,
// либо заменяется новым значением из реплики пользователя.

// Fact — один факт разговора.
type Fact struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// factsSystem — инструкция для обновления фактов.
const factsSystem = `Ты ведёшь блок ключевых фактов разговора пользователя с ассистентом.
Тебе дают текущие факты, последний ответ ассистента и новое сообщение пользователя.
Верни JSON-объект {"facts": {"ключ": "значение", ...}} — ПОЛНЫЙ обновлённый набор фактов.

Что считается фактом: цель, требования, ограничения (сроки, бюджет, запреты), предпочтения, принятые решения, договорённости, имена, числа, названия.
Правила:
- ключи короткие, по-русски, в нижнем регистре: "цель", "срок", "бюджет", "платформа", "запрещено" и т.п.;
- значения — дословно, с числами и единицами, без пересказа;
- прежние факты сохраняй как есть, пока пользователь их не изменил или не отменил;
- если пользователь изменил факт — замени значение; отменил — удали ключ;
- решение, которое ассистент предложил, а пользователь принял, — тоже факт;
- советы ассистента, которые пользователь не принял, фактами не являются;
- ничего не выдумывай и ничего не вычисляй за пользователя.`

// factsBlock — как факты подставляются в системный промпт.
func factsBlock(facts []Fact) string {
	var b strings.Builder
	b.WriteString("Ключевые факты этого разговора (важнее истории — ранние сообщения в запрос не входят):\n")
	for _, f := range facts {
		fmt.Fprintf(&b, "- %s: %s\n", f.Key, f.Value)
	}
	return strings.TrimRight(b.String(), "\n")
}

// updateFacts обновляет блок фактов по новому сообщению пользователя.
// Ошибка не ломает ход: вопрос уйдёт с прежними фактами.
func (a *Agent) updateFacts(ctx context.Context, cfg Config, hist []llm.Message, facts []Fact, text string, gen uint64, turn *Turn, on func(Event)) []Fact {
	if cfg.Context != ContextFacts {
		return facts
	}
	current, _ := json.Marshal(factsMap(facts))
	lastReply := "(ответов ещё не было)"
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Role == llm.RoleAssistant {
			lastReply = hist[i].Content
			break
		}
	}
	req := llm.Request{
		Model: cfg.Model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: factsSystem},
			{Role: llm.RoleUser, Content: fmt.Sprintf(
				"Текущие факты:\n%s\n\nПоследний ответ ассистента:\n%s\n\nНовое сообщение пользователя:\n%s",
				current, strings.TrimSpace(lastReply), text)},
		},
		Temperature:    llm.F(0),
		Thinking:       &llm.Thinking{Type: "disabled"},
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}
	start := time.Now()
	resp, err := a.client.Chat(ctx, req)
	a.record(req, resp, err, "обновление фактов", true)
	a.accountAux(resp, err)

	var next []Fact
	if err == nil {
		turn.AuxCalls++
		turn.AuxPrompt += resp.Usage.PromptTokens
		turn.AuxCompletion += resp.Usage.CompletionTokens
		next, err = parseFacts(resp.Content)
	}
	if err != nil {
		on(Event{Kind: EventContext, Label: "факты не обновлены — вопрос уйдёт с прежними", Content: err.Error()})
		return facts
	}

	on(Event{Kind: EventContext, Label: factsDiffLabel(facts, next), Content: factsBlockPlain(next),
		Usage: resp.Usage, CostUSD: resp.CostUSD, Latency: time.Since(start)})
	a.mu.Lock()
	if a.gen == gen {
		a.facts = next
		a.touch()
	}
	a.mu.Unlock()
	return next
}

// accountAux — служебный вызов стратегии в расходе агента и пула.
func (a *Agent) accountAux(resp *llm.Response, err error) {
	a.mu.Lock()
	if err != nil {
		a.stats.Errors++
	} else {
		a.stats.add(resp)
	}
	a.mu.Unlock()
	if a.onCall != nil {
		a.onCall(resp, err)
	}
}

// parseFacts читает {"facts": {...}} с сохранением порядка ключей:
// map в Go порядок теряет, а в промпте факты должны идти стабильно,
// иначе меняющийся порядок каждый раз сбивает кэш префикса.
func parseFacts(raw string) ([]Fact, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "```"), "```")
	var wrap struct {
		Facts json.RawMessage `json:"facts"`
	}
	if err := json.Unmarshal([]byte(raw), &wrap); err != nil {
		return nil, fmt.Errorf("факты не в JSON: %w", err)
	}
	if len(wrap.Facts) == 0 {
		return nil, fmt.Errorf("в ответе нет поля facts")
	}
	dec := json.NewDecoder(bytes.NewReader(wrap.Facts))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("facts — не объект")
	}
	var out []Fact
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var val any
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		key := strings.TrimSpace(fmt.Sprint(keyTok))
		if key == "" {
			continue
		}
		out = append(out, Fact{Key: key, Value: factValue(val)})
	}
	return out, nil
}

// factValue — значение факта строкой; списки склеиваются через «; ».
func factValue(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, factValue(e))
		}
		return strings.Join(parts, "; ")
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func factsMap(facts []Fact) map[string]string {
	m := make(map[string]string, len(facts))
	for _, f := range facts {
		m[f.Key] = f.Value
	}
	return m
}

func factsBlockPlain(facts []Fact) string {
	var b strings.Builder
	for _, f := range facts {
		fmt.Fprintf(&b, "%s: %s\n", f.Key, f.Value)
	}
	return strings.TrimRight(b.String(), "\n")
}

// factsDiffLabel — что изменилось: для ленты важнее разница, чем весь блок.
func factsDiffLabel(prev, next []Fact) string {
	old := factsMap(prev)
	var added, changed, removed int
	seen := map[string]bool{}
	for _, f := range next {
		seen[f.Key] = true
		v, ok := old[f.Key]
		switch {
		case !ok:
			added++
		case v != f.Value:
			changed++
		}
	}
	for _, f := range prev {
		if !seen[f.Key] {
			removed++
		}
	}
	if added+changed+removed == 0 {
		return fmt.Sprintf("факты без изменений (%d)", len(next))
	}
	return fmt.Sprintf("факты обновлены: +%d, изменено %d, удалено %d — всего %d", added, changed, removed, len(next))
}

// Facts — текущий блок фактов.
func (a *Agent) Facts() []Fact {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Fact(nil), a.facts...)
}
