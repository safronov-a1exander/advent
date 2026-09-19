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
	"github.com/safronov-a1exander/advent/internal/memory"
)

// Scenario — описание прогона.
type Scenario struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	Defaults    agent.Config `yaml:"defaults"`
	Variants    []Variant    `yaml:"variants"`
	Dialog      []Line       `yaml:"dialog"`
}

// Variant — конфиг агента и то, пользуется ли вариант ветками разговора.
type Variant struct {
	agent.Config `yaml:",inline"`
	// Branches — выполнять команды веток сценария. Вариант без веток
	// проходит те же реплики одной лентой: так видно, что ветки дают.
	Branches bool `yaml:"branches"`
}

// Line — одна реплика пользователя и, если нужно, проверка ответа.
// Вместо реплики строка может быть командой веток (день 10):
//
//   - checkpoint: развилка          # снять чекпойнт
//   - branch: быстрый MVP           # новая ветка от чекпойнта from
//     from: развилка
//   - switch: быстрый MVP           # вернуться в ветку
//
// И командой памяти (день 11):
//
//   - newchat: заказчик вернулся   # стереть разговор, память задачи оставить
//   - remember: user/стек = Go     # положить руками в слой
type Line struct {
	Say string `yaml:"say"`

	Checkpoint string `yaml:"checkpoint"`
	Branch     string `yaml:"branch"`
	From       string `yaml:"from"`
	Switch     string `yaml:"switch"`

	// NewChat — оборвать разговор и начать новый тем же агентом. Слои
	// задачи и пользователя переживают это, история и краткосрочный слой —
	// нет. Ровно тот случай, ради которого слои и заведены.
	NewChat string `yaml:"newchat"`
	// Remember — положить запись руками: "user/ключ = значение".
	Remember string `yaml:"remember"`

	// Expect — подстроки, которые обязаны быть в ответе (без учёта регистра).
	// Реплики с проверками — это вопросы на память о раннем разговоре.
	Expect []string `yaml:"expect"`
	// Forbid — подстроки, которых в ответе быть не должно.
	//
	// Нужен ровно там, где проверяется краткосрочный слой: он обязан
	// умереть вместе с разговором, и увидеть это можно только проверкой
	// наоборот — «этого агент помнить уже не должен».
	Forbid []string `yaml:"forbid"`
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
		commands := 0
		for _, c := range []string{l.Checkpoint, l.Branch, l.Switch, l.NewChat, l.Remember} {
			if strings.TrimSpace(c) != "" {
				commands++
			}
		}
		switch {
		case commands > 1 || (commands == 1 && l.Say != ""):
			return nil, fmt.Errorf("%s: строка %d — одна строка это либо реплика, либо одна команда веток", path, i+1)
		case commands == 0 && strings.TrimSpace(l.Say) == "":
			return nil, fmt.Errorf("%s: реплика %d пустая", path, i+1)
		case l.Branch != "" && l.From == "":
			return nil, fmt.Errorf("%s: строка %d — у branch нужен from (имя чекпойнта)", path, i+1)
		case l.Remember != "" && !strings.Contains(l.Remember, "/"):
			return nil, fmt.Errorf("%s: строка %d — remember пишется как \"user/ключ = значение\"", path, i+1)
		}
	}
	return &s, nil
}

// Checked — есть ли у строки проверки.
func (l Line) Checked() bool { return len(l.Expect) > 0 || len(l.Forbid) > 0 }

// Command — команда веток строки или пусто, если это реплика.
func (l Line) Command() string {
	switch {
	case l.Checkpoint != "":
		return "чекпойнт «" + l.Checkpoint + "»"
	case l.Branch != "":
		return "ветка «" + l.Branch + "» от «" + l.From + "»"
	case l.Switch != "":
		return "в ветку «" + l.Switch + "»"
	case l.NewChat != "":
		return "новый разговор: " + l.NewChat
	case l.Remember != "":
		return "запомнить: " + l.Remember
	}
	return ""
}

