package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// screen — то, что окажется в терминале: строки без ANSI-кодов.
func screen(m *Model) []string {
	return strings.Split(ansi.Strip(m.View().Content), "\n")
}

// Панель с параметрами всех дней длиннее окна в 40 строк. Экран всё равно
// не выше окна: блокнот и поле ввода остаются на месте (день 31).
func TestSidePanelDoesNotPushInputOffScreen(t *testing.T) {
	m := helpModel(t, nil)
	m.notes.lines = []string{"заметка в блокноте"}
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 40})
	if n := len(m.panel.fields); n < 30 {
		t.Skipf("полей всего %d — панель и так помещается", n)
	}

	rows := screen(m)
	if len(rows) > 40 {
		t.Fatalf("экран %d строк при окне 40", len(rows))
	}
	all := strings.Join(rows, "\n")
	if !strings.Contains(all, "блокнот") || !strings.Contains(all, "↓ ещё") {
		t.Fatalf("нет блокнота или пометки о скрытых полях:\n%s", all)
	}

	// Последнее поле выбрано — окно панели прокручено к нему.
	press(t, m, "tab")
	for range m.panel.fields {
		press(t, m, "down")
	}
	rows = screen(m)
	last := m.panel.fields[len(m.panel.fields)-1].Label
	if len(rows) > 40 || !strings.Contains(strings.Join(rows, "\n"), last) {
		t.Fatalf("выбранное поле %q не видно или экран вырос (%d строк)", last, len(rows))
	}
	if !strings.Contains(strings.Join(rows, "\n"), "↑ ещё") {
		t.Fatal("наверху должна быть пометка о прокрученных полях")
	}
}
