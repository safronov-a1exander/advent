// Package agent — агент как отдельная сущность (день 6).
//
// На первой неделе диалог жил внутри экрана чата: история сообщений,
// сборка запроса, вызов API и счётчики были полями модели Bubble Tea.
// Второго собеседника с другими настройками завести было негде, а
// сравнение моделей по Ctrl+E собирало запросы в обход общей логики.
//
// Здесь всё это собрано в один объект. Агент:
//
//   - владеет своим конфигом (Config) — модель, промпт, параметры;
//   - сам складывает стек сообщений и сам шлёт его в LLM;
//   - считает свой расход и пишет каждый вызов в журнал;
//   - ничего не знает об интерфейсе: снаружи видны только Ask, события
//     по ходу ответа и итог.
//
// Агентов порождает Pool — сколько угодно в одном процессе, каждый со своим
// конфигом и своей историей.
package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/store"
)

// ErrBusy — агент уже отвечает. Один агент ведёт один разговор: два
// параллельных вопроса перемешали бы историю.
var ErrBusy = errors.New("агент занят: дождись ответа на предыдущий вопрос")

// Journal — куда агент пишет каждый вызов. *store.Writer подходит как есть.
type Journal interface {
	Append(store.Record) error
}

// EventKind — что произошло по ходу ответа.
type EventKind int

const (
	// EventStep — закончился промежуточный шаг цепочки (стратегии дня 3).
	EventStep EventKind = iota
	// EventFinalStart — начинается финальный шаг; его текст пойдёт кусками.
	EventFinalStart
	// EventChunk — очередной кусок финального ответа.
	EventChunk
	// EventContext — служебный вызов стратегии контекста: сжатие истории
	// в сводку (день 9) или обновление фактов (день 10).
	EventContext
)

// Event — уведомление для того, кто показывает ответ. Агенту всё равно,
// куда оно уйдёт: в TUI, в консоль или никуда.
type Event struct {
	Kind    EventKind
	Label   string
	Content string
	Usage   llm.Usage
	CostUSD float64
	Latency time.Duration
}

// Step — один вызов LLM внутри ответа.
type Step struct {
	Label    string
	Response *llm.Response
}

// Reply — итог одного Ask: финальный ответ и все вызовы, из которых он собран.
type Reply struct {
	Final *llm.Response
	Steps []Step
}

// Usage — суммарный расход всех шагов ответа.
func (r *Reply) Usage() (u llm.Usage, cost float64) {
	for _, s := range r.Steps {
		u.PromptTokens += s.Response.Usage.PromptTokens
		u.CompletionTokens += s.Response.Usage.CompletionTokens
		u.ReasoningTokens += s.Response.Usage.ReasoningTokens
		u.CachedPromptTokens += s.Response.Usage.CachedPromptTokens
		u.TotalTokens += s.Response.Usage.TotalTokens
		cost += s.Response.CostUSD
	}
	return u, cost
}

// Stats — накопленный расход агента.
type Stats struct {
	Turns      int     `json:"turns"` // ходов диалога, закончившихся ответом
	Calls      int     `json:"calls"` // вызовов API, включая промежуточные шаги
	Errors     int     `json:"errors"`
	Prompt     int     `json:"prompt_tokens"`
	Completion int     `json:"completion_tokens"`
	Reasoning  int     `json:"reasoning_tokens"`
	Cached     int     `json:"cached_tokens"`
	CostUSD    float64 `json:"cost_usd"`
}

func (s *Stats) add(r *llm.Response) {
	s.Calls++
	s.Prompt += r.Usage.PromptTokens
	s.Completion += r.Usage.CompletionTokens
	s.Reasoning += r.Usage.ReasoningTokens
	s.Cached += r.Usage.CachedPromptTokens
	s.CostUSD += r.CostUSD
}

// Agent — один собеседник со своим конфигом и своей историей.
// Методы безопасны для вызова из разных горутин.
type Agent struct {
	id       string
	provider string
	client   llm.Provider
	journal  Journal
	// onCall — пул узнаёт о каждом вызове, чтобы вести общий счётчик.
	onCall func(*llm.Response, error)
	// onChange — разговор или конфиг поменялись; пул сохраняет агента.
	onChange func(*Agent)
	// temp — временный агент (сравнение моделей): на диск не пишется.
	temp bool
	// saved — агент уже лежит в хранилище (сохранён или поднят оттуда).
	saved atomic.Bool

	busy atomic.Bool

	mu      sync.Mutex
	cfg     Config
	history []llm.Message // только user/assistant; system подставляется при отправке
	stats   Stats
	turns   []Turn       // расход по ходам текущего разговора (день 8)
	calib   float64      // во сколько раз факт провайдера больше сырой оценки; 0 — ещё не знаем
	summary summaryState // сводка старой части разговора (день 9)
	facts   []Fact       // блок фактов для sticky facts (день 10)

	// Ветки разговора (день 10, branch.go). Активная ветка — это поля выше,
	// неактивные отложены в parked.
	branch      string
	parked      map[string]thread
	checkpoints []checkpoint
	branchOrder []string
	branchFrom  map[string]string

	created time.Time
	updated time.Time
	// rev растёт при каждом изменении, которое стоит сохранить. По нему
	// хранилище отбрасывает запоздавший старый снимок, если два сохранения
	// одного агента разминулись.
	rev uint64
	// gen растёт при сбросе контекста. Ответ, начатый до сброса, не должен
	// вернуть в историю то, что пользователь только что стёр.
	gen uint64
}

