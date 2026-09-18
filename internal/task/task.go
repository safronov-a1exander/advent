// Package task — состояние задачи как конечный автомат (день 13).
//
// До сих пор разговор был плоским: пользователь спрашивает, агент отвечает,
// и единственное, что их связывало, — история и память. Ничто не говорило,
// **где мы находимся**: собираем требования, пишем код или проверяем
// сделанное. Из-за этого агент с одинаковой готовностью пишет реализацию
// на второй реплике и переспрашивает про требования на десятой.
//
// Здесь появляется задача с явным состоянием:
//
//	planning → execution → validation → done
//
// Четыре стадии взяты не с потолка: курс прямо советовал эти четыре не
// трогать, а расширять уже внутри них. Состояние — обычные данные, они
// лежат в файле, уходят в промпт блоком и переживают что угодно: закрытое
// приложение, кончившиеся токены, вернувшегося через сутки пользователя.
//
// Что это даёт, кроме порядка: **паузу и продолжение**. Разговор можно
// оборвать на любой стадии, а вернувшись — не объяснять заново, чего мы
// хотели. Агент читает состояние и продолжает с того же места. Это и есть
// разница между «ассистентом, которому каждый раз рассказывают с нуля»
// и агентом, который ведёт задачу.
//
// Проверка допустимости переходов здесь пока самая мягкая: линейный
// happy path вперёд. Жёсткие правила, запрет прыжков и откат назад —
// день 15; там же и красный путь, когда модель или пользователь пытаются
// сойти с маршрута.
package task

import (
	"fmt"
	"strings"
	"time"
)

// State — стадия задачи.
type State string

const (
	// StatePlanning — собираем требования и договариваемся о плане.
	StatePlanning State = "planning"
	// StateExecution — делаем по утверждённому плану.
	StateExecution State = "execution"
	// StateValidation — проверяем сделанное.
	StateValidation State = "validation"
	// StateDone — задача закрыта.
	StateDone State = "done"
)

// States — порядок стадий.
var States = []State{StatePlanning, StateExecution, StateValidation, StateDone}

// Valid — известна ли стадия.
func (s State) Valid() bool {
	for _, v := range States {
		if v == s {
			return true
		}
	}
	return false
}

// Label — имя стадии по-русски.
func (s State) Label() string {
	switch s {
	case StatePlanning:
		return "планирование"
	case StateExecution:
		return "выполнение"
	case StateValidation:
		return "проверка"
	case StateDone:
		return "готово"
	}
	return string(s)
}

// Goal — что вообще делают на этой стадии. Уходит в промпт: без этого
// модель понимает «execution» как угодно.
func (s State) Goal() string {
	switch s {
	case StatePlanning:
		return "собрать требования и согласовать план. Не начинай реализацию, пока план не утверждён пользователем."
	case StateExecution:
		return "выполнять утверждённый план по шагам. Держись плана; если он оказался неверным — скажи об этом, а не меняй молча."
	case StateValidation:
		return "проверить сделанное против плана и требований. Ищи расхождения, а не подтверждения."
	case StateDone:
		return "задача закрыта. Подведи итог; новую работу начинай только по явной просьбе."
	}
	return ""
}

// Event — запись в журнале задачи: что произошло и когда.
type Event struct {
	At   time.Time `json:"at"`
	From State     `json:"from,omitempty"`
	To   State     `json:"to,omitempty"`
	// Note — человеческое пояснение: почему перешли или что сделали.
	Note string `json:"note,omitempty"`
}

// Task — состояние одной задачи.
//
// Поля ровно те, что просит задание: этап (State), текущий шаг (Step,
// Current) и ожидаемое действие (Expect). Plan и Done добавлены к ним,
// потому что без них «шаг 3 из 7» ничего не значит.
type Task struct {
	ID    string `json:"id"`
	Title string `json:"title"`

	State State `json:"state"`
	// Step — какой шаг плана идёт сейчас, 1-based; 0 — ещё не начали.
	Step int `json:"step"`
	// Plan — утверждённый план. Total — это len(Plan).
	Plan []string `json:"plan,omitempty"`
	// Done — что уже сделано, человеческими словами.
	Done []string `json:"done,omitempty"`
	// Current — что делаем прямо сейчас.
	Current string `json:"current,omitempty"`
	// Expect — ожидаемое действие: чего задача ждёт, чтобы двинуться.
	// Обычно это то, что должен сделать или подтвердить пользователь.
	Expect string `json:"expect,omitempty"`

	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	// Log — журнал переходов. Нужен не для отладки: вернувшись через сутки,
	// пользователь первым делом спрашивает «на чём мы остановились».
	Log []Event `json:"log,omitempty"`
}

// New — новая задача в планировании.
func New(id, title string) *Task {
	now := time.Now()
	t := &Task{ID: id, Title: title, State: StatePlanning, Created: now, Updated: now}
	t.Log = append(t.Log, Event{At: now, To: StatePlanning, Note: "задача заведена"})
	return t
}

// Total — сколько шагов в плане.
func (t *Task) Total() int {
	if t == nil {
		return 0
	}
	return len(t.Plan)
}

// Finished — задача закрыта.
func (t *Task) Finished() bool { return t != nil && t.State == StateDone }

