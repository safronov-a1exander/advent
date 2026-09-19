package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/memory"
	"github.com/safronov-a1exander/advent/internal/task"
)

// Слои памяти в интерфейсе (день 11).
//
// Ctrl+M открывает на месте панели параметров три слоя памяти агента:
// краткосрочный (этот разговор), рабочий (задача) и долговременный
// (пользователь). Там же видно, кто положил запись — пользователь или
// служебная раскладка.
//
// Класть руками — не из панели, а прямо из поля ввода:
//
//	/user стек = Go          → в долговременный слой
//	/task срок = 6 недель    → в рабочий
//	/chat остановились = …   → в краткосрочный
//	/forget task срок        → убрать
//
// Так сделано потому, что запись памяти — это часть разговора: пользователь
// уже печатает, и уводить его в форму ради двух полей незачем. Панель при
// этом остаётся: она отвечает на вопрос дня «какие данные попадают в каждый
// слой», а Del в ней убирает ошибочную запись.

// memoryRow — строка списка: заголовок слоя или запись.
type memoryRow struct {
	scope  memory.Scope
	header bool
	entry  memory.Entry
}

func (m *Model) memoryRows() []memoryRow {
	var rows []memoryRow
	mem := m.ag.Memory()
	for _, s := range memory.Scopes {
		rows = append(rows, memoryRow{scope: s, header: true})
		for _, e := range mem.Layer(s).Entries() {
			rows = append(rows, memoryRow{scope: s, entry: e})
		}
	}
	return rows
}

// openMemory показывает слои. Курсор встаёт на первую запись, а не на
// заголовок: чаще всего сюда заходят удалить кривую запись.
func (m *Model) openMemory() tea.Cmd {
	if m.ag.Memory() == nil {
		m.flash = "память выключена — включи поле «память» в панели (Ctrl+P)"
		return nil
	}
	m.memSel = 0
	for i, r := range m.memoryRows() {
		if !r.header {
			m.memSel = i
			break
		}
	}
	m.focus = focusMemory
	m.ta.Blur()
	m.layout()
	return nil
}

func (m *Model) memoryKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	rows := m.memoryRows()
	if m.memSel >= len(rows) {
		m.memSel = len(rows) - 1
	}
	switch msg.String() {
	case "up":
		if m.memSel > 0 {
			m.memSel--
		}
		return nil, true
	case "down":
		if m.memSel < len(rows)-1 {
			m.memSel++
		}
		return nil, true
	case "delete", "backspace":
		if m.memSel < len(rows) && !rows[m.memSel].header {
			row := rows[m.memSel]
			if err := m.ag.Forget(row.scope, row.entry.Key); err != nil {
				m.flash = err.Error()
				return nil, true
			}
			m.pushLine("")
			m.pushLine(stNote.Render(fmt.Sprintf("🧠 забыто · %s/%s", row.scope, row.entry.Key)))
			m.refresh()
		}
		return nil, true
	case "esc", "ctrl+m":
		return m.closeAgentsList(), true
	}
	return nil, false
}

// memoryCommand разбирает команду памяти из поля ввода. Возвращает false,
// если строка — обычная реплика.
func (m *Model) memoryCommand(text string) bool {
	if !strings.HasPrefix(text, "/") {
		return false
	}
	head, rest, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	head = strings.ToLower(strings.TrimSpace(head))
	rest = strings.TrimSpace(rest)

	// День 12: профиль переключается той же строкой, что и память
	// кладётся, — из поля ввода. Иначе смена профиля означала бы поход
	// в панель и десяток нажатий вниз, а менять его хочется часто:
	// вся суть персонализации видна только в сравнении.
	if head == "profile" {
		m.profileCommand(rest)
		return true
	}
	// День 13: стадии задачи двигаются теми же командами из поля ввода.
	if head == "stage" {
		m.stageCommand(rest)
		return true
	}
	if head == "step" {
		m.stepCommand(rest)
		return true
	}
	if head == "forget" {
		scope, key, _ := strings.Cut(rest, " ")
		m.forgetCommand(memory.Scope(strings.ToLower(strings.TrimSpace(scope))), strings.TrimSpace(key))
		return true
	}
	scope := memory.Scope(head)
	if !scope.Valid() {
		return false // не команда памяти — пусть уходит как реплика
	}
	key, value, ok := strings.Cut(rest, "=")
	if !ok {
		m.flash = "формат: /" + head + " ключ = значение"
		return true
	}
	if m.ag.Memory() == nil {
		m.flash = "память выключена — включи поле «память» в панели (Ctrl+P)"
		return true
	}
	key, value = strings.TrimSpace(key), strings.TrimSpace(value)
	if err := m.ag.Remember(scope, key, value); err != nil {
		m.flash = err.Error()
		return true
	}
	m.pushLine("")
	m.pushLine(stNote.Render(fmt.Sprintf("🧠 %s · %s: %s", scope.Label(), key, value)))
	m.refresh()
	return true
}