// Step — результат одного хода варианта.
type Step struct {
	Say    string
	Answer string
	Err    string
	// Command — строка была командой веток: что сделано или почему пропущено.
	Command string
	// Branch — в какой ветке шёл ход.
	Branch string
	Turn   agent.Turn
	// Checked — у реплики были проверки; Passed — все подстроки нашлись.
	Checked bool
	Passed  bool
	Missing []string
	Cost    float64
}

// Result — прогон одного варианта.
type Result struct {
	Variant  agent.Config
	Branches bool // вариант выполнял команды веток
	AgentID  string
	Steps    []Step
	Elapsed  time.Duration
	Summary  string       // сводка к концу разговора, если была
	Facts    []agent.Fact // факты к концу разговора (sticky facts)
	// Layers — слои памяти к концу прогона (день 11). Прямой ответ на
	// вопрос задания «какие данные попадают в каждый слой».
	Layers map[memory.Scope][]memory.Entry
	// BranchList — ветки агента к концу прогона, если вариант ветвился.
	BranchList []agent.BranchInfo
}

// Totals — итоги варианта.
type Totals struct {
	Prompt, Cached, Completion int
	AuxPrompt, AuxOut          int
	AuxCalls                   int
	Checks, Passed             int
	Errors                     int
	Cost                       float64
}

// Input — все входные токены варианта, включая служебные вызовы
// (сжатие в сводку, обновление фактов).
func (t Totals) Input() int { return t.Prompt + t.AuxPrompt }

// Output — все выходные токены, включая сводки и блоки фактов.
func (t Totals) Output() int { return t.Completion + t.AuxOut }

// Totals считает итоги.
func (r Result) Totals() Totals {
	var t Totals
	for _, s := range r.Steps {
		t.Prompt += s.Turn.Prompt
		t.Cached += s.Turn.Cached
		t.Completion += s.Turn.Completion
		t.AuxPrompt += s.Turn.AuxPrompt
		t.AuxOut += s.Turn.AuxCompletion
		t.AuxCalls += s.Turn.AuxCalls
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
		cfg := agent.Overlay(s.Defaults, v.Config)
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
			results[i] = runVariant(ctx, pool, cfg, s.Variants[i].Branches, s.Dialog, progress)
		}()
	}
	wg.Wait()
	return results, nil
}

func runVariant(ctx context.Context, pool *agent.Pool, cfg agent.Config, branches bool, lines []Line, progress Progress) Result {
	a := pool.SpawnTemp(cfg)
	defer pool.Remove(a.ID())
	res := Result{Variant: cfg, Branches: branches, AgentID: a.ID()}
	start := time.Now()

	for i, l := range lines {
		if progress != nil {
			progress(cfg.Name, i+1, len(lines))
		}
		if cmd := l.Command(); cmd != "" {
			if l.NewChat != "" || l.Remember != "" {
				res.Steps = append(res.Steps, runMemoryCommand(a, l, cmd))
			} else {
				res.Steps = append(res.Steps, runCommand(a, l, cmd, branches))
			}
			continue
		}
		turnsBefore := len(a.Turns())
		reply, err := a.Ask(ctx, l.Say, nil)
		st := Step{Say: l.Say, Checked: l.Checked(), Branch: a.ActiveBranch()}
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
			st.Passed, st.Missing = check(st.Answer, l)
		}
		res.Steps = append(res.Steps, st)
	}
	res.Elapsed = time.Since(start)
	res.Summary, _ = a.Summary()
	res.Facts = a.Facts()
	if mem := a.Memory(); mem != nil {
		res.Layers = map[memory.Scope][]memory.Entry{}
		for _, sc := range memory.Scopes {
			if e := mem.Layer(sc).Entries(); len(e) > 0 {
				res.Layers[sc] = e
			}
		}
	}
	if br := a.Branches(); len(br) > 1 {
		res.BranchList = br
	}
	return res
}

