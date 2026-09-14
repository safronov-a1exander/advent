package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// titledFrame рисует рамку с подписью, вписанной в верхнюю границу.
// Получается «вкладка» без отдельной строки под заголовок — экономит
// место и выглядит как обычная панель приложения, а не как подпись к слайду.
//
// Верхняя граница собирается вручную, а сам блок рисуется без неё.
// Врезать подпись в уже отрисованную рамку нельзя: lipgloss вшивает в строку
// ANSI-коды цвета, и посимвольная замена рвёт их — на экране появляется мусор
// вроде «блокнот 4;72m», а правая граница пропадает.
func titledFrame(style lipgloss.Style, border color.Color, width int, title, content string) string {
	if title == "" {
		return style.Width(width).Render(content)
	}

	body := style.BorderTop(false).Width(width).Render(content)
	total := lipgloss.Width(body)

	b := lipgloss.RoundedBorder()
	label := " " + title + " "
	// левый угол + один прочерк + подпись + прочерки + правый угол
	fill := total - 3 - runeLen(label)
	if fill < 1 {
		return style.Width(width).Render(content)
	}

	top := b.TopLeft + b.Top + label + strings.Repeat(b.Top, fill) + b.TopRight
	return lipgloss.NewStyle().Foreground(border).Render(top) + "\n" + body
}

func runeLen(s string) int { return len([]rune(s)) }
