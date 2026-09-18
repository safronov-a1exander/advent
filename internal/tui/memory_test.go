package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/memory"
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

func TestDumpShowsWhichLayersGoIntoPrompt(t *testing.T) {
	m := memModel(t)
	m.set.MemoryScopes = []string{"task"}
	typeText(t, m, "/user стек = Go")
	typeText(t, m, "/task срок = 6 недель")

	press(t, m, "ctrl+d")
	dump := strings.Join(m.lines, "\n")
	if !strings.Contains(dump, "уходит в промпт") {
		t.Fatalf("Ctrl+D не сказал, что уходит в промпт:\n%s", dump)
	}
	if !strings.Contains(dump, "в промпт НЕ уходит") {
		t.Fatalf("Ctrl+D не отметил слой, который отключён:\n%s", dump)
	}
}
