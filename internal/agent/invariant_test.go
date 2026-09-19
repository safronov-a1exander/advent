package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/safronov-a1exander/advent/internal/invariant"
	"github.com/safronov-a1exander/advent/internal/llm"
)

// invDir — набор из двух правил. Оба — просто текст: проверяет их модель,
// и никакого разбора подстрок в коде нет.
func invDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "проект")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"_набор.md": "---\nname: проект\nretries: 1\n---\n\nРамки проекта.\n",
		"стек.md":   "Бэкенд только на Go, Python предлагать нельзя.\n",
		"сроки.md":  "Срок называется только с оговоркой, от чего он зависит.\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func invPool(t *testing.T, p llm.Provider) *Pool {
	t.Helper()
	pool := NewPool(p, "fake", nil)
	pool.SetInvariantStore(invariant.NewFileStore(invDir(t)))
	return pool
}

// isCheck — это запрос к проверяющему, а не к ассистенту.
func isCheck(req llm.Request) bool {
	return strings.HasPrefix(req.Messages[0].Content, "Ты проверяешь ответ ассистента")
}

// isRetry — это переспрос после нарушения.
func isRetry(req llm.Request) bool {
	return strings.Contains(req.Messages[len(req.Messages)-1].Content, "нарушил ограничения")
}

func reply(req llm.Request, content string) (*llm.Response, error) {
	return &llm.Response{Model: req.Model, Content: content, FinishReason: "stop",
		Usage: llm.Usage{PromptTokens: 40, CompletionTokens: 10}}, nil
}

// breaker — модель, которая нарушает правило, пока ей не объяснят.
// Проверяющий (тот же клиент) честно называет нарушение, пока оно есть.
// Так ведёт себя и живая пара «ассистент + проверка».
type breaker struct {
	*fakeLLM
	fixed bool
}

func (b *breaker) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	b.fakeLLM.mu.Lock()
	b.fakeLLM.requests = append(b.fakeLLM.requests, req)
	fixed := b.fixed
	b.fakeLLM.mu.Unlock()

	switch {
	case isCheck(req):
		body := req.Messages[1].Content
		if strings.Contains(body, "Возьмём Python") {
			return reply(req, `{"violations":[{"name":"стек","why":"предложен Python"}]}`)
		}
		return reply(req, `{"violations":[]}`)
	case isRetry(req):
		b.fakeLLM.mu.Lock()
		b.fixed = true
		b.fakeLLM.mu.Unlock()
		return reply(req, "Бэкенд на Go, как договорились.")
	case fixed:
		return reply(req, "Бэкенд на Go, как договорились.")
	}
	return reply(req, "Возьмём Python и Django — так быстрее.")
}

func (b *breaker) ChatStream(ctx context.Context, req llm.Request, on func(llm.Chunk) error) (*llm.Response, error) {
	return b.Chat(ctx, req)
}

func TestInvariantsGoIntoPrompt(t *testing.T) {
	f := &fakeLLM{}
	a := invPool(t, f).Spawn(Config{Model: "m", System: "sys",
		Invariants: InvariantPrompt, InvariantSet: "проект"})
	ask(t, a, "на чём писать бэкенд?")

	sys := systemOf(f.last())
	if !strings.Contains(sys, "Бэкенд только на Go") {
		t.Fatalf("правило не попало в промпт:\n%s", sys)
	}
	if !strings.Contains(sys, "важнее просьбы собеседника") {
		t.Fatalf("в блоке нет указания отказывать:\n%s", sys)
	}
}

