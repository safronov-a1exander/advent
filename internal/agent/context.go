package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/llm"
)

// Управление контекстом (день 9).
//
// Здесь разведены две вещи, которые на днях 6–8 совпадали:
//
//   - память — вся история разговора. Она хранится целиком, показывается
//     пользователю и сохраняется на диск. Её ничто не сокращает;
//   - контекст — то, что уходит в модель с очередным вопросом. Он ограничен
//     окном и деньгами, и именно его стратегия решает, как собрать.
//
// Стратегии:
//   - полная история — всё как есть;
//   - summary (день 9) — последние N сообщений как есть, всё раньше — сводкой;
//   - sliding window (день 10) — только последние N сообщений, остальное
//     в запрос не попадает вовсе;
//   - sticky facts (день 10) — блок фактов «ключ: значение» и последние N.
//
// Ветки разговора (Branching, день 10) — не способ собрать контекст, а форма
// самой истории, поэтому они в branch.go и работают с любой стратегией.

// Стратегии контекста — поле Config.Context.
const (
	ContextFull    = ""        // вся история как есть
	ContextSummary = "summary" // сводка старой части + последние сообщения
	ContextWindow  = "window"  // только последние сообщения
	ContextFacts   = "facts"   // блок фактов + последние сообщения
)

// ContextStrategies — порядок перебора в панели.
var ContextStrategies = []string{ContextFull, ContextWindow, ContextFacts, ContextSummary}

// Параметры сжатия по умолчанию.
const (
	defaultKeepLast       = 4  // два последних хода — вопрос и ответ дважды
	defaultSummarizeEvery = 10 // сжимать, когда за хвостом накопилось 10 сообщений
)

// ContextLabel — имя стратегии для показа.
func ContextLabel(name string) string {
	switch name {
	case ContextSummary:
		return "summary"
	case ContextWindow:
		return "sliding window"
	case ContextFacts:
		return "sticky facts"
	default:
		return "полная история"
	}
}

// ContextHint — пояснение под панелью.
func ContextHint(name string) string {
	switch name {
	case ContextSummary:
		return "старая часть разговора уходит в модель сводкой, последние сообщения — как есть; история при этом хранится целиком"
	case ContextWindow:
		return "в модель уходят только последние сообщения («хвост как есть»); всё раньше модель не видит — дёшево, но ранние факты теряются"
	case ContextFacts:
		return "после каждого сообщения служебный вызов обновляет блок фактов «ключ: значение»; в модель уходят факты и последние сообщения"
	default:
		return "в модель уходит вся история; растёт с каждым ходом, зато ничего не теряется"
	}
}

func (c Config) keepLast() int {
	if c.KeepLast != nil && *c.KeepLast >= 0 {
		return *c.KeepLast
	}
	return defaultKeepLast
}

func (c Config) summarizeEvery() int {
	if c.SummarizeEvery != nil && *c.SummarizeEvery > 0 {
		return *c.SummarizeEvery
	}
	return defaultSummarizeEvery
}

// summaryState — что уже сжато. Хранится у агента рядом с историей.
type summaryState struct {
	Text string `json:"text,omitempty"`
	// Covered — сколько первых сообщений истории покрывает сводка.
	Covered int `json:"covered,omitempty"`
}

// window собирает то, что уйдёт в модель: системный промпт и прошлые
// сообщения. Вопрос добавляется отдельно.
func window(cfg Config, hist []llm.Message, sum summaryState, facts []Fact) (string, []llm.Message) {
	switch cfg.Context {
	case ContextWindow:
		return cfg.System, tail(hist, cfg.keepLast())
	case ContextFacts:
		system := cfg.System
		if len(facts) > 0 {
			system = joinSystem(cfg.System, factsBlock(facts))
		}
		return system, tail(hist, cfg.keepLast())
	}
	if cfg.Context != ContextSummary || sum.Text == "" {
		return cfg.System, hist
	}
	covered := sum.Covered
	if covered > len(hist) {
		covered = len(hist)
	}
	// Сводка дописывается к системному промпту, а не отдельным сообщением:
	// так её одинаково принимают все OpenAI-совместимые API, и модель
	// читает её как условие разговора, а не как реплику собеседника.
	block := "Краткое содержание более ранней части этого разговора (сами сообщения не приводятся):\n" + sum.Text
	return joinSystem(cfg.System, block), hist[covered:]
}

// joinSystem дописывает блок к системному промпту.
func joinSystem(system, block string) string {
	system = strings.TrimSpace(system)
	if system == "" {
		return block
	}
	return system + "\n\n" + block
}

// tail — последние n сообщений, не разрывая пару вопрос–ответ: окно
// всегда начинается с реплики пользователя.
func tail(hist []llm.Message, n int) []llm.Message {
	if n <= 0 {
		return nil
	}
	start := len(hist) - n
	if start <= 0 {
		return hist
	}
	if start%2 != 0 {
		start++
	}
	return hist[start:]
}

