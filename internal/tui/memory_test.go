package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/memory"
	"github.com/safronov-a1exander/advent/internal/profile"
)

// typeText — «печатает» строку в поле ввода и жмёт Enter, как это делает
// демо-сценарий и человек. Через настоящий путь клавиш, чтобы команды
// памяти проверялись там же, где их разбирает экран.
func typeText(t *testing.T, m *Model, text string) {
	t.Helper()
	for _, r := range text {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	press(t, m, "enter")
}

func memModel(t *testing.T) *Model {
	t.Helper()
	set := NewSettings([]llm.ModelInfo{{ID: "m"}}, "m", "sys")
	set.Memory = agent.MemoryManual
	set.User = "саша"
	set.Task = "бот"
	p := agent.NewPool(echoProvider{}, "echo", nil)
	p.SetMemoryStore(memory.NewFileStore(t.TempDir()))
	m := NewModel(Options{Pool: p, Provider: "echo", Settings: set})
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 40})
	return m
}

func TestMemoryCommandsFromInput(t *testing.T) {
	m := memModel(t)

	typeText(t, m, "/user стек = Go")
	typeText(t, m, "/task срок = 6 недель")

	mem := m.ag.Memory()
	if e, ok := mem.Layer(memory.ScopeUser).Get("стек"); !ok || e.Value != "Go" {
		t.Fatalf("долговременный слой: %+v", mem.Layer(memory.ScopeUser).Entries())
	}
	if e, ok := mem.Layer(memory.ScopeTask).Get("срок"); !ok || e.Source != memory.SourceManual {
		t.Fatalf("рабочий слой: %+v", e)
	}
	// Команда памяти — не реплика: в историю разговора она не попадает.
	if n := len(m.ag.History()); n != 0 {
		t.Fatalf("команда памяти ушла в разговор: %d сообщений", n)
	}

	// Кривой формат не молчит и ничего не портит.
	typeText(t, m, "/task без равно")
	if m.flash == "" {
		t.Fatal("о неверном формате команды должно быть сказано")
	}

	// Обычная реплика со слэшем командой памяти не считается.
	typeText(t, m, "/help что умеешь?")
	if strings.HasPrefix(m.flash, "формат") {
		t.Fatalf("чужая слэш-команда принята за команду памяти: %q", m.flash)
	}
}

func TestMemoryPanelShowsLayersAndForgets(t *testing.T) {
	m := memModel(t)
	typeText(t, m, "/user стек = Go")
	typeText(t, m, "/task срок = 6 недель")

	press(t, m, "ctrl+m")
	if m.focus != focusMemory {
		t.Fatal("Ctrl+M не открыл слои")
	}
	view := m.View().Content
	for _, want := range []string{"память агента", "краткосрочная", "рабочая", "долговременная", "стек: Go", "срок: 6 недель"} {
		if !strings.Contains(view, want) {
			t.Fatalf("в панели нет %q", want)
		}
	}

	// Курсор стоит на первой записи — Del убирает именно её.
	// Первая запись по порядку слоёв — из краткосрочного, его тут нет,
	// поэтому это запись рабочего слоя.
	press(t, m, "delete")
	if _, ok := m.ag.Memory().Layer(memory.ScopeTask).Get("срок"); ok {
		t.Fatal("Del не убрал запись")
	}
	if _, ok := m.ag.Memory().Layer(memory.ScopeUser).Get("стек"); !ok {
		t.Fatal("Del убрал лишнее")
	}
	press(t, m, "esc")
	if m.focus != focusInput {
		t.Fatal("Esc не вернул в поле ввода")
	}
}

func TestMemoryOffTellsHow(t *testing.T) {
	set := NewSettings([]llm.ModelInfo{{ID: "m"}}, "m", "sys")
	m := NewModel(Options{Pool: agent.NewPool(echoProvider{}, "echo", nil), Provider: "echo", Settings: set})
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 40})

	press(t, m, "ctrl+m")
	if m.focus == focusMemory {
		t.Fatal("без памяти панель открываться не должна")
	}
	if !strings.Contains(m.flash, "память выключена") {
		t.Fatalf("непонятно, почему ничего не открылось: %q", m.flash)
	}
	if strings.Contains(m.View().Content, "🧠") {
		t.Fatal("в шапке не должно быть памяти, когда её нет")
	}
}