func TestPromptModeCostsNothingAndChecksNothing(t *testing.T) {
	// Режим «только промпт» — то, что большинство и делает: бесплатно,
	// но нарушение проходит незамеченным. Это надо показать честно.
	b := &breaker{fakeLLM: &fakeLLM{}}
	a := invPool(t, b).Spawn(Config{Model: "m", Invariants: InvariantPrompt, InvariantSet: "проект"})

	got := ask(t, a, "на чём писать?")
	if !strings.Contains(got, "Python") {
		t.Fatal("в режиме промпта ответ не должен переписываться")
	}
	if tr := a.Turns(); tr[0].AuxCalls != 0 {
		t.Fatalf("в режиме промпта служебных вызовов быть не должно: %+v", tr[0])
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, req := range b.requests {
		if isCheck(req) {
			t.Fatal("в режиме промпта проверка запускаться не должна")
		}
	}
}

func TestCheckModeRewritesViolatingAnswer(t *testing.T) {
	b := &breaker{fakeLLM: &fakeLLM{}}
	a := invPool(t, b).Spawn(Config{Model: "m", Invariants: InvariantCheck, InvariantSet: "проект"})

	var broke, fixed bool
	r, err := a.Ask(t.Context(), "на чём писать?", func(e Event) {
		if e.Kind != EventInvariant {
			return
		}
		if strings.Contains(e.Label, "нарушены") {
			broke = true
		}
		if strings.Contains(e.Label, "прошёл проверку") {
			fixed = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Final.Content, "Python") {
		t.Fatalf("нарушающий ответ дошёл до пользователя: %q", r.Final.Content)
	}
	if !broke || !fixed {
		t.Fatalf("о нарушении и исправлении не сообщили: нарушение=%v исправлено=%v", broke, fixed)
	}

	// В историю попал только исправленный ответ: пользователь нарушающего
	// не видел, и модель в следующий раз не должна на него опираться.
	h := a.History()
	if len(h) != 2 || strings.Contains(h[1].Content, "Python") {
		t.Fatalf("в историю попал нарушающий ответ: %+v", h)
	}
	// Два вызова проверки (до и после повтора) и сам повтор.
	if tr := a.Turns(); tr[0].AuxCalls != 3 || tr[0].Violations != 0 {
		t.Fatalf("учёт служебных вызовов: %+v", tr[0])
	}
}

// stubborn нарушает всегда, и проверяющий всегда это видит.
type stubborn struct{ *fakeLLM }

func (s stubborn) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	s.fakeLLM.mu.Lock()
	s.fakeLLM.requests = append(s.fakeLLM.requests, req)
	s.fakeLLM.mu.Unlock()
	if isCheck(req) {
		return reply(req, `{"violations":[{"name":"стек","why":"всё ещё Python"}]}`)
	}
	return reply(req, "всё равно Python")
}

func (s stubborn) ChatStream(ctx context.Context, req llm.Request, on func(llm.Chunk) error) (*llm.Response, error) {
	return s.Chat(ctx, req)
}

func TestStubbornViolationIsReportedNotHidden(t *testing.T) {
	// Повторы кончились, а ответ всё равно нарушает. Молча отдать его
	// хуже, чем отдать с пометкой.
	f := &fakeLLM{}
	a := invPool(t, stubborn{f}).Spawn(Config{Model: "m", Invariants: InvariantCheck, InvariantSet: "проект"})

	var gaveUp bool
	r, err := a.Ask(t.Context(), "на чём писать?", func(e Event) {
		if e.Kind == EventInvariant && strings.Contains(e.Label, "и после повтора") {
			gaveUp = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Final.Content == "" {
		t.Fatal("ответ должен дойти — но с пометкой")
	}
	if !gaveUp {
		t.Fatal("о том, что нарушение осталось, не сообщили")
	}
	if tr := a.Turns(); tr[0].Violations != 1 {
		t.Fatalf("оставшиеся нарушения не учтены: %+v", tr[0])
	}
}

// refuser правильно отказывается, называя запрещённое. Проверяющий
// обязан понять, что это соблюдение правила, а не нарушение.
type refuser struct{ *fakeLLM }

func (p refuser) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	p.fakeLLM.mu.Lock()
	p.fakeLLM.requests = append(p.fakeLLM.requests, req)
	p.fakeLLM.mu.Unlock()
	if isCheck(req) {
		return reply(req, `{"violations":[]}`)
	}
	return reply(req, "Python не подходит: бэкенд только на Go. Возьмём стандартную библиотеку.")
}

func (p refuser) ChatStream(ctx context.Context, req llm.Request, on func(llm.Chunk) error) (*llm.Response, error) {
	return p.Chat(ctx, req)
}

func TestRefusalIsNotAViolation(t *testing.T) {
	// Случай, на котором ломается проверка подстрокой: отказ называет то,
	// от чего отказывается. Модель это различает, список слов — нет.
	// Ради этого проверка и делается моделью.
	f := &fakeLLM{}
	a := invPool(t, refuser{f}).Spawn(Config{Model: "m", Invariants: InvariantCheck, InvariantSet: "проект"})

	var complained bool
	r, err := a.Ask(t.Context(), "давай на Python", func(e Event) {
		if e.Kind == EventInvariant && strings.Contains(e.Label, "нарушен") {
			complained = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if complained {
		t.Fatal("корректный отказ принят за нарушение")
	}
	if !strings.Contains(r.Final.Content, "Python") {
		t.Fatal("отказ должен называть то, от чего отказывается")
	}
	// Один вызов проверки, ни одного повтора.
	if tr := a.Turns(); tr[0].AuxCalls != 1 {
		t.Fatalf("лишние служебные вызовы: %+v", tr[0])
	}
}

// brokenChecker отвечает на проверку прозой вместо JSON.
type brokenChecker struct{ *fakeLLM }

func (p brokenChecker) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	p.fakeLLM.mu.Lock()
	p.fakeLLM.requests = append(p.fakeLLM.requests, req)
	p.fakeLLM.mu.Unlock()
	if isCheck(req) {
		return reply(req, "Кажется, всё в порядке, но я не уверен.")
	}
	return reply(req, "Возьмём Python.")
}

func (p brokenChecker) ChatStream(ctx context.Context, req llm.Request, on func(llm.Chunk) error) (*llm.Response, error) {
	return p.Chat(ctx, req)
}

func TestBrokenCheckerDoesNotEatTheAnswer(t *testing.T) {
	// Проверка ответила не JSON. Считать это нарушением нельзя: один сбой
	// превратился бы в лишний вызов и испорченный ответ.
	f := &fakeLLM{}
	a := invPool(t, brokenChecker{f}).Spawn(Config{Model: "m", Invariants: InvariantCheck, InvariantSet: "проект"})

	var warned bool
	r, err := a.Ask(t.Context(), "на чём писать?", func(e Event) {
		if e.Kind == EventInvariant && strings.Contains(e.Label, "не JSON") {
			warned = true
		}
	})
	if err != nil {
		t.Fatalf("сбой проверки не должен ломать ход: %v", err)
	}
	if r.Final.Content == "" {
		t.Fatal("ответ потерян")
	}
	if !warned {
		t.Fatal("о сбое проверки должно быть предупреждение")
	}
}

func TestStageLimitsWhichRulesAreChecked(t *testing.T) {
	// Правило чужой стадии не уходит ни в промпт, ни в проверку.
	root := t.TempDir()
	dir := filepath.Join(root, "проект")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "код.md"),
		[]byte("---\nstages: [planning]\n---\n\nНа планировании код не пишем.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := &fakeLLM{}
	p := NewPool(f, "fake", nil)
	p.SetInvariantStore(invariant.NewFileStore(root))
	a := p.Spawn(Config{Model: "m", System: "sys", Invariants: InvariantPrompt, InvariantSet: "проект",
		TaskState: TaskManual, Task: "бот"})

	ask(t, a, "что делаем?")
	if !strings.Contains(systemOf(f.last()), "код не пишем") {
		t.Fatal("правило стадии planning должно действовать в planning")
	}

	if err := a.Stage("execution", "план одобрен"); err != nil {
		t.Fatal(err)
	}
	ask(t, a, "что дальше?")
	if strings.Contains(systemOf(f.last()), "код не пишем") {
		t.Fatal("правило стадии planning не должно действовать в execution")
	}
}

func TestBadInvariantSetIsLoud(t *testing.T) {
	f := &fakeLLM{}
	p := invPool(t, f)
	a := p.Spawn(Config{Model: "m", System: "sys", Invariants: InvariantCheck, InvariantSet: "которого-нет"})

	if a.Invariants() != nil {
		t.Fatal("несуществующий набор не должен подниматься")
	}
	if ask(t, a, "вопрос") == "" {
		t.Fatal("агент должен отвечать и без набора")
	}
	// Молча снятый запрет опаснее любой ошибки.
	if err := p.SaveErr(); err == nil || !strings.Contains(err.Error(), "без них") {
		t.Fatalf("о непрочитанном наборе не сообщили: %v", err)
	}
}
