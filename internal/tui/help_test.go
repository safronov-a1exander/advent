package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/llm"
)

func helpModel(t *testing.T, h *Help) *Model {
	t.Helper()
	set := NewSettings([]llm.ModelInfo{{ID: "m"}}, "m", "sys")
	p := agent.NewPool(echoProvider{}, "echo", nil)
	m := NewModel(Options{Pool: p, Provider: "echo", Settings: set, Help: h})
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 40})
	return m
}

func TestHelpAsksWithRAGAndGitStateForOneTurn(t *testing.T) {
	calls := 0
	m := helpModel(t, &Help{Context: func(context.Context) (string, error) {
		calls++
		return "ветка: day-31\nнезакоммичено файлов: 2", nil
	}})

	typeText(t, m, "/help где лежат профили?")
	cfg := m.ag.Config()
	if calls != 1 {
		t.Fatalf("состояние git нужно взять до вопроса, взято %d раз", calls)
	}
	if cfg.RAG != "on" {
		t.Fatal("вопрос /help должен уйти с базой знаний")
	}
	if !strings.Contains(cfg.System, "ветка: day-31") || !strings.HasPrefix(cfg.System, "sys") {
		t.Fatalf("состояние репозитория — добавкой к системному промпту: %q", cfg.System)
	}
	if m.lastQuestion != "где лежат профили?" {
		t.Fatalf("в модель уходит вопрос без /help: %q", m.lastQuestion)
	}
	if m.set.RAG != "" {
		t.Fatal("/help не должен включать базу в панели насовсем")
	}
}

func TestHelpWithoutQuestionShowsStateOnly(t *testing.T) {
	m := helpModel(t, &Help{Intro: "команды ассистента", Context: func(context.Context) (string, error) {
		return "ветка: main", nil
	}})
	typeText(t, m, "/help")
	if m.busy.Load() || len(m.ag.History()) != 0 {
		t.Fatal("/help без вопроса в модель не уходит")
	}
	feed := strings.Join(m.lines, "\n")
	if !strings.Contains(feed, "ветка: main") || !strings.Contains(feed, "команды ассистента") {
		t.Fatalf("в ленте нет состояния и подсказки:\n%s", feed)
	}
}

func TestHelpIsPlainTextOutsideDevAssistant(t *testing.T) {
	m := helpModel(t, nil)
	if _, ok := m.helpCommand("/help что умеешь?"); ok {
		t.Fatal("без режима ассистента /help — обычная реплика")
	}
}
