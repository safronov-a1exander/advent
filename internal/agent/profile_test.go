package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/safronov-a1exander/advent/internal/profile"
)

// profileDir — каталог с двумя профилями: у одного дороги с ключевыми
// словами, у другого — только стиль.
func profileDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"сеньор.yaml": `name: Олег, сеньор
about: тимлид, пишет на Go
style:
  tone: сухой
  level: базовое не объяснять
limits:
  - без предисловий
pipelines:
  - name: решить
    stages: [варианты, цена, что брать]
  - name: инцидент
    when: [упал, таймаут]
    stages: [гипотезы, проверка, фикс]
    tier: strong
    strategy: пошагово
`,
		"джуниор.yaml": `name: Максим, джуниор
about: полгода в профессии
style:
  tone: дружелюбный
  level: объяснять базовое
pipelines:
  - name: разобраться
    stages: [идея, пример]
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func profilePool(t *testing.T, f *fakeLLM) *Pool {
	t.Helper()
	p := NewPool(f, "fake", nil)
	p.SetProfileStore(profile.NewFileStore(profileDir(t)))
	p.SetCatalog([]ModelTier{{ID: "малая", Tier: "weak"}, {ID: "большая", Tier: "strong"}})
	return p
}

func TestProfileGoesIntoEverySystemPrompt(t *testing.T) {
	f := &fakeLLM{}
	a := profilePool(t, f).Spawn(Config{Model: "m", System: "sys", Profile: "сеньор"})

	ask(t, a, "как лучше сделать кэш?")
	sys := systemOf(f.last())
	for _, want := range []string{"sys", "тимлид, пишет на Go", "тон: сухой", "ограничение: без предисловий"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("в системном промпте нет %q:\n%s", want, sys)
		}
	}

	// «Подключите профиль к каждому запросу» — во втором запросе он тоже есть.
	ask(t, a, "а если данных много?")
	if !strings.Contains(systemOf(f.last()), "тон: сухой") {
		t.Fatal("во втором запросе профиля не оказалось")
	}
}

func TestDifferentProfilesDifferentPrompts(t *testing.T) {
	f := &fakeLLM{}
	p := profilePool(t, f)
	sen := p.Spawn(Config{Model: "m", System: "sys", Profile: "сеньор"})
	jun := p.Spawn(Config{Model: "m", System: "sys", Profile: "джуниор"})

	ask(t, sen, "объясни dependency injection")
	senSys := systemOf(f.last())
	ask(t, jun, "объясни dependency injection")
	junSys := systemOf(f.last())

	if senSys == junSys {
		t.Fatal("один и тот же запрос у разных профилей собрал одинаковый промпт")
	}
	if !strings.Contains(senSys, "базовое не объяснять") || strings.Contains(senSys, "объяснять базовое") {
		t.Fatalf("сеньор получил не свой профиль:\n%s", senSys)
	}
	if !strings.Contains(junSys, "объяснять базовое") {
		t.Fatalf("джуниор получил не свой профиль:\n%s", junSys)
	}
}

func TestPipelinePicksStrategyAndModel(t *testing.T) {
	f := &fakeLLM{}
	a := profilePool(t, f).Spawn(Config{Model: "m", System: "sys", Profile: "сеньор"})

	// Обычный вопрос — дорога по умолчанию: модель и стратегия агента.
	ask(t, a, "как лучше сделать кэш?")
	req := f.last()
	if req.Model != "m" {
		t.Fatalf("дорога по умолчанию сменила модель на %q", req.Model)
	}
	if !strings.Contains(systemOf(req), "варианты → цена → что брать") {
		t.Fatalf("стадии дороги не попали в промпт:\n%s", systemOf(req))
	}

	// Инцидент — своя дорога: сильная модель и пошаговая стратегия.
	ask(t, a, "сервис упал ночью, что делать")
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
	// дорога выбирается по тексту запроса, а не запоминается.
	ask(t, a, "а что по индексам?")
	if req = f.last(); req.Model != "m" {
		t.Fatalf("модель дороги протекла в следующий запрос: %q", req.Model)
	}
	if a.Config().Strategy != "" {
		t.Fatalf("стратегия дороги осела в конфиге агента: %q", a.Config().Strategy)
	}
}

func TestPipelineEventTellsWhichRoad(t *testing.T) {
	f := &fakeLLM{}
	a := profilePool(t, f).Spawn(Config{Model: "m", Profile: "сеньор"})

	var road, stages string
	if _, err := a.Ask(t.Context(), "у нас таймаут на проде", func(e Event) {
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
}

func TestSwitchingProfileChangesAnswersNotHistory(t *testing.T) {
	f := &fakeLLM{}
	p := profilePool(t, f)
	a := p.Spawn(Config{Model: "m", System: "sys", Profile: "сеньор"})
	ask(t, a, "первый вопрос")

	cfg := a.Config()
	cfg.Profile = "джуниор"
	a.SetConfig(cfg)
	ask(t, a, "второй вопрос")

	sys := systemOf(f.last())
	if !strings.Contains(sys, "полгода в профессии") {
		t.Fatalf("смена профиля не подействовала:\n%s", sys)
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

	p := NewPool(f, "fake", nil)
	p.SetStore(NewFileStore(sessions))
	p.SetProfileStore(profile.NewFileStore(dir))
	a := p.Spawn(Config{Model: "m", System: "sys", Profile: "сеньор"})
	ask(t, a, "привет")

	p2 := NewPool(f, "fake", nil)
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
	p := profilePool(t, f)
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
