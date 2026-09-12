package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/safronov-a1exander/advent/internal/agent"
)

// Список агентов — переключатель разговоров, как вкладки или сессии
// в обычном чат-клиенте.
//
// Ctrl+O открывает его на месте панели параметров. У каждого агента своя
// история и своя лента на экране, поэтому переключение — это возврат
// к разговору ровно в том виде, в каком его оставили, а не смена
// собеседника посреди общей переписки.

// openAgents показывает список, курсор — на текущем агенте.
func (m *Model) openAgents() tea.Cmd {
	m.agentSel = 0
	for i, a := range m.pool.List() {
		if a == m.ag {
			m.agentSel = i
		}
	}
	m.focus = focusAgents
	m.ta.Blur()
	m.layout()
	return nil
}

func (m *Model) closeAgentsList() tea.Cmd {
	m.focus = focusInput
	m.layout()
	return m.ta.Focus()
}

// agentsKey — клавиши, пока открыт список. Вторым значением сообщает,
// забрал ли список клавишу.
func (m *Model) agentsKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	list := m.pool.List()
	if m.agentSel >= len(list) {
		m.agentSel = len(list) - 1
	}
	switch msg.String() {
	case "up":
		if m.agentSel > 0 {
			m.agentSel--
		}
		return nil, true
	case "down":
		if m.agentSel < len(list)-1 {
			m.agentSel++
		}
		return nil, true
	case "enter":
		if list[m.agentSel] == m.ag {
			return m.closeAgentsList(), true
		}
		if m.busy.Load() {
			m.flash = "дождись ответа: пока агент отвечает, разговор не переключается"
			return nil, true
		}
		m.activate(list[m.agentSel])
		return m.closeAgentsList(), true
	case "ctrl+n":
		if m.busy.Load() {
			m.flash = "дождись ответа, потом заводи нового агента"
			return nil, true
		}
		m.newAgent()
		return m.closeAgentsList(), true
	case "ctrl+w", "delete":
		m.closeAgent(list[m.agentSel])
		return nil, true
	case "esc", "ctrl+o":
		return m.closeAgentsList(), true
	}
	return nil, false
}

// activate делает агента текущим: панель показывает его конфиг,
// лента — его переписку.
func (m *Model) activate(a *agent.Agent) {
	if a == m.ag {
		return
	}
	m.ag.SetConfig(m.set.AgentConfig())
	m.transcripts[m.ag.ID()] = m.lines
	m.ag = a
	m.lines = m.transcripts[a.ID()]
	m.set.LoadConfig(a.Config())
	m.panel.Changed = false
	m.follow = true
	m.refresh()
}

// newAgent — новый агент с теми же настройками и пустой историей.
// Прежний остаётся в пуле со всей перепиской, вернуться — через список.
func (m *Model) newAgent() {
	a := m.spawn()
	m.transcripts[a.ID()] = []string{stDim.Render(fmt.Sprintf(
		"— новый агент %s: настройки скопированы, история пуста · Ctrl+O — список агентов —", a.ID()))}
	m.activate(a)
}

// closeAgent убирает агента из пула. Последнего не закрываем — говорить
// будет не с кем; отвечающего тоже — его ответ пропал бы в никуда.
func (m *Model) closeAgent(a *agent.Agent) {
	list := m.pool.List()
	switch {
	case len(list) == 1:
		m.flash = "это последний агент — закрывать некого"
		return
	case a.Busy() || m.busy.Load():
		// m.busy — ещё и сравнение по Ctrl+E: его строки пишутся в ленту
		// текущего агента, и уход с неё посреди прогона их потерял бы
		m.flash = "идёт ответ — закрыть можно после него"
		return
	}
	if a == m.ag {
		// переходим к соседу, чтобы экран не остался без разговора
		for i, x := range list {
			if x == a {
				next := list[(i+1)%len(list)]
				if i == len(list)-1 {
					next = list[i-1]
				}
				m.activate(next)
				break
			}
		}
	}
	m.pool.Remove(a.ID())
	delete(m.transcripts, a.ID())
	if m.agentSel >= m.pool.Len() {
		m.agentSel = m.pool.Len() - 1
	}
}

// agentsView — сам список. Ширина та же, что у панели параметров.
func (m *Model) agentsView(width int) string {
	var b strings.Builder
	b.WriteString(stDim.Render(short(fmt.Sprintf("агенты · %d", m.pool.Len()), width)) + "\n\n")

	for i, a := range m.pool.List() {
		cur := i == m.agentSel
		marker := "  "
		if cur {
			marker = "› "
		}
		active := " "
		if a == m.ag {
			active = "●"
		}
		head := marker + active + " " + a.ID()
		if a.Busy() {
			head += " …"
		}
		st := lipgloss.NewStyle()
		if cur {
			st = st.Bold(true).Foreground(cAccent)
		}
		b.WriteString(st.Render(short(head, width)) + "\n")

		title := a.Title()
		if title == "" {
			title = "новый разговор"
		}
		cfg := a.Config()
		b.WriteString(stDim.Render("    "+short(title, width-4)) + "\n")
		b.WriteString(stDim.Render("    "+short(fmt.Sprintf("%s · сообщений %d", cfg.Model, len(a.History())), width-4)) + "\n")
		if i < m.pool.Len()-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}
