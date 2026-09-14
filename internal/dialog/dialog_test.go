package dialog

import (
	"context"
	"os"
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
		Variants: []Variant{
			{Config: agent.Config{Name: "полная"}},
			{Config: agent.Config{Name: "сжатие", Context: agent.ContextSummary, KeepLast: llm.I(2), SummarizeEvery: llm.I(2)}},
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
	if sum.AuxCalls == 0 || full.AuxCalls != 0 {
		t.Fatalf("вызовы сжатия: полная %d, summary %d", full.AuxCalls, sum.AuxCalls)
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
	if sum.Input() != sum.Prompt+sum.AuxPrompt || sum.AuxPrompt == 0 {
		t.Fatalf("входные токены сжатия не учтены: %+v", sum)
	}
}

func TestBranchCommands(t *testing.T) {
	s := &Scenario{
		Defaults: agent.Config{Tier: "weak"},
		Variants: []Variant{
			{Config: agent.Config{Name: "лента"}},
			{Config: agent.Config{Name: "ветки"}, Branches: true},
		},
		Dialog: []Line{
			{Say: "общее"},
			{Checkpoint: "развилка"},
			{Branch: "А", From: "развилка"},
			{Say: "только в А"},
			{Branch: "Б", From: "развилка"},
			{Say: "что известно?", Expect: []string{"общее"}},
			{Switch: "А"},
			{Say: "а тут?", Expect: []string{"только в А"}},
		},
	}
	pool := agent.NewPool(&echoLLM{}, "echo", nil)
	res, err := Run(context.Background(), pool, []llm.ModelInfo{{ID: "m", Tier: "weak"}}, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	line, br := res[0], res[1]
	if !strings.Contains(line.Steps[1].Command, "пропущено") || line.Steps[3].Branch != agent.MainBranch {
		t.Fatalf("вариант без веток: %+v", line.Steps[1])
	}
	for _, i := range []int{1, 2, 4, 6} {
		if br.Steps[i].Err != "" {
			t.Fatalf("команда %q: %s", br.Steps[i].Command, br.Steps[i].Err)
		}
	}
	// в ветке Б нет реплики из А — эхо-провайдер вернул бы её
	if b := br.Steps[5]; b.Branch != "Б" || strings.Contains(b.Answer, "только в А") {
		t.Fatalf("ветка Б: %+v", b)
	}
	if a := br.Steps[7]; a.Branch != "А" || !a.Passed {
		t.Fatalf("после возврата в А: %+v", a)
	}
	if tt := br.Totals(); tt.Checks != 2 || tt.Errors != 0 {
		t.Fatalf("итоги веток: %+v", tt)
	}
}

func TestLoadVariantWithBranches(t *testing.T) {
	path := t.TempDir() + "/s.yaml"
	body := "variants:\n  - name: ветки\n    context: facts\n    branches: true\ndialog:\n  - say: привет\n  - checkpoint: x\n  - branch: y\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "from") {
		t.Fatalf("branch без from: %v", err)
	}
	body = strings.Replace(body, "branch: y\n", "branch: y\n    from: x\n", 1)
	_ = os.WriteFile(path, []byte(body), 0o644)
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if v := s.Variants[0]; !v.Branches || v.Context != agent.ContextFacts || v.Name != "ветки" {
		t.Fatalf("вариант: %+v", v)
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
