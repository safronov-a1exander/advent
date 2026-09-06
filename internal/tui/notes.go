package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Notes — блокнот под окном диалога.
//
// Нужен, чтобы комментарии не лезли в саму переписку: в чате остаётся
// только разговор с моделью, а пояснения набираются здесь. Печатать в него
// можно руками (Tab переводит фокус), а демо-сценарий делает то же самое
// командой `note` — текст появляется посимвольно, как будто его набирают.
type Notes struct {
	lines   []string      // уже набранные строки
	current string        // строка, которая набирается прямо сейчас
	target  string        // её полный текст (для автонабора)
	queue   []string      // что ещё предстоит набрать
	speed   time.Duration // задержка между символами
	width   int
	height  int // сколько строк показываем
	typing  bool
}

type noteTickMsg struct{}

func NewNotes(width, height int) *Notes {
	return &Notes{width: width, height: height, speed: 22 * time.Millisecond}
}

func (n *Notes) SetSize(w, h int) { n.width, n.height = w, h }

// Empty — блокнот ещё не трогали, показывать нечего.
func (n *Notes) Empty() bool {
	return len(n.lines) == 0 && n.current == "" && !n.typing && len(n.queue) == 0
}

// Type ставит строку в очередь на автонабор.
func (n *Notes) Type(text string) tea.Cmd {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if n.typing {
		n.queue = append(n.queue, text)
		return nil
	}
	n.start(text)
	return n.tick()
}

func (n *Notes) start(text string) {
	if n.current != "" {
		n.lines = append(n.lines, n.current)
	}
	n.current, n.target, n.typing = "", text, true
}

func (n *Notes) tick() tea.Cmd {
	return tea.Tick(n.speed, func(time.Time) tea.Msg { return noteTickMsg{} })
}

// Tick добавляет очередной символ. Возвращает команду следующего тика,
// пока есть что набирать.
func (n *Notes) Tick() tea.Cmd {
	if !n.typing {
		return nil
	}
	if len(n.current) < len(n.target) {
		// шагаем по рунам, иначе кириллица развалится на байты
		r := []rune(n.target)
		c := []rune(n.current)
		n.current = string(r[:len(c)+1])
		return n.tick()
	}
	// строка допечатана
	n.lines = append(n.lines, n.current)
	n.current, n.target, n.typing = "", "", false
	if len(n.queue) > 0 {
		next := n.queue[0]
		n.queue = n.queue[1:]
		n.start(next)
		return n.tick()
	}
	return nil
}

// ---- ручной ввод ----

// Key обрабатывает клавишу, когда фокус на блокноте.
// Во время автонабора ручной ввод игнорируется, чтобы строки не смешивались.
func (n *Notes) Key(msg tea.KeyMsg) bool {
	if n.typing {
		return true
	}
	switch msg.Type {
	case tea.KeyRunes:
		n.current += string(msg.Runes)
		return true
	case tea.KeySpace:
		n.current += " "
		return true
	case tea.KeyBackspace:
		r := []rune(n.current)
		if len(r) > 0 {
			n.current = string(r[:len(r)-1])
		}
		return true
	case tea.KeyEnter:
		if strings.TrimSpace(n.current) != "" {
			n.lines = append(n.lines, n.current)
		}
		n.current = ""
		return true
	}
	return false
}

// ---- отрисовка ----

func (n *Notes) View(focused bool) string {
	visible := append([]string(nil), n.lines...)
	if n.current != "" || n.typing || focused {
		cur := n.current
		if focused && !n.typing {
			cur += "▌"
		}
		visible = append(visible, cur)
	}
	// показываем хвост: последние строки важнее первых
	if len(visible) > n.height {
		visible = visible[len(visible)-n.height:]
	}

	st := lipgloss.NewStyle().Foreground(cDim)
	if focused {
		st = lipgloss.NewStyle()
	}
	var b strings.Builder
	for i, l := range visible {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(st.Render(short(l, n.width)))
	}
	// добиваем пустыми строками, чтобы рамка не прыгала по высоте
	for i := len(visible); i < n.height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}
