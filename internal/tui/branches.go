package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/safronov-a1exander/advent/internal/agent"
)

// Ветки разговора (день 10).
//
// Ctrl+B открывает на месте панели параметров ветки текущего разговора
// и его чекпойнты. Это то же, что «редактировать сообщение и пойти другим
// путём» в чат-клиентах, только точка развилки явная и к ней можно
// возвращаться сколько угодно раз:
//
//   - Ctrl+S — чекпойнт в текущей точке разговора;
//   - Enter на чекпойнте — новая ветка от него, разговор продолжается там;
//   - Enter на ветке — вернуться в неё ровно в том виде, в каком её оставили.
//
// У каждой ветки своя лента на экране, как у каждого агента.

// laneKey — ключ ленты разговора: агент и его активная ветка.
func laneKey(a *agent.Agent) string { return a.ID() + "#" + a.ActiveBranch() }

// dropLanes забывает ленты всех веток агента.
func (m *Model) dropLanes(a *agent.Agent) {
	for k := range m.transcripts {
		if strings.HasPrefix(k, a.ID()+"#") {
			delete(m.transcripts, k)
		}
	}
}

// branchRow — строка списка: ветка или чекпойнт.
type branchRow struct {
	checkpoint bool
	name       string
}

func (m *Model) branchRows() []branchRow {
	var rows []branchRow
	for _, b := range m.ag.Branches() {
		rows = append(rows, branchRow{name: b.Name})
	}
	for _, c := range m.ag.Checkpoints() {
		rows = append(rows, branchRow{checkpoint: true, name: c.Name})
	}
	return rows
}

// openBranches показывает список, курсор — на активной ветке.
func (m *Model) openBranches() tea.Cmd {
	m.branchSel = 0
	for i, b := range m.ag.Branches() {
		if b.Active {
			m.branchSel = i
		}
	}
	m.focus = focusBranches
	m.ta.Blur()
	m.layout()
	return nil
}

func (m *Model) branchesKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	rows := m.branchRows()
	if m.branchSel >= len(rows) {
		m.branchSel = len(rows) - 1
	}
	switch msg.String() {
	case "up":
		if m.branchSel > 0 {
			m.branchSel--
		}
		return nil, true
	case "down":
		if m.branchSel < len(rows)-1 {
			m.branchSel++
		}
		return nil, true
	case "ctrl+s":
		m.checkpointHere()
		return nil, true
	case "enter":
		row := rows[m.branchSel]
		if row.checkpoint {
			m.branchFrom(row.name)
		} else {
			m.switchBranch(row.name)
		}
		if m.flash != "" {
			return nil, true
		}
		return m.closeAgentsList(), true
	case "esc", "ctrl+b":
		return m.closeAgentsList(), true
	}
	return nil, false
}

// branchErr — ошибка веток человеческими словами.
func branchErr(err error) string {
	if errors.Is(err, agent.ErrBusy) {
		return "дождись ответа: пока агент отвечает, разговор не ветвится"
	}
	return err.Error()
}

func (m *Model) checkpointHere() {
	if m.busy.Load() {
		m.flash = branchErr(agent.ErrBusy)
		return
	}
	if len(m.ag.History()) == 0 {
		m.flash = "разговор пуст — ветвиться пока не от чего"
		return
	}
	name, err := m.ag.Checkpoint("")
	if err != nil {
		m.flash = branchErr(err)
		return
	}
	m.pushLine("")
	m.pushLine(stNote.Render(fmt.Sprintf("⎇ чекпойнт «%s» · ветка %s · сообщений %d — Ctrl+B, Enter на нём: новая ветка отсюда",
		name, m.ag.ActiveBranch(), len(m.ag.History()))))
	m.refresh()
	// курсор — на новый чекпойнт, чтобы ветку можно было сразу завести Enter
	m.branchSel = len(m.branchRows()) - 1
}

func (m *Model) branchFrom(cp string) {
	if m.busy.Load() {
		m.flash = branchErr(agent.ErrBusy)
		return
	}
	m.ag.SetConfig(m.set.AgentConfig())
	prev := laneKey(m.ag)
	lines := m.lines
	name, err := m.ag.Branch("", cp)
	if err != nil {
		m.flash = branchErr(err)
		return
	}
	m.transcripts[prev] = lines
	m.lines = replayWith(m.ag, fmt.Sprintf("— ветка «%s» от чекпойнта «%s» · сообщений %d · прежняя ветка отложена, вернуться — Ctrl+B —",
		name, cp, len(m.ag.History())))
	m.follow = true
	m.refresh()
}

func (m *Model) switchBranch(name string) {
	if name == m.ag.ActiveBranch() {
		return
	}
	if m.busy.Load() {
		m.flash = branchErr(agent.ErrBusy)
		return
	}
	m.ag.SetConfig(m.set.AgentConfig())
	prev := laneKey(m.ag)
	lines := m.lines
	if err := m.ag.SwitchBranch(name); err != nil {
		m.flash = branchErr(err)
		return
	}
	m.transcripts[prev] = lines
	m.lines = m.transcripts[laneKey(m.ag)]
	if m.lines == nil {
		// ветку открыли после перезапуска — ленты ещё нет
		m.lines = replayWith(m.ag, fmt.Sprintf("— ветка «%s» · сообщений %d —", name, len(m.ag.History())))
	}
	m.pushLine("")
	m.pushLine(stNote.Render("⎇ снова в ветке «" + name + "»"))
	m.follow = true
	m.refresh()
}

// branchesView — список веток и чекпойнтов.
func (m *Model) branchesView(width int) string {
	var b strings.Builder
	branches, cps := m.ag.Branches(), m.ag.Checkpoints()
	b.WriteString(stDim.Render(short("ветки разговора "+m.ag.ID(), width)) + "\n\n")

	row := 0
	line := func(text string, active bool) {
		cur := row == m.branchSel
		marker := "  "
		if cur {
			marker = "› "
		}
		dot := " "
		if active {
			dot = "●"
		}
		st := lipgloss.NewStyle()
		if cur {
			st = st.Bold(true).Foreground(cAccent)
		}
		b.WriteString(st.Render(short(marker+dot+" "+text, width)) + "\n")
		row++
	}

	for _, br := range branches {
		line(br.Name, br.Active)
		info := fmt.Sprintf("сообщений %d", br.Messages)
		if br.From != "" {
			info = "от «" + br.From + "» · " + info
		}
		b.WriteString(stDim.Render("    "+short(info, width-4)) + "\n")
		if br.Last != "" {
			b.WriteString(stDim.Render("    "+short("«"+br.Last+"»", width-4)) + "\n")
		}
	}

	b.WriteString("\n" + stDim.Render("чекпойнты") + "\n")
	if len(cps) == 0 {
		b.WriteString(stDim.Render(short("  пока нет — Ctrl+S снимет в текущей точке", width)) + "\n")
	}
	for _, c := range cps {
		line("⎇ "+c.Name, false)
		b.WriteString(stDim.Render("    "+short(fmt.Sprintf("в «%s» · сообщений %d · %s", c.Branch, c.Messages, when(c.At)), width-4)) + "\n")
	}
	return b.String()
}
