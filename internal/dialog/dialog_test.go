package dialog

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/llm"
)

// echoLLM — провайдер без памяти: на вопрос с «?» отвечает всем, что знает
// из запроса (system и реплики пользователя), на сжатие — репликами
// пользователя. Токены запроса — по символу на токен.
type echoLLM struct {
	mu    sync.Mutex
	calls int
}

func (e *echoLLM) Name() string                                 { return "echo" }
func (e *echoLLM) ListModels(context.Context) ([]string, error) { return nil, nil }
func (e *echoLLM) ChatStream(ctx context.Context, r llm.Request, _ func(llm.Chunk) error) (*llm.Response, error) {
	return e.Chat(ctx, r)
}

func (e *echoLLM) Chat(_ context.Context, r llm.Request) (*llm.Response, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	prompt := 0
	var known []string
	for _, m := range r.Messages {
		prompt += len([]rune(m.Content))
		if m.Role != llm.RoleAssistant {
			known = append(known, m.Content)
		}
	}
	content := "ок"
	switch {
	case strings.HasPrefix(r.Messages[0].Content, "Ты сжимаешь переписку"):
		content = r.Messages[len(r.Messages)-1].Content
	case strings.HasSuffix(r.Messages[len(r.Messages)-1].Content, "?"):
		content = strings.Join(known, " | ")
	}
	return &llm.Response{Model: r.Model, Content: content, FinishReason: "stop",
		Usage: llm.Usage{PromptTokens: prompt, CompletionTokens: len([]rune(content))}}, nil
}

func TestRunComparesVariants(t *testing.T) {
	s := &Scenario{
		Defaults: agent.Config{Tier: "weak", System: "ассистент"},
		Variants: []agent.Config{
			{Name: "полная"},
			{Name: "сжатие", Context: agent.ContextSummary, KeepLast: llm.I(2), SummarizeEvery: llm.I(2)},
		},
		Dialog: []Line{
			{Say: "меня зовут Саша"},
			{Say: "бюджет 60000"},
			{Say: "реплика три"},
			{Say: "реплика четыре"},
			{Say: "реплика пять"},
			{Say: "как меня зовут?", Expect: []string{"САША"}},
			{Say: "какой бюджет?", Expect: []string{"60 000"}},
		},
	}
	pool := agent.NewPool(&echoLLM{}, "echo", nil)
	var progressCalls int
	var mu sync.Mutex
	res, err := Run(context.Background(), pool, []llm.ModelInfo{{ID: "m", Tier: "weak"}}, s,
		func(string, int, int) { mu.Lock(); progressCalls++; mu.Unlock() })
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || progressCalls != 14 {
		t.Fatalf("результатов %d, вызовов прогресса %d", len(res), progressCalls)
	}
	if pool.Len() != 0 {
		t.Fatal("агенты прогона остались в пуле")
	}

	full, sum := res[0].Totals(), res[1].Totals()
	if full.Checks != 2 || full.Passed != 2 {
		t.Fatalf("полная история: проверок %d, пройдено %d", full.Checks, full.Passed)
	}
	// сжатие через сводку сохранило оба факта — проверки проходят и тут
	if sum.Passed != 2 {
		t.Fatalf("со сжатием пройдено %d из %d: %+v", sum.Passed, sum.Checks, res[1].Steps[5])
	}
	if sum.CompressCalls == 0 || full.CompressCalls != 0 {
		t.Fatalf("вызовы сжатия: полная %d, summary %d", full.CompressCalls, sum.CompressCalls)
	}
	// последний запрос со сжатием меньше, чем у полной истории
	lastFull := res[0].Steps[6].Turn
	lastSum := res[1].Steps[6].Turn
	if lastSum.Sent >= lastFull.Sent {
		t.Fatalf("со сжатием ушло %d сообщений истории, без — %d", lastSum.Sent, lastFull.Sent)
	}
	if res[1].Summary == "" {
		t.Fatal("к концу разговора у варианта со сжатием нет сводки")
	}
	// в итог сжатия входят и служебные вызовы
	if sum.Input() != sum.Prompt+sum.CompressPrompt || sum.CompressPrompt == 0 {
		t.Fatalf("входные токены сжатия не учтены: %+v", sum)
	}
}

func TestCheckIgnoresCaseAndSpacesInNumbers(t *testing.T) {
	ok, missing := check("Осталось 21 135 ₽, Саша", []string{"21135", "саша"})
	if !ok || len(missing) != 0 {
		t.Fatalf("проверка не прошла: %v", missing)
	}
	ok, missing = check("не помню", []string{"10000"})
	if ok || len(missing) != 1 {
		t.Fatal("ложное срабатывание проверки")
	}
}
