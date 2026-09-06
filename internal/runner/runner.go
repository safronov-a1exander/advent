// Package runner прогоняет сценарий: каждый вариант, каждый повтор,
// каждый шаг — с записью в журнал и автоматическими проверками ответа.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/scenario"
	"github.com/safronov-a1exander/advent/internal/store"
)

type StepResult struct {
	Label     string
	Prompt    string
	Content   string
	Reasoning string
	Finish    string
	Usage     llm.Usage
	Latency   time.Duration
	CostUSD   float64
	Err       error
}

type CheckResult struct {
	Name   string
	OK     bool
	Detail string
}

// Attempt — один прогон одного варианта.
type Attempt struct {
	VariantID string
	Label     string
	Model     string
	Params    scenario.Params
	N         int
	Steps     []StepResult
	Final     string
	Usage     llm.Usage
	Latency   time.Duration
	CostUSD   float64
	Finish    string
	Checks    []CheckResult
	Err       error
}

func (a Attempt) ChecksOK() bool {
	for _, c := range a.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

type Result struct {
	Scenario *scenario.Scenario
	Provider string
	RunID    string
	Attempts []Attempt
	Started  time.Time
	Finished time.Time
}

// Progress — колбэк для UI: вызывается до и после каждого варианта.
type Progress func(ev Event)

type Event struct {
	Kind      string // "variant-start" | "step-done" | "variant-done"
	VariantID string
	Label     string
	N         int
	Attempt   *Attempt
}

type Runner struct {
	Client   *llm.Client
	Provider string
	Store    *store.Writer
	Fallback string // модель по умолчанию, если её нет в сценарии
	// Override — параметры, навязанные поверх сценария из панели интерфейса.
	// Накладываются последними, поэтому бьют и вариант, и шаг.
	Override *scenario.Params
	// ModelOverride — прогнать все варианты на одной модели.
	// Пусто — модель берётся из сценария.
	ModelOverride string
	// SystemOverride — заменить системный промпт во всех вариантах.
	SystemOverride string
}

func (r *Runner) Run(ctx context.Context, s *scenario.Scenario, onProgress Progress) (*Result, error) {
	res := &Result{Scenario: s, Provider: r.Provider, Started: time.Now()}
	if r.Store != nil {
		res.RunID = r.Store.RunID()
	}

	for _, v := range s.Variants {
		for n := 1; n <= s.Repeat; n++ {
			if err := ctx.Err(); err != nil {
				return res, err
			}
			if onProgress != nil {
				onProgress(Event{Kind: "variant-start", VariantID: v.ID, Label: v.Label, N: n})
			}
			a := r.runVariant(ctx, s, v, n)
			res.Attempts = append(res.Attempts, a)
			if onProgress != nil {
				cp := a
				onProgress(Event{Kind: "variant-done", VariantID: v.ID, Label: v.Label, N: n, Attempt: &cp})
			}
		}
	}
	res.Finished = time.Now()
	return res, nil
}

func (r *Runner) runVariant(ctx context.Context, s *scenario.Scenario, v scenario.Variant, n int) Attempt {
	model := s.EffectiveModel(v, r.Fallback)
	if r.ModelOverride != "" {
		model = r.ModelOverride
	}
	a := Attempt{VariantID: v.ID, Label: v.Label, Model: model, N: n}

	vars := map[string]string{}
	for k, val := range s.Vars {
		vars[k] = val
	}

	sysPrompt := v.System
	if sysPrompt == "" {
		sysPrompt = s.System
	}
	if r.SystemOverride != "" {
		sysPrompt = r.SystemOverride
	}

	var history []llm.Message
	for i, st := range v.Steps {
		params := s.EffectiveParams(v, st).Merge(r.Override)
		a.Params = params

		userText, err := scenario.Render(st.User, vars)
		if err != nil {
			a.Err = fmt.Errorf("шаг %d: шаблон: %w", i+1, err)
			return a
		}
		stepSys := st.System
		if stepSys == "" {
			stepSys = sysPrompt
		}
		stepSys, err = scenario.Render(stepSys, vars)
		if err != nil {
			a.Err = fmt.Errorf("шаг %d: шаблон system: %w", i+1, err)
			return a
		}

		var msgs []llm.Message
		if st.KeepHistory && len(history) > 0 {
			msgs = append(msgs, history...)
		} else if stepSys != "" {
			msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: stepSys})
		}
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: userText})

		req := llm.Request{Model: model, Messages: msgs}
		params.Apply(&req)

		resp, err := r.Client.Chat(ctx, req)
		sr := StepResult{Label: st.Label, Prompt: userText, Err: err}
		if st.Label == "" {
			sr.Label = fmt.Sprintf("шаг %d", i+1)
		}

		rec := store.Record{
			Scenario: s.Name, Variant: v.ID, Step: i + 1, Attempt: n,
			Provider: r.Provider, Model: model,
			Params: store.ParamsOf(req), Messages: msgs,
		}

		if err != nil {
			sr.Err = err
			a.Steps = append(a.Steps, sr)
			a.Err = fmt.Errorf("шаг %d: %w", i+1, err)
			rec.Error = err.Error()
			r.log(rec)
			return a
		}

		sr.Content, sr.Reasoning, sr.Finish = resp.Content, resp.Reasoning, resp.FinishReason
		sr.Usage, sr.Latency, sr.CostUSD = resp.Usage, resp.Latency, resp.CostUSD
		a.Steps = append(a.Steps, sr)

		a.Usage.PromptTokens += resp.Usage.PromptTokens
		a.Usage.CompletionTokens += resp.Usage.CompletionTokens
		a.Usage.ReasoningTokens += resp.Usage.ReasoningTokens
		a.Usage.CachedPromptTokens += resp.Usage.CachedPromptTokens
		a.Usage.TotalTokens += resp.Usage.TotalTokens
		a.Latency += resp.Latency
		a.CostUSD += resp.CostUSD
		a.Finish = resp.FinishReason
		a.Final = resp.Content

		rec.Content, rec.Reasoning, rec.Finish = resp.Content, resp.Reasoning, resp.FinishReason
		rec.Usage, rec.LatencyMS, rec.CostUSD = resp.Usage, resp.Latency.Milliseconds(), resp.CostUSD
		r.log(rec)

		history = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: resp.Content})
		if st.Capture != "" {
			vars[st.Capture] = resp.Content
		}
		vars["last"] = resp.Content
	}

	a.Checks = Verify(s.EffectiveChecks(v), a.Final, a.Finish)
	return a
}

