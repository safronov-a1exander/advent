package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/task"
)

// taskAnswer — «провайдер» продвижения задачи. Реплика пользователя вида
// «план ок» закрывает планирование, «сделал X» отмечает шаг, остальное
// не меняет ничего — как и на живом API, где обычный ход стадию не двигает.
func taskAnswer(msgs []llm.Message) (string, bool) {
	if !strings.HasPrefix(msgs[0].Content, "Ты следишь за состоянием рабочей задачи") {
		return "", false
	}
	_, q, _ := strings.Cut(msgs[1].Content, "Вопрос пользователя:\n")
	q, _, _ = strings.Cut(q, "\n\nОтвет ассистента:")
	q = strings.TrimSpace(q)

	u := map[string]any{}
	switch {
	case strings.HasPrefix(q, "план ок"):
		u["plan"] = []string{"схема", "хендлеры", "напоминания"}
		u["advance"] = "execution"
		u["current"] = "делаю схему"
		u["why"] = "план одобрен"
	case strings.HasPrefix(q, "сделал "):
		u["completed"] = strings.TrimPrefix(q, "сделал ")
	case strings.HasPrefix(q, "проверяй"):
		u["advance"] = "validation"
	case strings.HasPrefix(q, "закрывай"):
		u["advance"] = "done"
	case strings.HasPrefix(q, "сразу готово"):
		u["advance"] = "done" // попытка прыгнуть через стадию
	}
	b, _ := json.Marshal(u)
	return string(b), true
}

type taskProvider struct{ *fakeLLM }

func (p taskProvider) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if out, ok := taskAnswer(req.Messages); ok {
		p.fakeLLM.mu.Lock()
		p.fakeLLM.requests = append(p.fakeLLM.requests, req)
		p.fakeLLM.mu.Unlock()
		return &llm.Response{Model: req.Model, Content: out,
			Usage: llm.Usage{PromptTokens: 70, CompletionTokens: 12}}, nil
	}
	return p.fakeLLM.Chat(ctx, req)
}

func (p taskProvider) ChatStream(ctx context.Context, req llm.Request, on func(llm.Chunk) error) (*llm.Response, error) {
	return p.Chat(ctx, req)
}

func taskCfg() Config {
	return Config{Model: "m", System: "sys", TaskState: TaskAuto, Task: "бот барбершопа"}
}

func TestTaskBlockGoesIntoPrompt(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(f, "fake", nil).Spawn(Config{Model: "m", System: "sys", TaskState: TaskManual, Task: "бот"})
	ask(t, a, "с чего начнём?")

	sys := systemOf(f.last())
	for _, want := range []string{"[СТАДИЯ]  planning", "[ЦЕЛЬ]", "не перепрыгивай стадии"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("в промпте нет %q:\n%s", want, sys)
		}
	}
	// Ручной режим — без служебных вызовов.
	if tr := a.Turns(); tr[len(tr)-1].AuxCalls != 0 {
		t.Fatal("в ручном режиме продвижения быть не должно")
	}
}

func TestTaskAdvancesThroughStages(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(taskProvider{f}, "fake", nil).Spawn(taskCfg())

	if a.Task().State != task.StatePlanning {
		t.Fatal("новая задача должна начинаться с планирования")
	}
	ask(t, a, "обсудим план")
	if a.Task().State != task.StatePlanning {
		t.Fatal("обсуждение плана стадию не закрывает")
	}

	ask(t, a, "план ок, поехали")
	tk := a.Task()
	if tk.State != task.StateExecution {
		t.Fatalf("после одобрения плана: %s", tk.State)
	}
	if tk.Total() != 3 || tk.Step != 1 || tk.Current != "делаю схему" {
		t.Fatalf("план не лёг: %+v", tk)
	}

	ask(t, a, "сделал схему")
	if tk = a.Task(); tk.Step != 2 || len(tk.Done) != 1 {
		t.Fatalf("шаг не сдвинулся: шаг %d, сделано %v", tk.Step, tk.Done)
	}

	ask(t, a, "проверяй")
	if a.Task().State != task.StateValidation {
		t.Fatalf("не перешли в проверку: %s", a.Task().State)
	}
	ask(t, a, "закрывай")
	if !a.Task().Finished() {
		t.Fatalf("задача не закрылась: %s", a.Task().State)
	}
}

func TestTaskRejectsStageJump(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(taskProvider{f}, "fake", nil).Spawn(taskCfg())

	var label, content string
	if _, err := a.Ask(t.Context(), "сразу готово, закрывай", func(e Event) {
		if e.Kind == EventContext && strings.HasPrefix(e.Label, "задача") {
			label, content = e.Label, e.Content
		}
	}); err != nil {
		t.Fatal(err)
	}
	if a.Task().State != task.StatePlanning {
		t.Fatalf("прыжок planning → done прошёл: %s", a.Task().State)
	}
	if !strings.Contains(content, "отклонён") {
		t.Fatalf("об отклонении не сказано: %q / %q", label, content)
	}
}

func TestTaskUpdateFailureDoesNotEatAnswer(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(brokenTaskProvider{f}, "fake", nil).Spawn(taskCfg())

	var warned bool
	r, err := a.Ask(t.Context(), "план ок", func(e Event) {
		if e.Kind == EventContext && strings.Contains(e.Label, "не удалось") {
			warned = true
		}
	})
	if err != nil {
		t.Fatalf("сбой продвижения не должен ломать ход: %v", err)
	}
	if r.Final.Content == "" {
		t.Fatal("ответ потерян")
	}
	if !warned {
		t.Fatal("о сбое продвижения должно быть предупреждение")
	}
	if a.Task().State != task.StatePlanning {
		t.Fatal("состояние должно остаться прежним")
	}
}

