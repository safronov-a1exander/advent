package agent

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/safronov-a1exander/advent/internal/llm"
)

// Учёт токенов (день 8).
//
// Точное число токенов знает только провайдер, и узнаём мы его после ответа —
// поле usage. Но часть решений надо принимать до отправки: влезет ли запрос
// в окно, сколько весит новый вопрос сам по себе. Поэтому здесь два источника:
//
//   - факт: prompt/completion/reasoning/cached из usage каждого ответа;
//   - оценка: длина текста, делённая на среднюю длину токена, с калибровкой
//     по факту — после каждого ответа агент сравнивает свою оценку запроса
//     с тем, что насчитал провайдер, и поправляет коэффициент.
//
// Чужой токенизатор (tiktoken и подобные) сюда не подключён намеренно:
// словарь OpenAI заметно ошибается на моделях других провайдеров, особенно
// на кириллице, а калибровка по usage подстраивается под любую модель.

// charsPerToken — стартовая оценка средней длины токена в символах для
// смешанного русско-английского текста. Замер на deepseek-flash: системный
// промпт и вопрос общей длиной 198 символов дали 98 prompt-токенов.
// Дальше коэффициент уточняется по факту.
const charsPerToken = 2.2

// messageOverhead — служебные токены шаблона чата на каждое сообщение
// (роль, разделители). Точное значение у каждого провайдера своё — его
// тоже поглощает калибровка.
const messageOverhead = 4

// rawEstimate — оценка без калибровки.
func rawEstimate(text string) float64 {
	return float64(utf8.RuneCountInString(text)) / charsPerToken
}

func rawEstimateMessages(msgs []llm.Message) float64 {
	var n float64
	for _, m := range msgs {
		n += rawEstimate(m.Content) + messageOverhead
	}
	return n
}

// Turn — расход одного хода диалога.
type Turn struct {
	At time.Time `json:"at"`
	// Question — оценка токенов нового вопроса. Отдельно провайдер его не
	// считает: в usage приходит весь запрос целиком.
	Question int `json:"question_est"`
	// Prompt — факт: весь отправленный запрос — system, история и вопрос.
	// Для цепочек (стратегии дня 3) — сумма по всем шагам.
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
	Reasoning  int `json:"reasoning_tokens"`
	Cached     int `json:"cached_tokens"`
	Calls      int `json:"calls"`
	// Estimated — какой запрос агент насчитал до отправки; рядом с Prompt
	// показывает ошибку оценки.
	Estimated int `json:"prompt_est"`
	// Sent — сколько сообщений истории ушло в запрос вместе с вопросом:
	// у полной истории это вся история, у summary и окна — только хвост.
	Sent int `json:"sent_messages,omitempty"`

	// Aux* — служебные вызовы стратегии контекста на этом ходе: сжатие
	// истории (день 9), обновление фактов (день 10). Считаются отдельно:
	// они сами стоят токенов, и без них сравнение стратегий было бы нечестным.
	AuxCalls      int `json:"aux_calls,omitempty"`
	AuxPrompt     int `json:"aux_prompt_tokens,omitempty"`
	AuxCompletion int `json:"aux_completion_tokens,omitempty"`
}

// History — токены запроса без нового вопроса: system и прошлые реплики.
// Это та часть, что растёт от хода к ходу и которую приходится платить
// заново каждым следующим вопросом.
func (t Turn) History() int {
	if h := t.Prompt - t.Question; h > 0 {
		return h
	}
	return 0
}

// ErrContextOverflow — запрос не влезает в лимит контекста агента.
// Проверка идёт до отправки: такой запрос не тратит ни токенов, ни времени.
type ErrContextOverflow struct {
	Estimated int
	Limit     int
}

func (e *ErrContextOverflow) Error() string {
	return fmt.Sprintf("контекст переполнен: запрос ~%d токенов при лимите агента %d — запрос не отправлен", e.Estimated, e.Limit)
}

// IsContextOverflow — переполнение контекста на любой стороне: собственный
// лимит агента или отказ провайдера, у которого окно кончилось на его стороне.
func IsContextOverflow(err error) bool {
	var ov *ErrContextOverflow
	if errors.As(err, &ov) {
		return true
	}
	var api *llm.APIError
	if errors.As(err, &api) {
		body := strings.ToLower(api.Body)
		return strings.Contains(body, "context_length") || strings.Contains(body, "maximum context length")
	}
	return false
}

// ContextUsage — сколько займёт следующий запрос и какой у агента лимит.
// Лимит 0 — у агента своего лимита нет, решает окно модели.
type ContextUsage struct {
	Estimated int
	Limit     int
}

// Percent — заполнение лимита; -1, если лимита нет.
func (c ContextUsage) Percent() int {
	if c.Limit <= 0 {
		return -1
	}
	return c.Estimated * 100 / c.Limit
}

// Estimate — калиброванная оценка токенов текста для этого агента.
func (a *Agent) Estimate(text string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calibrated(rawEstimate(text))
}

// Context — оценка запроса, который уйдёт с вопросом text (пустой — без вопроса).
func (a *Agent) Context(text string) ContextUsage {
	a.mu.Lock()
	defer a.mu.Unlock()
	system, past := window(a.cfg, a.history, a.summary, a.facts)
	msgs := compose(system, past, text)
	return ContextUsage{Estimated: a.calibrated(rawEstimateMessages(msgs)), Limit: a.cfg.limit()}
}

// Calibration — во сколько раз факт провайдера отличается от сырой оценки.
func (a *Agent) Calibration() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calibration()
}

// Turns — расход по ходам.
func (a *Agent) Turns() []Turn {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Turn(nil), a.turns...)
}

func (a *Agent) calibration() float64 {
	if a.calib <= 0 {
		return 1
	}
	return a.calib
}

// calibrated — вызывать под a.mu.
func (a *Agent) calibrated(raw float64) int {
	return int(raw*a.calibration() + 0.5)
}

// learn уточняет коэффициент по факту провайдера; вызывать под a.mu.
// Скользящее среднее, а не последнее значение: у коротких запросов
// служебный шаблон весит непропорционально много, и один такой запрос
// не должен сбивать оценку длинных.
func (a *Agent) learn(raw float64, actual int) {
	if raw <= 0 || actual <= 0 {
		return
	}
	ratio := float64(actual) / raw
	if a.calib <= 0 {
		a.calib = ratio
		return
	}
	a.calib = 0.6*a.calib + 0.4*ratio
}

func (c Config) limit() int {
	if c.ContextLimit != nil && *c.ContextLimit > 0 {
		return *c.ContextLimit
	}
	return 0
}