// profileCommand переключает профиль. Пустой аргумент снимает профиль —
// так в демо показывают «а вот как то же самое без персонализации».
func (m *Model) profileCommand(id string) {
	id = strings.TrimSpace(id)
	m.set.Profile = id
	cfg := m.set.AgentConfig()
	m.ag.SetConfig(cfg)

	prof := m.ag.Profile()
	switch {
	case id == "":
		m.pushLine("")
		m.pushLine(stNote.Render("👤 профиль снят — дальше без персонализации"))
	case prof == nil:
		// Конфиг мы уже поменяли: агент честно останется без профиля,
		// и об этом надо сказать, а не делать вид, что всё хорошо.
		m.flash = "профиль «" + id + "» не прочитался — работаем без него"
		return
	default:
		m.pushLine("")
		m.pushLine(stNote.Render("👤 профиль: " + prof.Summary()))
		if pl, ok := prof.Default(); ok && len(pl.Stages) > 0 {
			m.pushLine(stDim.Render("   дорога по умолчанию «" + pl.Name + "»: " + strings.Join(pl.Stages, " → ")))
		}
	}
	m.refresh()
}

// stageCommand переводит задачу в стадию. Без аргумента — показывает,
// на чём остановились: чаще всего именно это и нужно, когда вернулся.
func (m *Model) stageCommand(arg string) {
	tk := m.ag.Task()
	if tk == nil {
		m.flash = "у агента нет задачи — включи поле «задача» в панели (Ctrl+P)"
		return
	}
	arg = strings.TrimSpace(arg)
	if arg == "" {
		m.pushLine("")
		m.pushLine(stNote.Render("◆ " + tk.Resume()))
		m.refresh()
		return
	}
	to, note, _ := strings.Cut(arg, " ")
	if err := m.ag.Stage(task.State(strings.ToLower(strings.TrimSpace(to))), strings.TrimSpace(note)); err != nil {
		m.flash = err.Error()
		return
	}
	m.pushLine("")
	m.pushLine(stNote.Render("◆ стадия: " + m.ag.Task().Summary()))
	m.refresh()
}

// stepCommand отмечает шаг сделанным.
func (m *Model) stepCommand(what string) {
	if m.ag.Task() == nil {
		m.flash = "у агента нет задачи"
		return
	}
	if err := m.ag.Step(strings.TrimSpace(what)); err != nil {
		m.flash = err.Error()
		return
	}
	m.pushLine("")
	m.pushLine(stNote.Render("◆ шаг закрыт: " + m.ag.Task().Summary()))
	m.refresh()
}

func (m *Model) forgetCommand(scope memory.Scope, key string) {
	switch {
	case !scope.Valid():
		m.flash = "формат: /forget chat|task|user ключ"
	case key == "":
		m.flash = "какой ключ забыть? /forget " + string(scope) + " ключ"
	case m.ag.Memory() == nil:
		m.flash = "память выключена"
	default:
		if _, ok := m.ag.Memory().Layer(scope).Get(key); !ok {
			m.flash = fmt.Sprintf("в слое %s нет ключа «%s»", scope, key)
			return
		}
		if err := m.ag.Forget(scope, key); err != nil {
			m.flash = err.Error()
			return
		}
		m.pushLine("")
		m.pushLine(stNote.Render(fmt.Sprintf("🧠 забыто · %s/%s", scope, key)))
		m.refresh()
	}
}

// memoryView — три слоя со всем, что в них лежит.
func (m *Model) memoryView(width int) string {
	var b strings.Builder
	mem := m.ag.Memory()
	cfg := m.ag.Config()

	b.WriteString(stDim.Render(short("память агента "+m.ag.ID(), width)) + "\n")
	who := []string{}
	if cfg.User != "" {
		who = append(who, "юзер "+cfg.User)
	}
	if cfg.Task != "" {
		who = append(who, "задача "+cfg.Task)
	}
	if len(who) > 0 {
		b.WriteString(stDim.Render(short(strings.Join(who, " · "), width)) + "\n")
	}
	b.WriteString("\n")

	row := 0
	for _, s := range memory.Scopes {
		entries := mem.Layer(s).Entries()
		title := fmt.Sprintf("%s (%s) · %d", s, s.Label(), len(entries))
		st := stDim
		if row == m.memSel {
			st = lipgloss.NewStyle().Foreground(cAccent)
		}
		b.WriteString(st.Render(short("  "+title, width)) + "\n")
		row++

		if len(entries) == 0 {
			b.WriteString(stDim.Render(short("      пусто", width)) + "\n")
			continue
		}
		for _, e := range entries {
			cur := row == m.memSel
			marker := "  "
			est := lipgloss.NewStyle()
			if cur {
				marker, est = "› ", est.Bold(true).Foreground(cAccent)
			}
			b.WriteString(est.Render(short(marker+"  "+e.Key+": "+e.Value, width)) + "\n")
			// Источник важен: раскладка служебным вызовом ошибается, и по метке
			// видно, что смотреть в первую очередь.
			b.WriteString(stDim.Render(short("      "+string(e.Source), width)) + "\n")
			row++
		}
	}

	b.WriteString("\n" + stDim.Render(short("класть — /user ключ = значение, /task …, /chat …", width)) + "\n")
	b.WriteString(stDim.Render(short("убирать — Del здесь или /forget task ключ", width)) + "\n")
	return b.String()
}

// memoryHeader — сводка слоёв для шапки экрана.
func memoryHeader(a *agent.Agent) string {
	mem := a.Memory()
	if mem == nil {
		return ""
	}
	return "🧠 " + mem.Summary()
}