type brokenTaskProvider struct{ *fakeLLM }

func (p brokenTaskProvider) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if strings.HasPrefix(req.Messages[0].Content, "Ты следишь за состоянием рабочей задачи") {
		return &llm.Response{Model: req.Model, Content: "задача движется отлично!"}, nil
	}
	return p.fakeLLM.Chat(ctx, req)
}

func (p brokenTaskProvider) ChatStream(ctx context.Context, req llm.Request, on func(llm.Chunk) error) (*llm.Response, error) {
	return p.Chat(ctx, req)
}

func TestTaskSurvivesResetAndRestart(t *testing.T) {
	// Главное свойство дня: пауза на любом этапе и продолжение без
	// повторных объяснений.
	sessions := t.TempDir()
	tasks := t.TempDir()
	f := &fakeLLM{}

	p := NewPool(taskProvider{f}, "fake", nil)
	p.SetStore(NewFileStore(sessions))
	p.SetTaskStore(task.NewFileStore(tasks))
	a := p.Spawn(taskCfg())
	ask(t, a, "план ок")
	ask(t, a, "сделал схему")

	// Сброс разговора задачу не трогает: стёртая переписка не значит,
	// что работа не сделана.
	a.Reset()
	if tk := a.Task(); tk == nil || tk.State != task.StateExecution || tk.Step != 2 {
		t.Fatalf("сброс уронил задачу: %+v", a.Task())
	}

	// Перезапуск: состояние поднимается из файла.
	p2 := NewPool(taskProvider{f}, "fake", nil)
	p2.SetStore(NewFileStore(sessions))
	p2.SetTaskStore(task.NewFileStore(tasks))
	restored, err := p2.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 {
		t.Fatalf("поднялось разговоров: %d", len(restored))
	}
	tk := restored[0].Task()
	if tk == nil || tk.State != task.StateExecution || tk.Step != 2 {
		t.Fatalf("задача не пережила перезапуск: %+v", tk)
	}
	if !strings.Contains(tk.Resume(), "шаг 2 из 3") {
		t.Fatalf("«на чём остановились» потерялось: %s", tk.Resume())
	}

	// И продолжение идёт без повторных объяснений: стадия и план уже
	// в промпте следующего запроса.
	ask(t, restored[0], "что дальше?")
	sys := systemOf(f.lastMain())
	if !strings.Contains(sys, "[СТАДИЯ]  execution") || !strings.Contains(sys, "✓ 1. схема") {
		t.Fatalf("после перезапуска промпт не знает, где мы:\n%s", sys)
	}
}

func TestStageByHandObeysSameRules(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(f, "fake", nil).Spawn(Config{Model: "m", TaskState: TaskManual, Task: "бот"})

	if err := a.Stage(task.StateDone, "хочу сразу"); err == nil {
		t.Fatal("пользователю можно не больше, чем агенту: прыжок должен быть запрещён")
	}
	if err := a.Stage(task.StateExecution, "план одобрен"); err != nil {
		t.Fatal(err)
	}
	if a.Task().State != task.StateExecution {
		t.Fatalf("стадия не сменилась: %s", a.Task().State)
	}
	if err := a.Stage("выдумано", ""); err == nil {
		t.Fatal("несуществующая стадия должна отвергаться")
	}
}

func TestTaskAndProfileAndMemoryOrder(t *testing.T) {
	// Порядок блоков: профиль → дорога → задача → память.
	// Задача — рамка для ответа, поэтому перед «что известно».
	f := &fakeLLM{}
	p := profilePool(t, f)
	p.SetTaskStore(task.NewFileStore(t.TempDir()))
	a := p.Spawn(Config{Model: "m", System: "sys", Profile: "сеньор",
		TaskState: TaskManual, Task: "бот",
		Memory: MemoryManual, User: "олег"})
	_ = a.Remember("user", "стек", "Go")
	ask(t, a, "как лучше сделать кэш?")

	sys := systemOf(f.lastMain())
	iProf := strings.Index(sys, "Профиль пользователя")
	iTask := strings.Index(sys, "[СТАДИЯ]")
	iMem := strings.Index(sys, "Что известно о собеседнике")
	if iProf < 0 || iTask < 0 || iMem < 0 {
		t.Fatalf("не все блоки на месте:\n%s", sys)
	}
	if !(iProf < iTask && iTask < iMem) {
		t.Fatalf("порядок сбит (профиль %d, задача %d, память %d)", iProf, iTask, iMem)
	}
}

func TestTaskAdvanceCountsInTurn(t *testing.T) {
	// Служебный вызов продвижения стоит токенов, и они должны попасть
	// в тот же ход, а не потеряться между ответом и записью хода.
	f := &fakeLLM{}
	a := NewPool(taskProvider{f}, "fake", nil).Spawn(taskCfg())
	ask(t, a, "план ок")

	tr := a.Turns()
	if len(tr) != 1 {
		t.Fatalf("ходов %d", len(tr))
	}
	if tr[0].AuxCalls != 1 {
		t.Fatalf("служебных вызовов в ходе %d, ожидали 1", tr[0].AuxCalls)
	}
	if tr[0].AuxPrompt == 0 || tr[0].AuxCompletion == 0 {
		t.Fatalf("токены продвижения потерялись: %+v", tr[0])
	}
}