func TestResetKeepsTaskAndUserLayers(t *testing.T) {
	m := memModel(t)
	typeText(t, m, "/chat остановились = на оплате")
	typeText(t, m, "/task срок = 6 недель")
	typeText(t, m, "/user стек = Go")
	say(t, m, "первая реплика")

	press(t, m, "ctrl+r")

	mem := m.ag.Memory()
	if mem.Layer(memory.ScopeChat).Len() != 0 {
		t.Fatal("Ctrl+R должен стирать краткосрочный слой")
	}
	if mem.Layer(memory.ScopeTask).Len() != 1 || mem.Layer(memory.ScopeUser).Len() != 1 {
		t.Fatal("Ctrl+R не должен трогать рабочий и долговременный слои")
	}
	if !strings.Contains(m.View().Content, "🧠") {
		t.Fatal("в шапке пропала сводка памяти")
	}
}

// profModel — экран с каталогом профилей: сеньор и джуниор.
func profModel(t *testing.T) *Model {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"сеньор.md":  "---\nname: Олег, сеньор\npipelines:\n  - name: решить\n    stages: [варианты, цена]\n---\n\nТимлид. Тон сухой.\n",
		"джуниор.md": "---\nname: Максим, джуниор\n---\n\nПолгода в профессии. Тон дружелюбный.\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	set := NewSettings([]llm.ModelInfo{{ID: "m"}}, "m", "sys")
	set.Profile = "сеньор"
	set.Profiles = []string{"джуниор", "сеньор"}
	p := agent.NewPool(echoProvider{}, "echo", nil)
	p.SetProfileStore(profile.NewFileStore(dir))
	m := NewModel(Options{Pool: p, Provider: "echo", Settings: set})
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 40})
	return m
}

func TestProfileCommandSwitchesAndClears(t *testing.T) {
	m := profModel(t)
	if m.ag.Profile() == nil || m.ag.Profile().Name != "Олег, сеньор" {
		t.Fatal("стартовый профиль не подключился")
	}
	if !strings.Contains(m.View().Content, "👤") {
		t.Fatal("в шапке нет профиля")
	}

	typeText(t, m, "/profile джуниор")
	if got := m.ag.Profile(); got == nil || got.Name != "Максим, джуниор" {
		t.Fatalf("профиль не переключился: %+v", got)
	}
	// Команда профиля — не реплика: в разговор она не уходит.
	if n := len(m.ag.History()); n != 0 {
		t.Fatalf("команда профиля ушла в разговор: %d сообщений", n)
	}

	typeText(t, m, "/profile")
	if m.ag.Profile() != nil {
		t.Fatal("пустой аргумент должен снимать профиль")
	}
	// В шапке профиля больше нет (в ленте остаётся строка «профиль снят»,
	// поэтому ищем именно имя, а не значок).
	if strings.Contains(m.View().Content, "👤 Максим") {
		t.Fatal("снятый профиль остался в шапке")
	}

	typeText(t, m, "/profile которого-нет")
	if m.flash == "" {
		t.Fatal("о непрочитанном профиле должно быть сказано")
	}
}

func TestProfileGoesIntoPromptAndDump(t *testing.T) {
	m := profModel(t)
	say(t, m, "объясни dependency injection")

	press(t, m, "ctrl+d")
	dump := strings.Join(m.lines, "\n")
	if !strings.Contains(dump, "профиль  Олег, сеньор") {
		t.Fatalf("Ctrl+D не показал профиль:\n%s", dump)
	}
	if !strings.Contains(dump, "дорога   «решить»: варианты → цена") {
		t.Fatalf("Ctrl+D не показал дорогу:\n%s", dump)
	}
}
