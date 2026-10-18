package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Help — команда /help ассистента разработчика (день 31).
//
// В обычном чате /help — просто реплика. У ассистента разработчика это
// вопрос о проекте: он уходит в модель с поиском по документации проекта
// и с тем, что сейчас в репозитории, — веткой, последним коммитом,
// незакоммиченными файлами. Это состояние код берёт у MCP-сервера git сам,
// до вопроса: «на какой мы ветке» не должно зависеть от того, догадается
// ли модель вызвать инструмент.
type Help struct {
	// Context — состояние репозитория для этого вопроса, строками
	// «ключ: значение». Ошибка не мешает ответу: вопрос уйдёт без него.
	Context func(ctx context.Context) (string, error)
	// Intro — что показать на /help без вопроса.
	Intro string
}

// helpCommand разбирает /help. false — строка не /help или режим выключен.
func (m *Model) helpCommand(text string) (tea.Cmd, bool) {
	h := m.opts.Help
	if h == nil || !strings.HasPrefix(text, "/") {
		return nil, false
	}
	head, rest, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	if !strings.EqualFold(strings.TrimSpace(head), "help") {
		return nil, false
	}
	q := strings.TrimSpace(rest)

	state := ""
	if h.Context != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		s, err := h.Context(ctx)
		cancel()
		m.pushLine("")
		if err != nil {
			m.pushLine(stErr.Render("🧭 git недоступен: " + err.Error()))
		} else {
			state = s
			m.pushLine(stNote.Render("🧭 " + strings.ReplaceAll(strings.TrimSpace(s), "\n", " · ")))
		}
	}
	if q == "" {
		if h.Intro != "" {
			m.pushLine(stDim.Render(h.Intro))
		}
		m.refresh()
		return nil, true
	}

	// Вопрос о проекте — с базой знаний, даже если в панели она выключена:
	// ради этого /help и существует. Только на этот ход — следующая
	// обычная реплика уйдёт так, как стоит в панели.
	prev := m.set.RAG
	m.set.RAG = "on"
	if state != "" {
		m.turnSystem = "Состояние репозитория прямо сейчас (получено из git через MCP):\n" + strings.TrimSpace(state)
	}
	cmd := m.send(q)
	m.set.RAG = prev
	return cmd, true
}
