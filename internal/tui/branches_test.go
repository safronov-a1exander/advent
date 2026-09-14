package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/llm"
)

// echoProvider отвечает репликами пользователя из запроса — по ответу видно,
// какую историю увидела «модель».
type echoProvider struct{}

func (echoProvider) Name() string                                 { return "echo" }
func (echoProvider) ListModels(context.Context) ([]string, error) { return nil, nil }
func (p echoProvider) ChatStream(ctx context.Context, r llm.Request, _ func(llm.Chunk) error) (*llm.Response, error) {
	return p.Chat(ctx, r)
}
func (echoProvider) Chat(_ context.Context, r llm.Request) (*llm.Response, error) {
	var seen []string
	for _, m := range r.Messages {
		if m.Role == llm.RoleUser {
			seen = append(seen, m.Content)
		}
	}
	return &llm.Response{Model: r.Model, Content: strings.Join(seen, " | "), FinishReason: "stop",
		Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 5}}, nil
}

func press(t *testing.T, m *Model, keys ...string) {
	t.Helper()
	for _, k := range keys {
		msg, ok := keyMap[k]
		if !ok {
			t.Fatalf("нет клавиши %q в keyMap", k)
		}
		m.Update(msg)
	}
}

// say — ход без горутины send: экран в тесте проверяется, а не поток ответа.
func say(t *testing.T, m *Model, text string) string {
	t.Helper()
	m.ag.SetConfig(m.set.AgentConfig())
	reply, err := m.ag.Ask(context.Background(), text, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.pushLine("вы: " + text)
	m.pushLine(reply.Final.Content)
	return reply.Final.Content
}

func TestBranchesScreen(t *testing.T) {
	set := NewSettings([]llm.ModelInfo{{ID: "m"}}, "m", "sys")
	m := NewModel(Options{Pool: agent.NewPool(echoProvider{}, "echo", nil), Provider: "echo", Settings: set})
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 40})

	// пустой разговор ветвить не от чего
	press(t, m, "ctrl+b", "ctrl+s")
	if m.flash == "" || len(m.ag.Checkpoints()) != 0 {
		t.Fatal("чекпойнт пустого разговора")
	}
	press(t, m, "esc")

	say(t, m, "общее требование")
	press(t, m, "ctrl+b")
	if m.focus != focusBranches || !strings.Contains(m.View().Content, "ветки разговора") {
		t.Fatal("Ctrl+B не открыл список веток")
	}
	press(t, m, "ctrl+s", "enter")
	if got := m.ag.ActiveBranch(); got != "ветка-1" || m.focus != focusInput {
		t.Fatalf("Enter на чекпойнте: ветка %q, фокус %v", got, m.focus)
	}
	say(t, m, "только в первой")

	// вторая ветка: курсор стоит на активной ветке, чекпойнт — ниже
	press(t, m, "ctrl+b", "down", "enter")
	if got := m.ag.ActiveBranch(); got != "ветка-2" {
		t.Fatalf("вторая ветка: %q", got)
	}
	if ans := say(t, m, "что видно?"); strings.Contains(ans, "только в первой") {
		t.Fatalf("вторая ветка видит первую: %q", ans)
	}
	if strings.Contains(strings.Join(m.lines, "\n"), "только в первой") {
		t.Fatal("лента второй ветки показывает переписку первой")
	}

	// назад в первую — её лента на месте
	press(t, m, "ctrl+b", "up", "enter")
	if got := m.ag.ActiveBranch(); got != "ветка-1" {
		t.Fatalf("возврат: %q", got)
	}
	if !strings.Contains(strings.Join(m.lines, "\n"), "только в первой") {
		t.Fatal("лента первой ветки потерялась")
	}
	if !strings.Contains(m.View().Content, "⎇ ветка-1 · веток 3") {
		t.Fatal("в шапке нет активной ветки")
	}

	press(t, m, "ctrl+d")
	if !strings.Contains(strings.Join(m.lines, "\n"), "ветки    основная, ●ветка-1, ветка-2") {
		t.Fatal("Ctrl+D не показал ветки")
	}

	// сброс убирает ветки
	press(t, m, "ctrl+r")
	if len(m.ag.Branches()) != 1 || len(m.transcripts) != 0 {
		t.Fatalf("после сброса: веток %d, лент %d", len(m.ag.Branches()), len(m.transcripts))
	}
}
