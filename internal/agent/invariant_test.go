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

// invDir — набор из двух правил: у одного есть быстрый фильтр,
// у другого только текст, и его смотрит смысловая проверка.
func invDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "проект")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"_набор.md": `---
name: проект
retries: 1
---

Рамки проекта.
`,
		"стек.md": `---
forbid: [python, django]
unless: [нельзя, "не подходит"]
---

Бэкенд только на Go, Python предлагать нельзя.
`,
		// Правило без frontmatter: фильтра нет, смотрит смысловая проверка.
		"сроки.md": "Срок называется только с оговоркой, от чего он зависит.\n",
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

// breaker отвечает запрещённым словом, пока его не поправят: первый ответ
// нарушает правило, после объяснения — нет. Так ведёт себя и живая модель.
type breaker struct {
	*fakeLLM
	fixed bool
}

func (b *breaker) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	b.fakeLLM.mu.Lock()
	b.fakeLLM.requests = append(b.fakeLLM.requests, req)
	b.fakeLLM.mu.Unlock()

	last := req.Messages[len(req.Messages)-1].Content
	content := "Возьмём Python и Django — так быстрее."
	if strings.Contains(last, "нарушил ограничения") {
		b.fixed = true
		content = "Бэкенд на Go, как договорились."
	}
	return &llm.Response{Model: req.Model, Content: content, FinishReason: "stop",
		Usage: llm.Usage{PromptTokens: 40, CompletionTokens: 10}}, nil
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

func TestPromptModeDoesNotCheckAnswer(t *testing.T) {
	// Режим «только промпт» — то, что большинство и делает: дёшево,
	// но нарушение проходит незамеченным. Это надо показать честно.
	b := &breaker{fakeLLM: &fakeLLM{}}
	a := invPool(t, b).Spawn(Config{Model: "m", Invariants: InvariantPrompt, InvariantSet: "проект"})

	got := ask(t, a, "на чём писать?")
	if !strings.Contains(got, "Python") {
		t.Fatal("в режиме промпта ответ не должен переписываться")
	}
	if b.fixed {
		t.Fatal("повтора быть не должно")
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
	// Повтор — служебный вызов, и он считается.
	if tr := a.Turns(); tr[0].AuxCalls != 1 || tr[0].Violations != 0 {
		t.Fatalf("учёт повтора: %+v", tr[0])
	}
}

// stubborn нарушает всегда — даже после объяснения.
type stubborn struct{ *fakeLLM }

func (s stubborn) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	s.fakeLLM.mu.Lock()
	s.fakeLLM.requests = append(s.fakeLLM.requests, req)
	s.fakeLLM.mu.Unlock()
	return &llm.Response{Model: req.Model, Content: "всё равно Python", FinishReason: "stop",
		Usage: llm.Usage{PromptTokens: 40, CompletionTokens: 5}}, nil
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

// judgeProvider отвечает на обычный запрос нормально, а на проверку —
// нарушением правила «сроки».
type judgeProvider struct{ *fakeLLM }

func (p judgeProvider) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	p.fakeLLM.mu.Lock()
	p.fakeLLM.requests = append(p.fakeLLM.requests, req)
	p.fakeLLM.mu.Unlock()

	switch {
	case strings.HasPrefix(req.Messages[0].Content, "Ты проверяешь ответ ассистента"):
		// Придуманное правило в ответе тоже есть — проверяем, что отбрасываем.
		return &llm.Response{Model: req.Model,
			Content: `{"violations":[{"name":"сроки","why":"две недели без условий"},{"name":"выдумка","why":"мимо"}]}`,
			Usage:   llm.Usage{PromptTokens: 50, CompletionTokens: 15}}, nil
	case strings.Contains(req.Messages[len(req.Messages)-1].Content, "нарушил ограничения"):
		return &llm.Response{Model: req.Model, Content: "Две недели, если Calendar отдаёт слоты.",
			Usage: llm.Usage{PromptTokens: 40, CompletionTokens: 10}}, nil
	}
	return &llm.Response{Model: req.Model, Content: "Сделаем за две недели.",
		Usage: llm.Usage{PromptTokens: 40, CompletionTokens: 10}}, nil
}

func (p judgeProvider) ChatStream(ctx context.Context, req llm.Request, on func(llm.Chunk) error) (*llm.Response, error) {
	return p.Chat(ctx, req)
}

func TestJudgeCatchesWhatCodeCannot(t *testing.T) {
	f := &fakeLLM{}
	a := invPool(t, judgeProvider{f}).Spawn(Config{Model: "m", Invariants: InvariantJudge, InvariantSet: "проект"})

	var byJudge bool
	r, err := a.Ask(t.Context(), "когда будет готово?", func(e Event) {
		if e.Kind == EventInvariant && strings.Contains(e.Content, "смысловая проверка") {
			byJudge = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !byJudge {
		t.Fatal("нарушение, найденное внешней моделью, не помечено")
	}
	if !strings.Contains(r.Final.Content, "если") {
		t.Fatalf("ответ не переписан: %q", r.Final.Content)
	}
	// Дешёвых правил нарушено не было — значит, был и вызов проверки,
	// и вызов повтора.
	if tr := a.Turns(); tr[0].AuxCalls < 2 {
		t.Fatalf("служебных вызовов %d, ожидали хотя бы 2 (проверка + повтор)", tr[0].AuxCalls)
	}
}

func TestCheckModeSkipsJudge(t *testing.T) {
	// В режиме check внешняя проверка не запускается — за неё не платим.
	f := &fakeLLM{}
	a := invPool(t, judgeProvider{f}).Spawn(Config{Model: "m", Invariants: InvariantCheck, InvariantSet: "проект"})
	ask(t, a, "когда будет готово?")

	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		if strings.HasPrefix(req.Messages[0].Content, "Ты проверяешь ответ ассистента") {
			t.Fatal("в режиме check внешняя проверка запускаться не должна")
		}
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