// ID — уникальный в пределах пула идентификатор.
func (a *Agent) ID() string { return a.id }

// Busy — отвечает ли агент прямо сейчас.
func (a *Agent) Busy() bool { return a.busy.Load() }

// Created — когда агент порождён.
func (a *Agent) Created() time.Time { return a.created }

// Config — копия текущего конфига.
func (a *Agent) Config() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Clone()
}

// SetConfig меняет конфиг. Действует со следующего вопроса; история
// при этом не трогается — можно сменить модель посреди разговора.
func (a *Agent) SetConfig(c Config) {
	a.mu.Lock()
	if reflect.DeepEqual(a.cfg, c) {
		// Экран отдаёт конфиг перед каждым вопросом; без проверки
		// каждый вопрос означал бы лишнюю запись на диск.
		a.mu.Unlock()
		return
	}
	a.cfg = c.Clone()
	a.touch()
	a.mu.Unlock()
	a.changed()
}

// History — копия истории диалога.
func (a *Agent) History() []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Message(nil), a.history...)
}

// Title — название разговора для списков: первый вопрос пользователя.
// Имя id вроде «бюджет-003» ничего не говорит человеку, который вернулся
// к разговору через час; первая реплика обычно говорит, о чём он.
func (a *Agent) Title() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range a.history {
		if m.Role == llm.RoleUser {
			return strings.Join(strings.Fields(m.Content), " ")
		}
	}
	return ""
}

// Stats — копия счётчиков.
func (a *Agent) Stats() Stats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats
}

// Reset стирает историю. Счётчики расхода остаются: деньги уже потрачены.
func (a *Agent) Reset() {
	a.mu.Lock()
	a.history = nil
	a.turns = nil // учёт ходов — про разговор; калибровка — про модель, её не трогаем
	a.summary = summaryState{}
	a.facts = nil
	a.resetBranches() // сброс — новый разговор: ветки и чекпойнты тоже уходят
	a.gen++
	a.touch()
	a.mu.Unlock()
	a.changed()
}

// Updated — когда разговор или конфиг менялись последний раз.
func (a *Agent) Updated() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.updated
}

// Temp — временный ли агент (не сохраняется между запусками).
func (a *Agent) Temp() bool { return a.temp }

// touch отмечает изменение; вызывать под a.mu.
func (a *Agent) touch() {
	a.rev++
	a.updated = time.Now()
}

// changed сообщает пулу об изменении; вызывать без a.mu —
// сохранение само снимает копию под замком.
func (a *Agent) changed() {
	if a.onChange != nil {
		a.onChange(a)
	}
}

// Messages — стек, который уйдёт в API при следующем вопросе text:
// системный промпт, вся история и сам вопрос. Нужен для отладочного вида.
func (a *Agent) Messages(text string) []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	system, past := window(a.cfg, a.history, a.summary, a.facts)
	return compose(system, past, text)
}

func compose(system string, history []llm.Message, text string) []llm.Message {
	msgs := make([]llm.Message, 0, len(history)+2)
	if s := strings.TrimSpace(system); s != "" {
		msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: s})
	}
	msgs = append(msgs, history...)
	if text != "" {
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: text})
	}
	return msgs
}

