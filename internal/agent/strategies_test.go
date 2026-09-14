package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/safronov-a1exander/advent/internal/llm"
)

// factsAnswer — ответ «провайдера» на запрос обновления фактов: реплика вида
// «ключ = значение» добавляет или заменяет факт, остальные факты остаются.
func factsAnswer(msgs []llm.Message) (string, bool) {
	if !strings.HasPrefix(msgs[0].Content, "Ты ведёшь блок ключевых фактов") {
		return "", false
	}
	body := msgs[1].Content
	cur := body[strings.Index(body, "Текущие факты:\n")+len("Текущие факты:\n"):]
	cur = cur[:strings.Index(cur, "\n\n")]
	var m map[string]string
	_ = json.Unmarshal([]byte(cur), &m)
	if m == nil {
		m = map[string]string{}
	}
	msg := body[strings.Index(body, "Новое сообщение пользователя:\n")+len("Новое сообщение пользователя:\n"):]
	if k, v, ok := strings.Cut(msg, "="); ok {
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	out, _ := json.Marshal(map[string]any{"facts": m})
	return string(out), true
}

func TestSlidingWindowSendsOnlyTail(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(f, "fake", nil).Spawn(Config{Model: "m", System: "sys", Context: ContextWindow, KeepLast: llm.I(4)})
	ask(t, a, "меня зовут Саша")
	for i := 2; i <= 4; i++ {
		ask(t, a, fmt.Sprintf("реплика %d", i))
	}
	req := f.last()
	// system + 4 последних сообщения + вопрос
	if len(req.Messages) != 6 {
		t.Fatalf("в запросе %d сообщений, ожидали 6", len(req.Messages))
	}
	if req.Messages[1].Content != "реплика 2" || req.Messages[1].Role != llm.RoleUser {
		t.Fatalf("окно начинается не с реплики пользователя: %+v", req.Messages[1])
	}
	// ранний факт за окном — модель его не видит
	if got := ask(t, a, "как меня зовут?"); got != "не знаю" {
		t.Fatalf("окно пропустило раннее сообщение: %q", got)
	}
	// история при этом хранится целиком
	if n := len(a.History()); n != 10 {
		t.Fatalf("память: %d сообщений", n)
	}
	if tr := a.Turns(); tr[len(tr)-1].AuxCalls != 0 {
		t.Fatal("у окна не бывает служебных вызовов")
	}
}

func TestTailKeepsPairs(t *testing.T) {
	hist := make([]llm.Message, 7)
	for i := range hist {
		hist[i].Role = llm.RoleUser
		if i%2 == 1 {
			hist[i].Role = llm.RoleAssistant
		}
	}
	if got := tail(hist, 3); len(got) != 3 || got[0].Role != llm.RoleUser {
		t.Fatalf("хвост 3 из 7: %d, первое %s", len(got), got[0].Role)
	}
	if got := tail(hist[:6], 3); len(got) != 2 || got[0].Role != llm.RoleUser {
		t.Fatalf("хвост 3 из 6 должен сузиться до пары: %d", len(got))
	}
	if tail(hist, 0) != nil || len(tail(hist, 100)) != 7 {
		t.Fatal("границы хвоста")
	}
}

// factsProvider отвечает на запросы фактов через factsAnswer, остальное — как fakeLLM.
type factsProvider struct{ *fakeLLM }

func (p factsProvider) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if out, ok := factsAnswer(req.Messages); ok {
		p.fakeLLM.mu.Lock()
		p.fakeLLM.requests = append(p.fakeLLM.requests, req)
		p.fakeLLM.mu.Unlock()
		return &llm.Response{Model: req.Model, Content: out, Usage: llm.Usage{PromptTokens: 50, CompletionTokens: 20}}, nil
	}
	return p.fakeLLM.Chat(ctx, req)
}

func (p factsProvider) ChatStream(ctx context.Context, req llm.Request, on func(llm.Chunk) error) (*llm.Response, error) {
	return p.Chat(ctx, req)
}

