// Package dialog — прогон одного и того же диалога в нескольких вариантах
// контекста (дни 9–10).
//
// Сравнивать стратегии контекста вручную ненадёжно: ответы модели каждый
// раз разные, а разница в токенах проявляется только на длинном разговоре.
// Здесь разговор записан сценарием — реплики пользователя и проверки на
// ответы, — и каждый вариант проходит его отдельным агентом с чистого листа.
// На выходе по каждому ходу: сколько ушло в запрос, сколько из кэша, сколько
// стоило сжатие, прошли ли проверки на память.
package dialog

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/llm"
)

// Scenario — описание прогона.
type Scenario struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Defaults    agent.Config   `yaml:"defaults"`
	Variants    []agent.Config `yaml:"variants"`
	Dialog      []Line         `yaml:"dialog"`
}

// Line — одна реплика пользователя и, если нужно, проверка ответа.
type Line struct {
	Say string `yaml:"say"`
	// Expect — подстроки, которые обязаны быть в ответе (без учёта регистра).
	// Реплики с проверками — это вопросы на память о раннем разговоре.
	Expect []string `yaml:"expect"`
	// Note — зачем эта реплика: попадает в отчёт рядом с проверкой.
	Note string `yaml:"note"`
}

// Load читает сценарий.
func Load(path string) (*Scenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Scenario
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	switch {
	case len(s.Dialog) == 0:
		return nil, fmt.Errorf("%s: пустой dialog", path)
	case len(s.Variants) == 0:
		return nil, fmt.Errorf("%s: нет variants", path)
	}
	for i, l := range s.Dialog {
		if strings.TrimSpace(l.Say) == "" {
			return nil, fmt.Errorf("%s: реплика %d пустая", path, i+1)
		}
	}
	return &s, nil
}

// Step — результат одного хода варианта.
type Step struct {
	Say    string
	Answer string
	Err    string
	Turn   agent.Turn
	// Checked — у реплики были проверки; Passed — все подстроки нашлись.
	Checked bool
	Passed  bool
	Missing []string
	Cost    float64
}

// Result — прогон одного варианта.
type Result struct {
	Variant agent.Config
	AgentID string
	Steps   []Step
	Elapsed time.Duration
	Summary string // сводка к концу разговора, если была
}

// Totals — итоги варианта.
type Totals struct {
	Prompt, Cached, Completion  int
	CompressPrompt, CompressOut int
	CompressCalls               int
	Checks, Passed              int
	Errors                      int
	Cost                        float64
}

// Input — все входные токены варианта, включая служебные вызовы сжатия.
func (t Totals) Input() int { return t.Prompt + t.CompressPrompt }

// Output — все выходные токены, включая сводки.
func (t Totals) Output() int { return t.Completion + t.CompressOut }

// Totals считает итоги.
func (r Result) Totals() Totals {
	var t Totals
	for _, s := range r.Steps {
		t.Prompt += s.Turn.Prompt
		t.Cached += s.Turn.Cached
		t.Completion += s.Turn.Completion
		t.CompressPrompt += s.Turn.CompressPrompt
		t.CompressOut += s.Turn.CompressCompletion
		t.CompressCalls += s.Turn.CompressCalls
		t.Cost += s.Cost
		if s.Err != "" {
			t.Errors++
		}
		if s.Checked {
			t.Checks++
			if s.Passed {
				t.Passed++
			}
		}
	}
	return t
}

// Progress — что сообщать по ходу прогона.
type Progress func(variant string, step, total int)

// Run прогоняет все варианты. Варианты идут параллельно — это разные агенты
// без общего состояния, — а реплики внутри варианта строго по порядку.
func Run(ctx context.Context, pool *agent.Pool, catalog []llm.ModelInfo, s *Scenario, progress Progress) ([]Result, error) {
	cfgs := make([]agent.Config, len(s.Variants))
	for i, v := range s.Variants {
		cfg := agent.Overlay(s.Defaults, v)
		if cfg.Name == "" {
			cfg.Name = fmt.Sprintf("вариант %d", i+1)
		}
		cfg.Stream = false
		if err := cfg.Resolve(catalog); err != nil {
			return nil, err
		}
		cfgs[i] = cfg
	}

	results := make([]Result, len(cfgs))
	var wg sync.WaitGroup
	for i, cfg := range cfgs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = runVariant(ctx, pool, cfg, s.Dialog, progress)
		}()
	}
	wg.Wait()
	return results, nil
}

func runVariant(ctx context.Context, pool *agent.Pool, cfg agent.Config, lines []Line, progress Progress) Result {
	a := pool.SpawnTemp(cfg)
	defer pool.Remove(a.ID())
	res := Result{Variant: cfg, AgentID: a.ID()}
	start := time.Now()

	for i, l := range lines {
		if progress != nil {
			progress(cfg.Name, i+1, len(lines))
		}
		turnsBefore := len(a.Turns())
		reply, err := a.Ask(ctx, l.Say, nil)
		st := Step{Say: l.Say, Checked: len(l.Expect) > 0}
		if err != nil {
			st.Err = err.Error()
			res.Steps = append(res.Steps, st)
			continue
		}
		st.Answer = reply.Final.Content
		_, st.Cost = reply.Usage()
		if turns := a.Turns(); len(turns) > turnsBefore {
			st.Turn = turns[len(turns)-1]
		}
		if st.Checked {
			st.Passed, st.Missing = check(st.Answer, l.Expect)
		}
		res.Steps = append(res.Steps, st)
	}
	res.Elapsed = time.Since(start)
	res.Summary, _ = a.Summary()
	return res
}

// check — все ли ожидаемые подстроки есть в ответе. Регистр и неразрывные
// пробелы в числах не важны: «21 135» и «21135» — один и тот же ответ.
func check(answer string, expect []string) (bool, []string) {
	norm := func(s string) string {
		s = strings.ToLower(s)
		for _, sp := range []string{" ", " ", " "} {
			s = strings.ReplaceAll(s, sp, "")
		}
		return s
	}
	a := norm(answer)
	var missing []string
	for _, e := range expect {
		if !strings.Contains(a, norm(e)) {
			missing = append(missing, e)
		}
	}
	return len(missing) == 0, missing
}