// needsCompression — пора ли сжимать: за сохраняемым хвостом накопилось
// не меньше summarizeEvery несжатых сообщений. Возвращает, до какого
// сообщения истории сжать.
func needsCompression(cfg Config, hist []llm.Message, sum summaryState) (int, bool) {
	if cfg.Context != ContextSummary {
		return 0, false
	}
	upTo := len(hist) - cfg.keepLast()
	if upTo-sum.Covered < cfg.summarizeEvery() {
		return 0, false
	}
	// Сжатие не должно разрывать пару вопрос-ответ: иначе в хвосте окажется
	// ответ без вопроса. История хранится парами, так что граница чётная.
	if upTo%2 != 0 {
		upTo--
	}
	if upTo <= sum.Covered {
		return 0, false
	}
	return upTo, true
}

// summarizeSystem — инструкция для сжатия. Язык и цифры оговорены явно:
// без этого модель норовит писать сводку русской переписки по-английски
// и округлять суммы — ровно то, что потом теряется.
//
// Вторая половина инструкции — про то, что НЕ сохранять. Первая версия
// сохраняла всё подряд, и на живом прогоне сводка к концу разговора дословно
// хранила абзацы советов ассистента: сжатие не сжимало, а выход на сводки
// оказался в 2,4 раза больше самих ответов. Факты пользователя важны дословно,
// советы ассистента — одной строкой о теме: при нужде модель даст их заново.
const summarizeSystem = `Ты сжимаешь переписку пользователя с ассистентом в сводку, которая заменит эти сообщения в следующих запросах.
Пиши по-русски, списком коротких пунктов.
Сохрани дословно всё, что сообщил или решил пользователь: имена, числа и суммы, даты, названия, цели, ограничения, договорённости, открытые вопросы.
Советы и объяснения ассистента не пересказывай: одна строка на тему, о чём был разговор, без подробностей.
Не добавляй ничего, чего нет в переписке. Не считай за пользователя то, чего он не просил.
Если дана предыдущая сводка — объедини её с новыми сообщениями в одну сводку: старые факты не теряй, устаревшие замени новыми.
Сводка должна быть заметно короче исходной переписки — не длиннее 150 слов.`

// summarize — один вызов сжатия: предыдущая сводка + новые сообщения → новая сводка.
func summarizeRequest(cfg Config, prev string, msgs []llm.Message) llm.Request {
	var b strings.Builder
	if prev != "" {
		b.WriteString("Предыдущая сводка:\n")
		b.WriteString(prev)
		b.WriteString("\n\n")
	}
	b.WriteString("Новые сообщения:\n")
	for _, m := range msgs {
		who := "Пользователь"
		if m.Role == llm.RoleAssistant {
			who = "Ассистент"
		}
		fmt.Fprintf(&b, "%s: %s\n", who, strings.TrimSpace(m.Content))
	}
	req := llm.Request{
		Model: cfg.Model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: summarizeSystem},
			{Role: llm.RoleUser, Content: b.String()},
		},
		// Сводка — не творчество: низкая температура и без рассуждений,
		// иначе сжатие само съест больше токенов, чем сэкономит.
		Temperature: llm.F(0),
		Thinking:    &llm.Thinking{Type: "disabled"},
	}
	return req
}

// compress сжимает историю до upTo, если пора. Ошибка сжатия не ломает ход:
// вопрос уйдёт с прежним контекстом, а сжать попробуем в следующий раз.
func (a *Agent) compress(ctx context.Context, cfg Config, hist []llm.Message, sum summaryState, gen uint64, turn *Turn, on func(Event)) summaryState {
	upTo, ok := needsCompression(cfg, hist, sum)
	if !ok {
		return sum
	}
	req := summarizeRequest(cfg, sum.Text, hist[sum.Covered:upTo])
	start := time.Now()
	resp, err := a.client.Chat(ctx, req)
	a.record(req, resp, err, "сжатие истории", true)
	a.accountAux(resp, err)
	if err != nil {
		on(Event{Kind: EventContext, Label: "сжатие не удалось — вопрос уйдёт без него", Content: err.Error()})
		return sum
	}

	next := summaryState{Text: strings.TrimSpace(resp.Content), Covered: upTo}
	turn.AuxCalls++
	turn.AuxPrompt += resp.Usage.PromptTokens
	turn.AuxCompletion += resp.Usage.CompletionTokens
	on(Event{Kind: EventContext,
		Label:   fmt.Sprintf("сжато %d сообщений в сводку (всего сводка покрывает %d)", upTo-sum.Covered, upTo),
		Content: next.Text, Usage: resp.Usage, CostUSD: resp.CostUSD, Latency: time.Since(start)})

	a.mu.Lock()
	if a.gen == gen {
		a.summary = next
		a.touch()
	}
	a.mu.Unlock()
	return next
}

// Summary — текущая сводка и сколько сообщений она покрывает.
func (a *Agent) Summary() (text string, covered int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.summary.Text, a.summary.Covered
}

// KeepLastN — сколько последних сообщений идёт в запрос как есть (с учётом умолчания).
func (c Config) KeepLastN() int { return c.keepLast() }

// SummarizeEveryN — сколько несжатых сообщений копится до сжатия (с учётом умолчания).
func (c Config) SummarizeEveryN() int { return c.summarizeEvery() }
