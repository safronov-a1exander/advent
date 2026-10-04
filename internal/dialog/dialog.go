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
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/mcp"
	"github.com/safronov-a1exander/advent/internal/memory"
	"github.com/safronov-a1exander/advent/internal/rag"
	"github.com/safronov-a1exander/advent/internal/task"
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
	// Stage — перевести задачу в стадию руками (день 13). Варианты без
	// задачи команду пропускают.
	Stage string `yaml:"stage"`

	// Expect — подстроки, которые обязаны быть в ответе (без учёта регистра).
	// Реплики с проверками — это вопросы на память о раннем разговоре.
	Expect []string `yaml:"expect"`
	// Forbid — подстроки, которых в ответе быть не должно (день 12).
	// Половина персонализации — это запреты: «никакого кода», «без
	// предисловий», — и проверить их можно только так.
	Forbid []string `yaml:"forbid"`
	// ExpectBy — проверки для конкретных вариантов по их имени (день 12).
	// У профилей ожидания разные по построению: джуниору код нужен,
	// продакту запрещён, и общей проверкой это не выразить.
	ExpectBy map[string]Check `yaml:"expect_by"`
	// Tools — какие инструменты агент обязан вызвать на этой реплике и
	// в каком порядке: «rates.convert» (день 17). Без сервера —
	// «convert» — подходит инструмент с таким именем на любом сервере:
	// так один сценарий гоняют на двух конфигурациях одного сервера. Между ними могут быть
	// другие вызовы — проверяется порядок, а не точное совпадение.
	// Проверка варианта без MCP — это его смысл: он её не проходит.
	Tools []string `yaml:"tools"`
	// Chains — несколько цепочек, каждая проверяется как Tools (день 20).
	// Нужны, когда у флоу несколько независимых веток: «курс → цель» и
	// «выписка → сводка → план» могут идти в любом порядке между собой,
	// но внутри каждой порядок обязателен.
	Chains [][]string `yaml:"chains"`
	// Passes — данные между инструментами разных серверов (день 20):
	// число из результата From должно оказаться в аргументах To. Сервер
	// «Цели» не может проверить, что сумма в рублях пришла из «Курсов», —
	// они ничего друг о друге не знают. Проверить стык может только тот,
	// кто видит оба вызова.
	Passes []Pass `yaml:"passes"`
	// NoTools — на этой реплике инструменты звать незачем. Лишний вызов —
	// это деньги и задержка, и модель, которая зовёт курс на «привет»,
	// так же неправа, как модель, которая курс придумывает.
	NoTools bool `yaml:"no_tools"`
	// UsesResult — ответ обязан опираться на результат инструмента: в нём
	// должно быть число из результата, которого не было в вопросе.
	// «Получите и используйте результат» из задания дня 17 — ровно это:
	// вызвать инструмент и ответить своими словами — не одно и то же.
	UsesResult bool `yaml:"uses_result"`
	// ToolExpect — что должно быть в результате инструмента (день 19):
	// «budget.summarize_transactions: совпали все». Проверяется последний
	// вызов этого инструмента на реплике. Так проверяется не ответ модели,
	// а то, что она передала между инструментами: сервер сам сверяет вход
	// со своей выдачей и пишет итог сверки в результат.
	ToolExpect map[string]string `yaml:"tool_expect"`
	// Sources — из каких файлов базы знаний должны прийти фрагменты
	// (день 22): «docs/days/day17.md». Проверяется поиск, а не ответ,
	// и только у вариантов с rag: варианту без базы искать негде,
	// его сравнивают по ответу.
	Sources []string `yaml:"sources"`
	// Note — зачем эта реплика: попадает в отчёт рядом с проверкой.
	Note string `yaml:"note"`
}

// Pass — стык двух инструментов: результат From → аргументы To.
type Pass struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// Check — что должно и чего не должно быть в ответе.
type Check struct {
	Expect []string `yaml:"expect"`
	Forbid []string `yaml:"forbid"`
}

// Empty — проверять нечего.
func (c Check) Empty() bool { return len(c.Expect) == 0 && len(c.Forbid) == 0 }

// checkFor — проверка этой строки для варианта с таким именем: общая
// плюс персональная. Общая действует на всех, персональная дополняет.
func (l Line) checkFor(variant string) Check {
	c := Check{Expect: append([]string(nil), l.Expect...), Forbid: append([]string(nil), l.Forbid...)}
	if own, ok := l.ExpectBy[variant]; ok {
		c.Expect = append(c.Expect, own.Expect...)
		c.Forbid = append(c.Forbid, own.Forbid...)
	}
	return c
}