// Next — следующая стадия по happy path; для последней возвращает её же.
//
// День 13 знает только дорогу вперёд. Карта допустимых переходов с откатами
// и запретами — день 15; пока «следующая» и «допустимая» — одно и то же.
func Next(s State) State {
	for i, v := range States {
		if v == s && i+1 < len(States) {
			return States[i+1]
		}
	}
	return s
}

// Advance переводит задачу в стадию to. Note попадает в журнал.
func (t *Task) Advance(to State, note string) error {
	if t == nil {
		return fmt.Errorf("задачи нет")
	}
	if !to.Valid() {
		return fmt.Errorf("неизвестная стадия %q", to)
	}
	if to == t.State {
		return nil
	}
	from := t.State
	t.State = to
	t.touch()
	t.Log = append(t.Log, Event{At: t.Updated, From: from, To: to, Note: note})
	return nil
}

// Complete отмечает шаг сделанным и двигает счётчик.
func (t *Task) Complete(what string) {
	if t == nil {
		return
	}
	if what = strings.TrimSpace(what); what != "" {
		t.Done = append(t.Done, what)
	}
	if t.Step < t.Total() {
		t.Step++
	}
	t.touch()
}

// SetPlan кладёт утверждённый план и ставит задачу на первый шаг.
func (t *Task) SetPlan(plan []string) {
	if t == nil {
		return
	}
	var clean []string
	for _, s := range plan {
		if s = strings.TrimSpace(s); s != "" {
			clean = append(clean, s)
		}
	}
	t.Plan = clean
	if t.Step == 0 && len(clean) > 0 {
		t.Step = 1
	}
	t.touch()
	t.Log = append(t.Log, Event{At: t.Updated, Note: fmt.Sprintf("план утверждён: %d шагов", len(clean))})
}

func (t *Task) touch() { t.Updated = time.Now() }

// Block — состояние задачи для системного промпта.
//
// Формат взят со слайда лекции почти дословно: помеченные блоки [STATE],
// [PLAN], [DONE] и правила в конце. Помеченные блоки модель читает лучше
// связного абзаца — и, что важнее, их видно глазами в журнале запросов,
// когда разбираешься, почему агент повёл себя не так.
func (t *Task) Block() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Текущая задача и её состояние — работай в этих рамках:\n")
	fmt.Fprintf(&b, "[ЗАДАЧА]  %s\n", t.Title)
	fmt.Fprintf(&b, "[СТАДИЯ]  %s (%s)", t.State, t.State.Label())
	if n := t.Total(); n > 0 {
		fmt.Fprintf(&b, ", шаг %d из %d", t.Step, n)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "[ЦЕЛЬ]    %s\n", t.State.Goal())
	if t.Current != "" {
		fmt.Fprintf(&b, "[СЕЙЧАС]  %s\n", t.Current)
	}
	if len(t.Plan) > 0 {
		b.WriteString("[ПЛАН]\n")
		for i, s := range t.Plan {
			mark := " "
			if i+1 < t.Step {
				mark = "✓"
			} else if i+1 == t.Step {
				mark = "→"
			}
			fmt.Fprintf(&b, "  %s %d. %s\n", mark, i+1, s)
		}
	}
	if len(t.Done) > 0 {
		b.WriteString("[СДЕЛАНО]\n")
		for _, s := range t.Done {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
	}
	if t.Expect != "" {
		fmt.Fprintf(&b, "[ЖДЁМ]    %s\n", t.Expect)
	}
	b.WriteString("\nПравила:\n")
	b.WriteString("- работай только в рамках текущей стадии и текущего шага;\n")
	b.WriteString("- не перепрыгивай стадии и не делай работу следующей стадии заранее;\n")
	b.WriteString("- если стадия или шаг закончены — скажи об этом прямо в ответе.")
	return b.String()
}

// Summary — однострочная сводка для шапки и списков.
func (t *Task) Summary() string {
	if t == nil {
		return ""
	}
	s := string(t.State)
	if n := t.Total(); n > 0 {
		s += fmt.Sprintf(" %d/%d", t.Step, n)
	}
	if t.Current != "" {
		s += " · " + t.Current
	}
	return s
}

// Resume — строка «на чём остановились» для возвращения к задаче.
// Это не украшение: пауза и продолжение — то, ради чего состояние
// вообще выделено в отдельную сущность.
func (t *Task) Resume() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Задача «%s» — стадия %s", t.Title, t.State.Label())
	if n := t.Total(); n > 0 {
		fmt.Fprintf(&b, ", шаг %d из %d", t.Step, n)
	}
	b.WriteString(".")
	if t.Current != "" {
		b.WriteString(" Сейчас: " + t.Current + ".")
	}
	if t.Expect != "" {
		b.WriteString(" Ждём: " + t.Expect + ".")
	}
	if len(t.Done) > 0 {
		b.WriteString(fmt.Sprintf(" Сделано пунктов: %d.", len(t.Done)))
	}
	return b.String()
}

// Clone — глубокая копия: снимок для сохранения и для показа.
func (t *Task) Clone() *Task {
	if t == nil {
		return nil
	}
	out := *t
	out.Plan = append([]string(nil), t.Plan...)
	out.Done = append([]string(nil), t.Done...)
	out.Log = append([]Event(nil), t.Log...)
	return &out
}