// Brief — ячейка хода для таблиц: токены запроса, служебные вызовы, ветка
// и проверка. sent — добавить, сколько сообщений истории ушло с вопросом.
func (st Step) Brief(sent bool) string {
	switch {
	case st.Err != "":
		return "ошибка"
	case st.Command != "":
		switch {
		case strings.Contains(st.Command, "пропущено"):
			return "—"
		case strings.HasPrefix(st.Command, "новый разговор"):
			return "↺"
		case strings.HasPrefix(st.Command, "запомнить"):
			return "🧠"
		}
		return "→ " + st.Branch
	}
	c := fmt.Sprintf("%d", st.Turn.Prompt)
	if sent {
		c += fmt.Sprintf(" · %d сообщ.", st.Turn.Sent)
	}
	if st.Turn.AuxCalls > 0 {
		c += fmt.Sprintf(" · +служ. %d", st.Turn.AuxPrompt)
	}
	if st.Branch != "" && st.Branch != agent.MainBranch {
		c += " [" + st.Branch + "]"
	}
	if st.Checked {
		if st.Passed {
			c += " ✓"
		} else {
			c += " ✗"
		}
	}
	return c
}

// Text — реплика строки или её команда для таблиц.
func (l Line) Text() string {
	switch {
	case l.NewChat != "" || l.Remember != "":
		return "🧠 " + l.Command()
	case l.Command() != "":
		return "⎇ " + l.Command()
	}
	return strings.Join(strings.Fields(l.Say), " ")
}

// check — все ли ожидаемые подстроки есть в ответе. Регистр и неразрывные
// пробелы в числах не важны: «21 135» и «21135» — один и тот же ответ.
func check(answer string, l Line) (bool, []string) {
	norm := func(s string) string {
		s = strings.ToLower(s)
		for _, sp := range []string{" ", " ", " "} {
			s = strings.ReplaceAll(s, sp, "")
		}
		return s
	}
	a := norm(answer)
	var missing []string
	for _, e := range l.Expect {
		if !strings.Contains(a, norm(e)) {
			missing = append(missing, e)
		}
	}
	// Запрет проверяется так же буквально, как ожидание: годится
	// для «этого в ответе быть не должно», а не для тонких различий.
	for _, f := range l.Forbid {
		if strings.Contains(a, norm(f)) {
			missing = append(missing, "лишнее: "+f)
		}
	}
	return len(missing) == 0, missing
}

// runCommand выполняет команду веток. Вариант без веток её пропускает —
// и продолжает разговор той же лентой.
func runCommand(a *agent.Agent, l Line, cmd string, branches bool) Step {
	st := Step{Command: cmd, Branch: a.ActiveBranch()}
	if !branches {
		st.Command = cmd + " — пропущено: вариант без веток"
		return st
	}
	var err error
	switch {
	case l.Checkpoint != "":
		_, err = a.Checkpoint(l.Checkpoint)
	case l.Branch != "":
		_, err = a.Branch(l.Branch, l.From)
	case l.Switch != "":
		err = a.SwitchBranch(l.Switch)
	}
	if err != nil {
		st.Err = err.Error()
	}
	st.Branch = a.ActiveBranch()
	return st
}

// runMemoryCommand выполняет команды памяти (день 11). В отличие от команд
// веток их выполняют все варианты: newchat — это событие разговора, а не
// приём одного варианта, и вариант без памяти должен пройти через него тоже
// (и потерять всё — в этом и смысл сравнения).
func runMemoryCommand(a *agent.Agent, l Line, cmd string) Step {
	st := Step{Command: cmd, Branch: a.ActiveBranch()}
	switch {
	case l.NewChat != "":
		a.Reset()
	case l.Remember != "":
		lhs, value, _ := strings.Cut(l.Remember, "=")
		scope, key, _ := strings.Cut(lhs, "/")
		sc := memory.Scope(strings.ToLower(strings.TrimSpace(scope)))
		if !sc.Valid() {
			st.Err = fmt.Sprintf("неизвестный слой %q", scope)
			return st
		}
		if err := a.Remember(sc, strings.TrimSpace(key), strings.TrimSpace(value)); err != nil {
			// Вариант без памяти просто не может ничего запомнить — это не
			// сбой прогона, а его смысл.
			st.Command = cmd + " — пропущено: у варианта нет памяти"
		}
	}
	st.Branch = a.ActiveBranch()
	return st
}