func TestStickyFactsKeepEarlyDetails(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(factsProvider{f}, "fake", nil).Spawn(Config{Model: "m", System: "sys", Context: ContextFacts, KeepLast: llm.I(2)})

	var updates int
	for _, q := range []string{"меня зовут Саша", "срок = 6 недель", "бюджет = 150000", "реплика 4", "срок = 8 недель"} {
		if _, err := a.Ask(t.Context(), q, func(e Event) {
			if e.Kind == EventContext {
				updates++
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	if updates != 5 {
		t.Fatalf("факты должны обновляться после каждого сообщения: %d", updates)
	}
	facts := a.Facts()
	got := map[string]string{}
	for _, fc := range facts {
		got[fc.Key] = fc.Value
	}
	if got["срок"] != "8 недель" || got["бюджет"] != "150000" {
		t.Fatalf("факты: %+v", facts)
	}

	req := f.last()
	sys := req.Messages[0].Content
	if !strings.Contains(sys, "Ключевые факты") || !strings.Contains(sys, "- бюджет: 150000") {
		t.Fatalf("фактов нет в системном промпте:\n%s", sys)
	}
	if strings.Contains(sys, "6 недель") {
		t.Fatal("устаревшее значение осталось в фактах")
	}
	// в запросе: system + хвост 2 + вопрос
	if len(req.Messages) != 4 {
		t.Fatalf("в запросе %d сообщений", len(req.Messages))
	}
	var aux int
	for _, tr := range a.Turns() {
		aux += tr.AuxCalls
	}
	if aux != 5 {
		t.Fatalf("служебных вызовов в учёте: %d", aux)
	}
}

func TestFactsFailureKeepsOldFacts(t *testing.T) {
	a := NewPool(&fakeLLM{}, "fake", nil).Spawn(Config{Model: "m", Context: ContextFacts})
	var warned bool
	_, err := a.Ask(t.Context(), "вопрос", func(e Event) {
		if e.Kind == EventContext && strings.Contains(e.Label, "не обновлены") {
			warned = true
		}
	})
	if err != nil {
		t.Fatalf("сбой фактов сломал ход: %v", err)
	}
	if !warned || len(a.Facts()) != 0 {
		t.Fatal("ответ не-JSON должен дать предупреждение и оставить факты как были")
	}
}

func TestParseFactsKeepsOrderAndFlattensLists(t *testing.T) {
	facts, err := parseFacts("```json\n{\"facts\": {\"цель\": \"бот\", \"платформы\": [\"Telegram\", \"веб\"], \"срок\": 8}}\n```")
	if err != nil {
		t.Fatal(err)
	}
	want := []Fact{{"цель", "бот"}, {"платформы", "Telegram; веб"}, {"срок", "8"}}
	if fmt.Sprint(facts) != fmt.Sprint(want) {
		t.Fatalf("факты %v, ожидали %v", facts, want)
	}
	if _, err := parseFacts(`{"нет": 1}`); err == nil {
		t.Fatal("без поля facts должна быть ошибка")
	}
}

func TestBranchesAreIndependent(t *testing.T) {
	dir := t.TempDir()
	p := NewPool(&fakeLLM{}, "fake", nil)
	p.SetStore(NewFileStore(dir))
	a := p.Spawn(Config{Name: "тз", Model: "m"})

	ask(t, a, "общее требование")
	cp, err := a.Checkpoint("")
	if err != nil || cp != "точка-1" {
		t.Fatalf("чекпойнт %q: %v", cp, err)
	}

	if _, err := a.Branch("вариант А", cp); err != nil {
		t.Fatal(err)
	}
	ask(t, a, "меня зовут Алиса")
	if a.ActiveBranch() != "вариант А" || len(a.History()) != 4 {
		t.Fatalf("ветка А: активна %q, сообщений %d", a.ActiveBranch(), len(a.History()))
	}

	if _, err := a.Branch("вариант Б", cp); err != nil {
		t.Fatal(err)
	}
	// ветка Б начинается от чекпойнта: там нет того, что было в А
	if len(a.History()) != 2 {
		t.Fatalf("ветка Б начинается не от чекпойнта: %d сообщений", len(a.History()))
	}
	if got := ask(t, a, "как меня зовут?"); got != "не знаю" {
		t.Fatalf("ветка Б видит историю А: %q", got)
	}
	ask(t, a, "меня зовут Борис")

	if err := a.SwitchBranch("вариант А"); err != nil {
		t.Fatal(err)
	}
	if got := ask(t, a, "как меня зовут?"); got != "тебя зовут Алиса" {
		t.Fatalf("после переключения на А: %q", got)
	}

	br := a.Branches()
	if len(br) != 3 || br[0].Name != MainBranch || br[1].From != cp || !br[1].Active || br[2].Messages != 6 {
		t.Fatalf("ветки: %+v", br)
	}

	// всё переживает перезапуск
	_, restored := restart(t, dir)
	b := restored[0]
	if b.ActiveBranch() != "вариант А" || len(b.Branches()) != 3 || len(b.Checkpoints()) != 1 {
		t.Fatalf("ветки после перезапуска: активна %q, веток %d", b.ActiveBranch(), len(b.Branches()))
	}
	if err := b.SwitchBranch("вариант Б"); err != nil {
		t.Fatal(err)
	}
	if got := ask(t, b, "как меня зовут?"); got != "тебя зовут Борис" {
		t.Fatalf("ветка Б после перезапуска: %q", got)
	}

	// ошибки
	if err := b.SwitchBranch("нет такой"); !errors.Is(err, ErrUnknownBranch) {
		t.Fatalf("переключение на несуществующую: %v", err)
	}
	if _, err := b.Branch("x", "нет такого"); !errors.Is(err, ErrUnknownCheckpoint) {
		t.Fatalf("ветка от несуществующего чекпойнта: %v", err)
	}
	if _, err := b.Branch("вариант А", cp); err == nil {
		t.Fatal("имя ветки повторилось")
	}

	b.Reset()
	if len(b.Branches()) != 1 || len(b.Checkpoints()) != 0 || b.ActiveBranch() != MainBranch {
		t.Fatal("сброс должен убрать ветки и чекпойнты")
	}
}

func TestBranchCarriesFactsFromCheckpoint(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(factsProvider{f}, "fake", nil).Spawn(Config{Model: "m", Context: ContextFacts})
	ask(t, a, "срок = 6 недель")
	cp, _ := a.Checkpoint("развилка")
	if _, err := a.Branch("быстро", cp); err != nil {
		t.Fatal(err)
	}
	ask(t, a, "срок = 4 недели")
	if _, err := a.Branch("полно", cp); err != nil {
		t.Fatal(err)
	}
	if got := a.Facts(); len(got) != 1 || got[0].Value != "6 недель" {
		t.Fatalf("факты ветки от чекпойнта: %+v", got)
	}
	ask(t, a, "срок = 10 недель")
	if err := a.SwitchBranch("быстро"); err != nil {
		t.Fatal(err)
	}
	if got := a.Facts(); got[0].Value != "4 недели" {
		t.Fatalf("факты ветки «быстро» после переключения: %+v", got)
	}
}

func TestBranchRejectedWhileBusy(t *testing.T) {
	f := &fakeLLM{delay: 50e6}
	a := NewPool(f, "fake", nil).Spawn(Config{Model: "m"})
	ask(t, a, "начало")
	cp, _ := a.Checkpoint("")
	done := make(chan struct{})
	go func() { defer close(done); _, _ = a.Ask(t.Context(), "долго", nil) }()
	for !a.Busy() {
	}
	if _, err := a.Branch("x", cp); !errors.Is(err, ErrBusy) {
		t.Fatalf("ветвление во время ответа: %v", err)
	}
	<-done
}
