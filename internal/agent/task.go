package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/task"
)

// Задача агента (день 13).
//
// Третья вещь, которая подмешивается в системный промпт, — после профиля
// (день 12) и вместе с памятью (день 11). Порядок блоков теперь такой:
//
//	system → профиль → дорога → ЗАДАЧА → память → [сводка/факты] → история
//
// Задача стоит перед памятью, потому что задаёт рамку: память отвечает
// на «что известно», а задача — на «что мы сейчас вообще делаем».
// От запроса к запросу она меняется чаще профиля, но реже памяти.
//
// Продвигается задача служебным вызовом после ответа — как раскладка
// памяти дня 11, только та смотрит на реплику пользователя до ответа,
// а эта на пару «вопрос–ответ» после. Раньше нельзя: пока ответа нет,
// непонятно, закончилась ли стадия.

// Режимы задачи — поле Config.TaskState.
const (
	// TaskOff — задачи нет, всё как на двенадцатом дне.
	TaskOff = ""
	// TaskManual — состояние есть и уходит в промпт, но двигает его
	// пользователь. Служебных вызовов нет.
	TaskManual = "manual"
	// TaskAuto — то же плюс служебный вызов после каждого ответа:
	// стадия, план и шаг двигаются сами.
	TaskAuto = "auto"
)

// TaskModes — порядок перебора в панели.
var TaskModes = []string{TaskOff, TaskManual, TaskAuto}

// TaskLabel — имя режима для показа.
func TaskLabel(mode string) string {
	switch mode {
	case TaskManual:
		return "стадии, двигает пользователь"
	case TaskAuto:
		return "стадии, двигает агент"
	}
	return "без стадий"
}

// TaskHint — пояснение под панелью.
func TaskHint(mode string) string {
	switch mode {
	case TaskManual:
		return "состояние задачи (стадия, шаг, план, ожидаемое действие) уходит в промпт; двигают его командами /stage и /step — служебных вызовов нет"
	case TaskAuto:
		return "то же плюс служебный вызов после каждого ответа: сам решает, закончилась ли стадия, и двигает шаг. Возвращает только изменения"
	}
	return "разговор без стадий, как на двенадцатом дне"
}

// Task — состояние задачи агента или nil.
func (a *Agent) Task() *task.Task {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.task
}

// SetTask подключает агенту задачу. Зовётся пулом при создании и подъёме.
func (a *Agent) SetTask(t *task.Task) {
	a.mu.Lock()
	a.task = t
	a.mu.Unlock()
}

// SaveTask сохраняет состояние задачи, если у пула есть хранилище.
func (a *Agent) SaveTask() error {
	a.mu.Lock()
	t, save := a.task, a.saveTask
	a.mu.Unlock()
	if t == nil || save == nil {
		return nil
	}
	return save(t)
}

// Stage переводит задачу в стадию руками. Проверка допустимости та же,
// что у служебного вызова: пользователю можно не больше, чем агенту.
func (a *Agent) Stage(to task.State, note string) error {
	a.mu.Lock()
	t := a.task
	a.mu.Unlock()
	if t == nil {
		return fmt.Errorf("у агента нет задачи: включи поле «задача» в настройках")
	}
	if !to.Valid() {
		return fmt.Errorf("неизвестная стадия %q", to)
	}
	if to == t.State {
		return fmt.Errorf("задача уже в стадии %s", to)
	}
	if !task.Allow(t.State, to) {
		return fmt.Errorf("из %s нельзя сразу в %s", t.State, to)
	}
	if err := t.Advance(to, note); err != nil {
		return err
	}
	a.taskChanged()
	return nil
}

// Step отмечает текущий шаг сделанным.
func (a *Agent) Step(what string) error {
	a.mu.Lock()
	t := a.task
	a.mu.Unlock()
	if t == nil {
		return fmt.Errorf("у агента нет задачи")
	}
	t.Complete(what)
	a.taskChanged()
	return nil
}

// taskChanged сохраняет задачу и помечает агента изменённым.
func (a *Agent) taskChanged() {
	_ = a.SaveTask()
	a.mu.Lock()
	a.touch()
	a.mu.Unlock()
	a.changed()
}

// taskBlock — блок задачи для системного промпта.
func taskBlock(cfg Config, t *task.Task) string {
	if t == nil || cfg.TaskState == TaskOff {
		return ""
	}
	return t.Block()
}

// advanceTask — служебный вызов продвижения после ответа.
//
// Ошибка не ломает ход: ответ пользователь уже получил, а состояние
// останется прежним и сдвинется на следующем ходу. Терять из-за этого
// ответ было бы обменом худшим из возможных.
func (a *Agent) advanceTask(ctx context.Context, cfg Config, t *task.Task, question, answer string, gen uint64, turn *Turn, on func(Event)) {
	if t == nil || cfg.TaskState != TaskAuto || strings.TrimSpace(answer) == "" {
		return
	}
	req := llm.Request{
		Model: cfg.Model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: task.UpdateSystem},
			{Role: llm.RoleUser, Content: task.UpdatePrompt(t, question, answer)},
		},
		Temperature:    llm.F(0),
		Thinking:       &llm.Thinking{Type: "disabled"},
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}
	start := time.Now()
	resp, err := a.client.Chat(ctx, req)
	a.record(req, resp, err, "продвижение задачи", true)
	a.accountAux(resp, err)

	var u task.Update
	if err == nil {
		turn.AuxCalls++
		turn.AuxPrompt += resp.Usage.PromptTokens
		turn.AuxCompletion += resp.Usage.CompletionTokens
		u, err = task.ParseUpdate(resp.Content)
	}
	if err != nil {
		on(Event{Kind: EventContext, Label: "продвижение задачи не удалось — состояние осталось прежним", Content: err.Error()})
		return
	}

	// Разговор сбросили, пока вызов был в полёте. Задача сброс переживает
	// (она не про переписку), но продвигать её по ходу, которого больше
	// нет в истории, неправильно.
	a.mu.Lock()
	stale := a.gen != gen
	a.mu.Unlock()
	if stale {
		return
	}

	changes := t.Apply(u)
	if changes == "" {
		on(Event{Kind: EventContext, Label: "задача: без изменений",
			Usage: resp.Usage, CostUSD: resp.CostUSD, Latency: time.Since(start)})
		return
	}
	on(Event{Kind: EventContext, Label: "задача: " + t.Summary(), Content: changes,
		Usage: resp.Usage, CostUSD: resp.CostUSD, Latency: time.Since(start)})
	a.taskChanged()
}
