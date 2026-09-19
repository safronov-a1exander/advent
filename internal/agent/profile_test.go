package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/profile"
)

// profileDir — два профиля: у сеньора две дороги (значит, дорогу будет
// выбирать модель), у джуниора одна (значит, выбирать нечего и вызова
// быть не должно).
func profileDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"сеньор.md": `---
name: Олег, сеньор
pipelines:
  - name: решить
    stages: [варианты, цена, что брать]
  - name: инцидент
    when: что-то сломалось прямо сейчас и нужно понять причину
    stages: [гипотезы, проверка, фикс]
    tier: strong
    strategy: пошагово
---

Тимлид, пишет на Go. Тон сухой, базовое не объяснять, без предисловий.
`,
		"джуниор.md": `---
name: Максим, джуниор
pipelines:
  - name: разобраться
    stages: [идея, пример]
---

Полгода в профессии. Тон дружелюбный, объяснять базовое.
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func profilePool(t *testing.T, p llm.Provider) *Pool {
	t.Helper()
	pool := NewPool(p, "fake", nil)
	pool.SetProfileStore(profile.NewFileStore(profileDir(t)))
	pool.SetCatalog([]ModelTier{{ID: "малая", Tier: "weak"}, {ID: "большая", Tier: "strong"}})
	return pool
}

// isChoose — это запрос к выбирающему дорогу.
func isChoose(req llm.Request) bool {
	return strings.HasPrefix(req.Messages[0].Content, "Ты распределяешь запросы")
}

// roadPicker — «модель», которая выбирает дорогу по смыслу: инцидент, если
// в реплике говорится о поломке, иначе дорога по умолчанию. Живая модель
// делает то же самое, только не по одному слову.
type roadPicker struct{ *fakeLLM }

func (p roadPicker) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if !isChoose(req) {
		return p.fakeLLM.Chat(ctx, req)
	}
	p.fakeLLM.mu.Lock()
	p.fakeLLM.requests = append(p.fakeLLM.requests, req)
	p.fakeLLM.mu.Unlock()

	body := strings.ToLower(req.Messages[1].Content)
	road := "решить"
	for _, sign := range []string{"упал", "таймаут", "легло", "не отвечает"} {
		if strings.Contains(body, sign) {
			road = "инцидент"
		}
	}
	return &llm.Response{Model: req.Model, Content: `{"road":"` + road + `"}`,
		Usage: llm.Usage{PromptTokens: 30, CompletionTokens: 6}}, nil
}

func (p roadPicker) ChatStream(ctx context.Context, req llm.Request, on func(llm.Chunk) error) (*llm.Response, error) {
	return p.Chat(ctx, req)
}

func TestProfileGoesIntoEverySystemPrompt(t *testing.T) {
	f := &fakeLLM{}
	a := profilePool(t, roadPicker{f}).Spawn(Config{Model: "m", System: "sys", Profile: "сеньор"})

	ask(t, a, "как лучше сделать кэш?")
	sys := systemOf(f.last())
	for _, want := range []string{"sys", "Тимлид, пишет на Go", "без предисловий"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("в системном промпте нет %q:\n%s", want, sys)
		}
	}

	// «Подключите профиль к каждому запросу» — во втором запросе он тоже есть.
	ask(t, a, "а если данных много?")
	if !strings.Contains(systemOf(f.last()), "Тон сухой") {
		t.Fatal("во втором запросе профиля не оказалось")
	}
}

func TestDifferentProfilesDifferentPrompts(t *testing.T) {
	f := &fakeLLM{}
	p := profilePool(t, roadPicker{f})
	sen := p.Spawn(Config{Model: "m", System: "sys", Profile: "сеньор"})
	jun := p.Spawn(Config{Model: "m", System: "sys", Profile: "джуниор"})

	ask(t, sen, "объясни dependency injection")
	senSys := systemOf(f.last())
	ask(t, jun, "объясни dependency injection")
	junSys := systemOf(f.last())

	if senSys == junSys {
		t.Fatal("один и тот же запрос у разных профилей собрал одинаковый промпт")
	}
	if !strings.Contains(senSys, "базовое не объяснять") {
		t.Fatalf("сеньор получил не свой профиль:\n%s", senSys)
	}
	if !strings.Contains(junSys, "объяснять базовое") {
		t.Fatalf("джуниор получил не свой профиль:\n%s", junSys)
	}
}

func TestRoadIsChosenByMeaningNotWords(t *testing.T) {
	f := &fakeLLM{}
	a := profilePool(t, roadPicker{f}).Spawn(Config{Model: "m", System: "sys", Profile: "сеньор"})

	// Обычный вопрос — дорога по умолчанию: модель и стратегия агента.
	ask(t, a, "как лучше сделать кэш?")
	req := f.last()
	if req.Model != "m" {
		t.Fatalf("дорога по умолчанию сменила модель на %q", req.Model)
	}
	if !strings.Contains(systemOf(req), "варианты → цена → что брать") {
		t.Fatalf("стадии дороги не попали в промпт:\n%s", systemOf(req))
	}

	// Поломка — своя дорога: сильная модель и пошаговая стратегия.
	// Слова «инцидент» в реплике нет: выбор по смыслу, а не по вхождению.
	ask(t, a, "сервис не отвечает уже час")
	req = f.last()
	if req.Model != "большая" {
		t.Fatalf("дорога инцидента не переключила модель: %q", req.Model)
	}
	if !strings.Contains(req.Messages[len(req.Messages)-1].Content, "Решай пошагово") {
		t.Fatal("дорога инцидента не включила пошаговую стратегию")
	}
	if !strings.Contains(systemOf(req), "гипотезы → проверка → фикс") {
		t.Fatal("стадии дороги инцидента не попали в промпт")
	}

	// И следующий обычный вопрос не унаследовал ни модель, ни стратегию:
	// дорога выбирается под запрос, а не запоминается.
	ask(t, a, "а что по индексам?")
	if req = f.last(); req.Model != "m" {
		t.Fatalf("модель дороги протекла в следующий запрос: %q", req.Model)
	}
	if a.Config().Strategy != "" {
		t.Fatalf("стратегия дороги осела в конфиге агента: %q", a.Config().Strategy)
	}
}

func TestSingleRoadCostsNoCall(t *testing.T) {
	// У джуниора одна дорога — выбирать не из чего, и платить не за что.
	f := &fakeLLM{}
	a := profilePool(t, roadPicker{f}).Spawn(Config{Model: "m", Profile: "джуниор"})
	ask(t, a, "объясни dependency injection")

	if tr := a.Turns(); tr[0].AuxCalls != 0 {
		t.Fatalf("при одной дороге служебных вызовов быть не должно: %+v", tr[0])
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		if isChoose(req) {
			t.Fatal("выбор дороги запускался, хотя дорога одна")
		}
	}
}

func TestRoadChoiceIsCountedAndAnnounced(t *testing.T) {
	f := &fakeLLM{}
	a := profilePool(t, roadPicker{f}).Spawn(Config{Model: "m", Profile: "сеньор"})

	var road, stages string
	if _, err := a.Ask(t.Context(), "у нас всё легло", func(e Event) {
		if e.Kind == EventPipeline {
			road, stages = e.Label, e.Content
		}
	}); err != nil {
		t.Fatal(err)
	}
	if road != "инцидент" {
		t.Fatalf("о выбранной дороге не сообщили: %q", road)
	}
	if !strings.Contains(stages, "гипотезы") {
		t.Fatalf("в событии нет стадий: %q", stages)
	}
	if tr := a.Turns(); tr[0].AuxCalls != 1 {
		t.Fatalf("вызов выбора не посчитан: %+v", tr[0])
	}
}

// brokenPicker отвечает на выбор дороги прозой.
type brokenPicker struct{ *fakeLLM }

func (p brokenPicker) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if isChoose(req) {
		p.fakeLLM.mu.Lock()
		p.fakeLLM.requests = append(p.fakeLLM.requests, req)
		p.fakeLLM.mu.Unlock()
		return &llm.Response{Model: req.Model, Content: "думаю, это инцидент"}, nil
	}
	return p.fakeLLM.Chat(ctx, req)
}

func (p brokenPicker) ChatStream(ctx context.Context, req llm.Request, on func(llm.Chunk) error) (*llm.Response, error) {
	return p.Chat(ctx, req)
}

func TestBrokenChoiceFallsBackToDefaultRoad(t *testing.T) {
	// Ошибка выбора должна стоить обычного маршрута, а не потерянного
	// ответа. Это и отличает выбор дороги от проверки инвариантов.
	f := &fakeLLM{}
	a := profilePool(t, brokenPicker{f}).Spawn(Config{Model: "m", System: "sys", Profile: "сеньор"})

	var warned bool
	r, err := a.Ask(t.Context(), "у нас всё легло", func(e Event) {
		if e.Kind == EventPipeline && strings.Contains(e.Label, "не JSON") {
			warned = true
		}
	})
	if err != nil {
		t.Fatalf("сбой выбора не должен ломать ход: %v", err)
	}
	if r.Final.Content == "" {
		t.Fatal("ответ потерян")
	}
	if !warned {
		t.Fatal("о сбое выбора должно быть сказано")
	}
	if req := f.last(); req.Model != "m" {
		t.Fatalf("после сбоя должна идти дорога по умолчанию, а модель %q", req.Model)
	}
}

func TestSwitchingProfileChangesAnswersNotHistory(t *testing.T) {
	f := &fakeLLM{}
	p := profilePool(t, roadPicker{f})
	a := p.Spawn(Config{Model: "m", System: "sys", Profile: "сеньор"})
	ask(t, a, "первый вопрос")

	cfg := a.Config()
	cfg.Profile = "джуниор"
	a.SetConfig(cfg)
	ask(t, a, "второй вопрос")

	if !strings.Contains(systemOf(f.last()), "Полгода в профессии") {
		t.Fatalf("смена профиля не подействовала:\n%s", systemOf(f.last()))
	}
	// История разговора при этом на месте: профиль — про то, как отвечать,
	// а не про то, о чём говорили.
	if n := len(a.History()); n != 4 {
		t.Fatalf("смена профиля тронула историю: %d сообщений", n)
	}
}

func TestBadProfileDoesNotBreakAgent(t *testing.T) {
	f := &fakeLLM{}
	p := profilePool(t, f)
	a := p.Spawn(Config{Model: "m", System: "sys", Profile: "которого-нет"})

	if a.Profile() != nil {
		t.Fatal("несуществующий профиль не должен подниматься")
	}
	if ask(t, a, "вопрос") == "" {
		t.Fatal("агент без профиля должен отвечать как обычно")
	}
	if systemOf(f.last()) != "sys" {
		t.Fatal("в промпт попало что-то от непрочитанного профиля")
	}
	// Но пользователь об этом узнает: молча персонализировать
	// «как получится» хуже, чем сказать.
	if err := p.SaveErr(); err == nil || !strings.Contains(err.Error(), "профиль") {
		t.Fatalf("о непрочитанном профиле не сообщили: %v", err)
	}
}

func TestProfileSurvivesRestart(t *testing.T) {
	sessions := t.TempDir()
	dir := profileDir(t)
	f := &fakeLLM{}

	p := NewPool(roadPicker{f}, "fake", nil)
	p.SetStore(NewFileStore(sessions))
	p.SetProfileStore(profile.NewFileStore(dir))
	a := p.Spawn(Config{Model: "m", System: "sys", Profile: "сеньор"})
	ask(t, a, "привет")

	p2 := NewPool(roadPicker{f}, "fake", nil)
	p2.SetStore(NewFileStore(sessions))
	p2.SetProfileStore(profile.NewFileStore(dir))
	restored, err := p2.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0].Profile() == nil {
		t.Fatal("профиль не поднялся вместе с разговором")
	}
	if restored[0].Profile().Name != "Олег, сеньор" {
		t.Fatalf("поднялся не тот профиль: %q", restored[0].Profile().Name)
	}
}

func TestProfileAndMemoryStackInFixedOrder(t *testing.T) {
	// Профиль и память — разные вещи, и в промпте они идут в заданном
	// порядке: сначала «как отвечать», потом «что известно». Порядок
	// определяет префикс, а от префикса зависит кэш.
	f := &fakeLLM{}
	p := profilePool(t, roadPicker{f})
	a := p.Spawn(Config{Model: "m", System: "sys", Profile: "сеньор",
		Memory: MemoryManual, User: "олег", Task: "бот"})
	_ = a.Remember("user", "стек", "Go")
	ask(t, a, "как лучше сделать кэш?")

	sys := systemOf(f.last())
	iProf := strings.Index(sys, "Профиль пользователя")
	iRoad := strings.Index(sys, "дорогой «решить»")
	iMem := strings.Index(sys, "Что известно о собеседнике")
	if iProf < 0 || iRoad < 0 || iMem < 0 {
		t.Fatalf("не все блоки на месте:\n%s", sys)
	}
	if !(iProf < iRoad && iRoad < iMem) {
		t.Fatalf("порядок блоков сбит (профиль %d, дорога %d, память %d):\n%s", iProf, iRoad, iMem, sys)
	}
}
