package tui

import (
	"image/color"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// В lipgloss v2 нет AdaptiveColor: цвет под тему терминала выбирает
// lipgloss.LightDark, а саму тему приложение узнаёт от терминала
// сообщением tea.BackgroundColorMsg.
//
// Стили лежат пакетными переменными, поэтому тему применяем разом:
// до ответа терминала работают тёмные значения (в них и записываются демо),
// а когда ответ приходит — стили пересобираются.

var (
	cBorder color.Color
	cAccent color.Color
	cUser   color.Color
	cDim    color.Color
	cErr    color.Color

	stTitle  lipgloss.Style
	stUser   lipgloss.Style
	stBot    lipgloss.Style
	stDim    lipgloss.Style
	stErr    lipgloss.Style
	stFrame  lipgloss.Style
	stFocus  lipgloss.Style
	stStatus lipgloss.Style
	stNote   lipgloss.Style
)

func init() { applyTheme(true) }

// applyTheme пересобирает палитру и стили под светлый или тёмный терминал.
func applyTheme(dark bool) {
	pick := lipgloss.LightDark(dark)

	cBorder = pick(lipgloss.Color("#c8ccd4"), lipgloss.Color("#3b4048"))
	cAccent = pick(lipgloss.Color("#0b6bcb"), lipgloss.Color("#7aa2f7"))
	cUser = pick(lipgloss.Color("#1a7f37"), lipgloss.Color("#9ece6a"))
	cDim = pick(lipgloss.Color("#6b7280"), lipgloss.Color("#7f8694"))
	cErr = pick(lipgloss.Color("#b42318"), lipgloss.Color("#f7768e"))

	stTitle = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	stUser = lipgloss.NewStyle().Bold(true).Foreground(cUser)
	stBot = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	stDim = lipgloss.NewStyle().Foreground(cDim)
	stErr = lipgloss.NewStyle().Foreground(cErr)
	stFrame = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cBorder)
	stFocus = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent)
	stStatus = lipgloss.NewStyle().Foreground(cDim).Padding(0, 1)
	stNote = lipgloss.NewStyle().Foreground(cAccent).Italic(true)
}

// themeFromMsg применяет тему, если сообщение — ответ терминала про фон.
// Возвращает true, когда стили изменились и экран стоит перерисовать.
func themeFromMsg(msg tea.Msg) bool {
	bg, ok := msg.(tea.BackgroundColorMsg)
	if !ok {
		return false
	}
	applyTheme(bg.IsDark())
	return true
}