// Ask — один ход диалога: вопрос уходит в LLM вместе со всей историей,
// ответ добавляется в историю. on получает события по ходу ответа и
// может быть nil.
//
// История меняется только при успехе: неотвеченный вопрос в неё не попадает,
// поэтому после ошибки его можно просто задать заново.
func (a *Agent) Ask(ctx context.Context, text string, on func(Event)) (*Reply, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("пустой вопрос")
	}
	if !a.busy.CompareAndSwap(false, true) {
		return nil, ErrBusy
	}
	defer a.busy.Store(false)
	if on == nil {
		on = func(Event) {}
	}

	// Всё, что нужно для ответа, снимаем под замком один раз: конфиг могут
	// поменять из интерфейса, пока запрос в полёте.
	a.mu.Lock()
	cfg := a.cfg.Clone()
	hist := append([]llm.Message(nil), a.history...)
	sum := a.summary
	facts := append([]Fact(nil), a.facts...)
	gen := a.gen
	turn := Turn{At: time.Now(), Question: a.calibrated(rawEstimate(text))}
	a.mu.Unlock()

	// День 9: если разговор разросся, старая часть сначала сжимается в сводку,
	// и уже этот вопрос уходит с коротким контекстом.
	sum = a.compress(ctx, cfg, hist, sum, gen, &turn, on)
	// День 10: факты обновляются по новому сообщению до отправки — вопрос
	// уходит уже со свежим блоком.
	facts = a.updateFacts(ctx, cfg, hist, facts, text, gen, &turn, on)
	system, past := window(cfg, hist, sum, facts)

	a.mu.Lock()
	turn.Estimated = a.calibrated(rawEstimateMessages(compose(system, past, text)))
	turn.Sent = len(past)
	a.mu.Unlock()

	// Свой лимит контекста агента проверяется до отправки: запрос, который
	// заведомо не влезет, не должен тратить ни токенов, ни времени. История
	// не меняется — разговор можно сбросить или продолжить в новом агенте.
	if limit := cfg.limit(); limit > 0 && turn.Estimated > limit {
		return nil, &ErrContextOverflow{Estimated: turn.Estimated, Limit: limit}
	}

	chain := Chain(cfg.Strategy)
	prev := map[string]string{}
	reply := &Reply{}

	for _, st := range chain {
		// История (в том виде, в каком её собрала стратегия контекста)
		// подмешивается только в финальный шаг: промежуточные шаги —
		// самостоятельные вызовы со своими ролями.
		stepSystem := cfg.System
		var stepPast []llm.Message
		if st.Final {
			stepSystem, stepPast = system, past
		}
		if st.System != "" {
			stepSystem = st.System
		}
		req := llm.Request{Messages: compose(stepSystem, stepPast, st.Build(text, prev))}
		cfg.Apply(&req)

		if st.Final && len(chain) > 1 {
			on(Event{Kind: EventFinalStart, Label: st.Label})
		}

		resp, err := a.call(ctx, req, st, cfg.Stream, on)
		a.record(req, resp, err, st.Label, len(chain) > 1)
		if err != nil {
			return reply, err
		}
		reply.Steps = append(reply.Steps, Step{Label: st.Label, Response: resp})

		// Факт провайдера уточняет оценку: следующая проверка лимита
		// и следующий вопрос посчитаются уже по этой модели.
		a.mu.Lock()
		a.learn(rawEstimateMessages(req.Messages), resp.Usage.PromptTokens)
		a.mu.Unlock()
		turn.Calls++
		turn.Prompt += resp.Usage.PromptTokens
		turn.Completion += resp.Usage.CompletionTokens
		turn.Reasoning += resp.Usage.ReasoningTokens
		turn.Cached += resp.Usage.CachedPromptTokens

		if !st.Final {
			if st.Capture != "" {
				prev[st.Capture] = resp.Content
			}
			on(Event{Kind: EventStep, Label: st.Label, Content: resp.Content,
				Usage: resp.Usage, CostUSD: resp.CostUSD, Latency: resp.Latency})
			continue
		}
		reply.Final = resp
	}

	a.mu.Lock()
	if a.gen == gen {
		a.history = append(a.history,
			llm.Message{Role: llm.RoleUser, Content: text},
			llm.Message{Role: llm.RoleAssistant, Content: reply.Final.Content})
		// ход без сброса попадает в учёт; начатый до сброса относится
		// к стёртому разговору, и его рост токенов уже ни о чём не говорит
		a.turns = append(a.turns, turn)
	}
	a.stats.Turns++
	a.touch()
	a.mu.Unlock()
	a.changed()
	return reply, nil
}

// call — один вызов API. Финальный шаг при включённом стриминге идёт
// потоком; без стриминга весь текст приходит одним куском.
func (a *Agent) call(ctx context.Context, req llm.Request, st ChainStep, stream bool, on func(Event)) (*llm.Response, error) {
	var (
		resp *llm.Response
		err  error
	)
	switch {
	case st.Final && stream:
		resp, err = a.client.ChatStream(ctx, req, func(c llm.Chunk) error {
			if !c.Done && c.Content != "" {
				on(Event{Kind: EventChunk, Content: c.Content})
			}
			return nil
		})
	default:
		resp, err = a.client.Chat(ctx, req)
		if err == nil && st.Final {
			on(Event{Kind: EventChunk, Content: resp.Content})
		}
	}

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
	return resp, err
}

// record пишет вызов в журнал: ровно те сообщения, что ушли в API.
func (a *Agent) record(req llm.Request, resp *llm.Response, err error, label string, chained bool) {
	if a.journal == nil {
		return
	}
	rec := store.Record{
		Agent:    a.id,
		Provider: a.provider,
		Model:    req.Model,
		Params:   store.ParamsOf(req),
		Messages: req.Messages,
	}
	if chained {
		rec.Variant = label
	}
	if err != nil {
		rec.Error = err.Error()
	}
	if resp != nil {
		rec.Content = resp.Content
		rec.Reasoning = resp.Reasoning
		rec.Finish = resp.FinishReason
		rec.Usage = resp.Usage
		rec.LatencyMS = resp.Latency.Milliseconds()
		rec.CostUSD = resp.CostUSD
	}
	_ = a.journal.Append(rec)
}