// Checked — есть ли у строки проверки хоть для кого-нибудь.
func (l Line) Checked() bool {
	return len(l.Expect) > 0 || len(l.Forbid) > 0 || len(l.ExpectBy) > 0 || len(l.Tools) > 0 || l.NoTools || l.UsesResult || len(l.ToolExpect) > 0 || len(l.Chains) > 0 || len(l.Passes) > 0 || len(l.Sources) > 0
}

// Load читает сценарий.
func Load(path string) (*Scenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Scenario
	// Строгий разбор: неизвестное поле — ошибка, а не тишина. На одиннадцатом
	// дне сценарий полдня «проверял» ответ полем forbid, которого в структуре
	// ещё не было, и YAML молча его проглатывал. Проверка, которая ничего
	// не проверяет, хуже отсутствующей.
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
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
		for _, c := range []string{l.Checkpoint, l.Branch, l.Switch, l.NewChat, l.Remember, l.Stage} {
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
	case l.Stage != "":
		return "стадия: " + l.Stage
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
	// State — в какой стадии задачи шёл ход (день 13).
	State string
	// Violations — сколько инвариантов осталось нарушенными после
	// повторов (день 14).
	Violations int
	// Tools — вызовы инструментов на этом ходе по порядку (день 17).
	Tools []mcp.Outcome
	// Sources — фрагменты базы знаний, с которыми ушёл вопрос (день 22).
	Sources []rag.Hit
	Turn    agent.Turn
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
	// Task — состояние задачи к концу прогона (день 13).
	Task *task.Task
}

// Totals — итоги варианта.
type Totals struct {
	Prompt, Cached, Completion int
	AuxPrompt, AuxOut          int
	AuxCalls                   int
	Checks, Passed             int
	Errors                     int
	// Violations — сколько ходов ушло к пользователю с нарушенным
	// инвариантом (день 14).
	Violations int
	Cost       float64
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
		if s.Violations > 0 {
			t.Violations++
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
			if l.NewChat != "" || l.Remember != "" || l.Stage != "" {
				res.Steps = append(res.Steps, runMemoryCommand(a, l, cmd))
			} else {
				res.Steps = append(res.Steps, runCommand(a, l, cmd, branches))
			}
			continue
		}
		turnsBefore := len(a.Turns())
		reply, err := a.Ask(ctx, l.Say, nil)
		want := l.checkFor(cfg.Name)
		st := Step{Say: l.Say, Checked: !want.Empty() || len(l.Tools) > 0 || l.NoTools || l.UsesResult || len(l.ToolExpect) > 0 || len(l.Chains) > 0 || len(l.Passes) > 0 || (len(l.Sources) > 0 && cfg.RAG != ""), Branch: a.ActiveBranch(), State: stateOf(a)}
		if err != nil {
			st.Err = err.Error()
			res.Steps = append(res.Steps, st)
			continue
		}
		st.Answer = reply.Final.Content
		st.Tools = reply.Tools
		st.Sources = reply.Sources
		_, st.Cost = reply.Usage()
		if turns := a.Turns(); len(turns) > turnsBefore {
			st.Turn = turns[len(turns)-1]
			st.Violations = st.Turn.Violations
		}
		if st.Checked {
			st.Passed, st.Missing = check(st.Answer, want)
			if miss := checkTools(st.Tools, l.Tools, l.NoTools); len(miss) > 0 {
				st.Passed = false
				st.Missing = append(st.Missing, miss...)
			}
			for _, ch := range l.Chains {
				if miss := checkTools(st.Tools, ch, false); len(miss) > 0 {
					st.Passed = false
					st.Missing = append(st.Missing, miss...)
				}
			}
			for _, p := range l.Passes {
				if miss := checkPass(l.Say, st.Tools, p); miss != "" {
					st.Passed = false
					st.Missing = append(st.Missing, miss)
				}
			}
			for tool, want := range l.ToolExpect {
				if miss := checkToolResult(st.Tools, tool, want); miss != "" {
					st.Passed = false
					st.Missing = append(st.Missing, miss)
				}
			}
			if cfg.RAG != "" {
				if miss := checkSources(st.Sources, l.Sources); len(miss) > 0 {
					st.Passed = false
					st.Missing = append(st.Missing, miss...)
				}
			}
			if l.UsesResult && !usesResult(l.Say, st.Answer, st.Tools) {
				st.Passed = false
				st.Missing = append(st.Missing, "в ответе нет чисел из результата инструмента")
			}
		}
		res.Steps = append(res.Steps, st)
	}
	res.Elapsed = time.Since(start)
	res.Task = a.Task().Clone()
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

// stateOf — стадия задачи агента или пусто, если задачи нет.
func stateOf(a *agent.Agent) string {
	if t := a.Task(); t != nil {
		return string(t.State)
	}
	return ""
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
		case strings.HasPrefix(st.Command, "стадия"):
			return "◆ " + st.State
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
	if st.State != "" {
		c += " ◆" + st.State
	}
	if st.Violations > 0 {
		c += fmt.Sprintf(" ⛔%d", st.Violations)
	}
	if len(st.Tools) > 0 {
		c += " ▸ " + agent.ToolTrace(st.Tools)
	}
	if len(st.Sources) > 0 {
		c += fmt.Sprintf(" ⌕%d", len(st.Sources))
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
	case l.Stage != "":
		return "◆ " + l.Command()
	case l.NewChat != "" || l.Remember != "":
		return "🧠 " + l.Command()
	case l.Command() != "":
		return "⎇ " + l.Command()
	}
	return strings.Join(strings.Fields(l.Say), " ")
}

// decimalComma — запятая между цифрами.
var decimalComma = regexp.MustCompile(`(\d),(\d)`)

// check — все ли ожидаемые подстроки есть в ответе и нет ли запрещённых.
// Регистр и неразрывные пробелы в числах не важны: «21 135» и «21135» —
// один и тот же ответ.
func check(answer string, want Check) (bool, []string) {
	norm := func(s string) string {
		s = strings.ToLower(s)
		for _, sp := range []string{" ", " ", " "} {
			s = strings.ReplaceAll(s, sp, "")
		}
		// «0,0046» и «0.0046» — одно число: по-русски модель пишет
		// десятичную запятую, а заметки стенда — точку (день 22).
		return decimalComma.ReplaceAllString(s, "$1.$2")
	}
	a := norm(answer)
	var missing []string
	for _, e := range want.Expect {
		if !strings.Contains(a, norm(e)) {
			missing = append(missing, e)
		}
	}
	// Запреты проверяются так же буквально, как ожидания. Этого мало,
	// чтобы поймать «никакого кода» вообще, но достаточно, чтобы поймать
	// конкретные признаки — и разница между профилями становится фактом,
	// а не впечатлением от чтения ответов.
	for _, f := range want.Forbid {
		if strings.Contains(a, norm(f)) {
			missing = append(missing, "лишнее: "+f)
		}
	}
	return len(missing) == 0, missing
}

// checkTools — вызваны ли нужные инструменты в нужном порядке. Имя —
// «сервер.инструмент»; между ожидаемыми вызовами могут быть другие.
// Вызов, кончившийся ошибкой, не засчитывается: инструмент не отработал.
func checkTools(got []mcp.Outcome, want []string, none bool) []string {
	var missing []string
	if none && len(got) > 0 {
		missing = append(missing, "лишний вызов: "+agent.ToolTrace(got))
	}
	i := 0
	for _, w := range want {
		found := false
		for ; i < len(got); i++ {
			if !got[i].IsError && toolIs(got[i], w) {
				found, i = true, i+1
				break
			}
		}
		if !found {
			missing = append(missing, "не вызван: "+w)
			break
		}
	}
	return missing
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
	st := Step{Command: cmd, Branch: a.ActiveBranch(), State: stateOf(a)}
	switch {
	case l.NewChat != "":
		a.Reset()
	case l.Stage != "":
		to, note, _ := strings.Cut(l.Stage, " ")
		if err := a.Stage(task.State(strings.TrimSpace(to)), strings.TrimSpace(note)); err != nil {
			st.Command = cmd + " — " + err.Error()
		}
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
	// Стадия после команды, а не до: при отказе она та же, и в отчёте
	// сразу видно, что перейти не удалось.
	st.State = stateOf(a)
	return st
}

// checkSources — пришли ли фрагменты из всех нужных файлов (день 22).
func checkSources(got []rag.Hit, want []string) []string {
	var missing []string
	for _, w := range want {
		found := false
		for _, h := range got {
			if h.Source == w {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, "не найден источник: "+w)
		}
	}
	return missing
}