func (r *Runner) log(rec store.Record) {
	if r.Store != nil {
		_ = r.Store.Append(rec)
	}
}

// ---- проверки ----

// Verify прогоняет объявленные проверки по финальному ответу варианта.
func Verify(c scenario.Checks, content, finish string) []CheckResult {
	if c.Empty() {
		return nil
	}
	var out []CheckResult
	body := strings.TrimSpace(content)

	var root any
	if c.JSON || c.ArrayPath != "" || len(c.RequiredFields) > 0 {
		cleaned := stripFence(body)
		err := json.Unmarshal([]byte(cleaned), &root)
		out = append(out, CheckResult{
			Name:   "валидный JSON",
			OK:     err == nil,
			Detail: errText(err),
		})
		if err != nil {
			return out
		}
	}

	if c.ArrayPath != "" || len(c.RequiredFields) > 0 {
		items, detail := extractArray(root, c.ArrayPath)
		out = append(out, CheckResult{
			Name:   "массив " + orDash(c.ArrayPath),
			OK:     items != nil,
			Detail: detail,
		})
		if items != nil && len(c.RequiredFields) > 0 {
			missing := map[string]int{}
			for _, it := range items {
				obj, ok := it.(map[string]any)
				if !ok {
					missing["<элемент не объект>"]++
					continue
				}
				for _, f := range c.RequiredFields {
					if _, has := obj[f]; !has {
						missing[f]++
					}
				}
			}
			out = append(out, CheckResult{
				Name:   "поля " + strings.Join(c.RequiredFields, ", "),
				OK:     len(missing) == 0,
				Detail: missDetail(missing, len(items)),
			})
		}
	}

	if c.MaxWords > 0 {
		n := len(strings.Fields(body))
		out = append(out, CheckResult{
			Name:   fmt.Sprintf("не длиннее %d слов", c.MaxWords),
			OK:     n <= c.MaxWords,
			Detail: fmt.Sprintf("%d слов", n),
		})
	}
	if c.MaxChars > 0 {
		n := len([]rune(body))
		out = append(out, CheckResult{
			Name:   fmt.Sprintf("не длиннее %d символов", c.MaxChars),
			OK:     n <= c.MaxChars,
			Detail: fmt.Sprintf("%d символов", n),
		})
	}
	for _, s := range c.MustContain {
		out = append(out, CheckResult{Name: "содержит " + quote(s), OK: strings.Contains(body, s)})
	}
	for _, s := range c.MustNotContain {
		out = append(out, CheckResult{Name: "не содержит " + quote(s), OK: !strings.Contains(body, s)})
	}
	if c.FinishReason != "" {
		out = append(out, CheckResult{
			Name:   "finish_reason = " + c.FinishReason,
			OK:     finish == c.FinishReason,
			Detail: finish,
		})
	}
	return out
}

// stripFence убирает ```json … ``` — модели любят оборачивать ответ,
// даже когда просишь чистый JSON. Убираем перед разбором, но отдельная
// проверка «содержит ```» ловит это, если формат важен буквально.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if j := strings.LastIndex(s, "```"); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}

func extractArray(root any, path string) ([]any, string) {
	if path == "" {
		if arr, ok := root.([]any); ok {
			return arr, fmt.Sprintf("%d элементов", len(arr))
		}
		// массив может лежать в единственном поле объекта
		if obj, ok := root.(map[string]any); ok {
			for k, v := range obj {
				if arr, ok := v.([]any); ok {
					return arr, fmt.Sprintf("%d элементов в поле %q", len(arr), k)
				}
			}
		}
		return nil, "массива на верхнем уровне нет"
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return nil, "корень не объект"
	}
	v, has := obj[path]
	if !has {
		return nil, "нет поля " + quote(path)
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, "поле " + quote(path) + " не массив"
	}
	return arr, fmt.Sprintf("%d элементов", len(arr))
}

func missDetail(missing map[string]int, total int) string {
	if len(missing) == 0 {
		return fmt.Sprintf("все %d элементов полные", total)
	}
	var parts []string
	for f, n := range missing {
		parts = append(parts, fmt.Sprintf("%s нет у %d", f, n))
	}
	return strings.Join(parts, "; ")
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func quote(s string) string { return "\"" + s + "\"" }

func orDash(s string) string {
	if s == "" {
		return "(верхний уровень)"
	}
	return s
}
