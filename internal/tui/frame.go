package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// titledFrame рисует рамку с подписью, вписанной в верхнюю границу.
// Получается «вкладка» без отдельной строки под заголовок — экономит
// место и выглядит как обычная панель приложения, а не как подпись к слайду.
func titledFrame(style lipgloss.Style, width int, title, content string) string {
	out := style.Width(width).Render(content)
	if title == "" {
		return out
	}
	lines := strings.Split(out, "\n")
	if len(lines) == 0 {
		return out
	}
	top := []rune(lines[0])
	label := []rune(" " + title + " ")
	// не рисуем подпись, если она не влезает в границу
	if len(top) < len(label)+4 {
		return out
	}
	copy(top[2:], label)
	lines[0] = string(top)
	return strings.Join(lines, "\n")
}
