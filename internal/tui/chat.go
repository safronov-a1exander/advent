// Package tui — терминальный интерфейс стенда на bubbletea.
//
// День 1: слева поток диалога, справа панель параметров запроса,
// снизу ввод и строка состояния с задержкой, токенами и стоимостью.
// Все параметры правятся не перезапуском с флагами, а прямо на экране.
//
// День 6: экран больше не разговаривает с моделью сам. Историю, сборку
// запроса и вызов API держит агент (пакет agent), а экран только показывает
// его ответы и правит его конфиг. Агентов в пуле может быть сколько угодно:
// Ctrl+N заводит нового, Ctrl+O открывает список разговоров, Ctrl+D
// показывает, что внутри текущего агента.
package tui

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/safronov-a1exander/advent/internal/agent"
)

// ---- сообщения ----

// eventMsg — событие агента по ходу ответа: шаг цепочки или кусок текста.
type eventMsg agent.Event

// doneMsg — агент закончил отвечать.
type doneMsg struct {
	reply *agent.Reply
	err   error
}

// NoteMsg — строка для блокнота под окном диалога. Демо-сценарий шлёт её
// командой `note`, и текст набирается посимвольно. В саму переписку
// комментарии не попадают: там остаётся только разговор с моделью.
type NoteMsg string

// defaultTitle — имя продукта: на экране должен быть ассистент,
// а не служебное название экрана.
const defaultTitle = "Бюджет · ассистент по личным тратам"

// ---- модель ----

// Options — то, что нужно экрану чата для работы.
type Options struct {
	// Pool — откуда брать агентов. Первый агент порождается из Settings.
	Pool     *agent.Pool
	Provider string
	Settings *Settings
	Title    string
}

type focusTarget int

const (
	focusInput focusTarget = iota
	focusPanel
	focusNotes
	focusAgents // список агентов на месте панели параметров
)

// notesHeight — сколько строк блокнота видно одновременно.
const notesHeight = 3

// Model — состояние экрана.
type Model struct {
	opts  Options
	set   *Settings
	panel *Panel

	notes     *Notes
	vp        viewport.Model
	ta        textarea.Model
	sp        spinner.Model
	help      help.Model
	keys      chatKeys
	panelKeys panelKeys
	editKeys  editKeys
	notesKeys notesKeys
	agentKeys agentKeys
	ready     bool
	w, h      int
	focus     focusTarget
	showPanel bool
	// follow: вьюпорт сам прокручивается к новым строкам. Сбрасывается,
	// когда читатель отлистал вверх, и возвращается на низу.
	follow bool

	// pool и ag — с кем разговариваем. Экран не хранит историю: она у агента.
	// Панель правит конфиг, и перед каждым вопросом он уходит в агента,
	// поэтому правка системного промпта действует сразу.
	pool *agent.Pool
	ag   *agent.Agent
	// transcripts — ленты неактивных агентов. Лента текущего живёт в lines;
	// при переключении она откладывается сюда, а на экран встаёт чужая.
	transcripts map[string][]string
	agentSel    int
	// flash — короткое пояснение в строке состояния до следующей клавиши:
	// почему нажатие ничего не сделало.
	flash string

	lines   []string
	partial strings.Builder

	busy   atomic.Bool
	stream chan tea.Msg
	cancel context.CancelFunc
	// lastQuestion — последний заданный вопрос; по Ctrl+E он уходит
	// на все модели сразу (задание дня 5).
	lastQuestion string
}

func NewModel(o Options) *Model {
	ta := textarea.New()
	ta.Placeholder = "спроси что-нибудь… (Enter — отправить, Ctrl+J — перенос строки)"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(3)
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(cAccent)

	m := &Model{
		opts: o, set: o.Settings, ta: ta, sp: sp, showPanel: true,
		follow:    true,
		help:      newHelp(),
		keys:      newChatKeys(len(o.Settings.Catalog) > 1),
		panelKeys: newPanelKeys("в диалог"),
		editKeys:  newEditKeys(),
		notesKeys: newNotesKeys("вернуться в диалог"),
		agentKeys: newAgentKeys(),
	}
	m.pool = o.Pool
	m.transcripts = map[string][]string{}
	m.ag = m.spawn()
	m.panel = NewPanel(m.set.Fields(), 34)
	m.notes = NewNotes(60, notesHeight)
	return m
}

func (m *Model) Init() tea.Cmd {
	// Спрашиваем у терминала цвет фона: в lipgloss v2 нет AdaptiveColor,
	// тему приложение выбирает само (см. theme.go).
	return tea.Batch(tea.RequestBackgroundColor, m.sp.Tick)
}

// Busy сообщает демо-драйверу, ждать ли ответ.
func (m *Model) Busy() bool { return m.busy.Load() }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if themeFromMsg(msg) {
		m.refresh()
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
		m.ready = true
		return m, nil

	case tea.KeyPressMsg:
		return m.onKey(msg)

	case NoteMsg:
		cmd := m.notes.Type(string(msg))
		m.layout()
		return m, cmd

	case noteTickMsg:
		return m, m.notes.Tick()

	case benchStepMsg:
		row := benchRow(msg)
		if row.Err != "" {
			m.pushLine(stErr.Render("  " + row.Model + ": " + row.Err))
		} else {
			m.pushLine(stBot.Render("  "+row.Model) + " " +
				stDim.Render(fmt.Sprintf("%s · in %d / out %d · $%.6f",
					row.Latency.Round(time.Millisecond),
					row.Usage.PromptTokens, row.Usage.CompletionTokens, row.Cost)))
			m.pushLine(stDim.Render("  " + shorten(row.Answer, 300)))
		}
		m.refresh()
		return m, m.waitChunk()

	case benchDoneMsg:
		m.busy.Store(false)
		m.pushLine("")
		for _, line := range benchTable(msg.rows) {
			m.pushLine(line)
		}
		m.pushLine(stDim.Render("сравнение записано в журнал runs/*.jsonl"))
		m.refresh()
		return m, nil

	case eventMsg:
		m.onEvent(agent.Event(msg))
		return m, m.waitChunk()

	case doneMsg:
		m.busy.Store(false)
		m.finish(msg.reply, msg.err)
		return m, nil

	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.follow = m.vp.AtBottom()
		return m, cmd

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m *Model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Ctrl+C работает всегда, даже посреди редактирования поля.
	if msg.String() == "ctrl+c" {
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}

	m.flash = ""
	if m.focus == focusAgents {
		if cmd, handled := m.agentsKey(msg); handled {
			return m, cmd
		}
		return m, nil
	}

	if m.focus == focusPanel {
		if cmd, handled := m.panel.Update(msg); handled {
			m.refreshPanelWidth()
			return m, cmd
		}
		// панель не забрала клавишу — обрабатываем общие
	}

	// Esc из любого места возвращает в поле ввода. Считать нажатия Tab,
	// чтобы вернуться к диалогу, неудобно и человеку, и демо-сценарию.
	if msg.String() == "esc" && !m.panel.Editing() && m.focus != focusInput {
		m.focus = focusInput
		m.layout()
		return m, m.ta.Focus()
	}

	// Tab водит по кругу: ввод → параметры → блокнот → ввод.
	if msg.String() == "tab" && !m.panel.Editing() {
		switch {
		case m.focus == focusInput && m.showPanel:
			m.focus = focusPanel
			m.ta.Blur()
		case m.focus == focusInput || m.focus == focusPanel:
			m.focus = focusNotes
			m.ta.Blur()
			m.layout()
		default:
			m.focus = focusInput
			m.layout()
			return m, m.ta.Focus()
		}
		return m, nil
	}

	if m.focus == focusNotes {
		if m.notes.Key(msg) {
			m.layout()
			return m, nil
		}
	}

	switch msg.String() {

	case "ctrl+p":
		m.showPanel = !m.showPanel
		if !m.showPanel && m.focus == focusPanel {
			m.focus = focusInput
			m.layout()
			return m, m.ta.Focus()
		}
		m.layout()
		return m, nil

	case "ctrl+l":
		m.lines = nil
		m.refresh()
		return m, nil

	case "ctrl+r":
		m.resetConversation()
		return m, nil

	case "ctrl+d":
		m.dumpAgent()
		return m, nil

	case "ctrl+n":
		if m.busy.Load() {
			m.flash = "дождись ответа, потом заводи нового агента"
			return m, nil
		}
		m.newAgent()
		if m.focus != focusInput {
			m.focus = focusInput
			m.layout()
			return m, m.ta.Focus()
		}
		return m, nil

	case "ctrl+o":
		return m, m.openAgents()

	case "ctrl+e":
		if m.busy.Load() {
			return m, nil
		}
		return m, m.startBench()
	}

	if m.focus != focusInput {
		return m, nil
	}

	switch msg.String() {
	case "enter":
		if m.busy.Load() {
			return m, nil
		}
		text := strings.TrimSpace(m.ta.Value())
		if text == "" {
			return m, nil
		}
		m.ta.Reset()
		m.follow = true
		return m, m.send(text)
	case "ctrl+j":
		m.ta.InsertString("\n")
		return m, nil
	case "end":
		m.vp.GotoBottom()
		m.follow = true
		return m, nil
	case "home":
		m.vp.GotoTop()
		m.follow = false
		return m, nil
	case "pgup", "pgdown", "shift+up", "shift+down":
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		// Вернулись к низу — снова следуем за новыми строками.
		m.follow = m.vp.AtBottom()
		return m, cmd
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m *Model) resetConversation() {
	m.ag.Reset()
	m.pushLine("")
	m.pushLine(stDim.Render("— контекст агента " + m.ag.ID() + " сброшен (счётчики расхода не обнуляются) —"))
	m.refresh()
}

// spawn порождает агента из текущих настроек панели.
func (m *Model) spawn() *agent.Agent {
	cfg := m.set.AgentConfig()
	cfg.Name = "бюджет"
	return m.pool.Spawn(cfg)
}

// dumpAgent — Ctrl+D: что внутри агента и что именно уйдёт в API
// следующим запросом. Для видео это и есть «дебаг-данные» шестого дня.
func (m *Model) dumpAgent() {
	m.ag.SetConfig(m.set.AgentConfig())
	cfg := m.ag.Config()
	st := m.ag.Stats()
	stack := m.ag.Messages("<следующий вопрос>")

	m.pushLine("")
	m.pushLine(stBot.Render("▸ агент " + m.ag.ID()))
	sum := cfg.Summary()
	if sum == "" {
		sum = "параметры по умолчанию"
	}
	m.pushLine(stDim.Render(fmt.Sprintf("  конфиг   модель %s · %s", cfg.Model, sum)))
	m.pushLine(stDim.Render(fmt.Sprintf("  расход   ходов %d · вызовов %d · ошибок %d · in %d (кэш %d) · out %d · reasoning %d · $%.6f",
		st.Turns, st.Calls, st.Errors, st.Prompt, st.Cached, st.Completion, st.Reasoning, st.CostUSD)))
	m.pushLine(stDim.Render(fmt.Sprintf("  стек     %d сообщений уйдёт в API со следующим вопросом:", len(stack))))
	for i, msg := range stack {
		m.pushLine(stDim.Render(fmt.Sprintf("    %2d %-9s %5d симв.  %s",
			i+1, msg.Role, len([]rune(msg.Content)), shorten(oneLine(msg.Content), 70))))
	}
	others := m.pool.Len() - 1
	if others > 0 {
		m.pushLine(stDim.Render(fmt.Sprintf("  в пуле ещё агентов: %d — у каждого свой конфиг и своя история", others)))
	}
	m.refresh()
}

func (m *Model) send(text string) tea.Cmd {
	// Панель живёт в главном цикле и может поменяться, пока агент отвечает,
	// поэтому конфиг отдаём агенту до старта, а подпись снимаем здесь же.
	cfg := m.set.AgentConfig()
	m.ag.SetConfig(cfg)
	ag := m.ag
	m.lastQuestion = text

	m.pushLine("")
	m.pushLine(stUser.Render("вы:"))
	m.pushLine(text)
	m.pushLine("")
	head := stBot.Render(cfg.Model + ":")
	if m.pool.Len() > 1 {
		head = stBot.Render(ag.ID()+" · "+cfg.Model) + stBot.Render(":")
	}
	if sum := cfg.Summary(); sum != "" {
		head += " " + stDim.Render(sum)
	}
	if n := len(agent.Chain(cfg.Strategy)); n > 1 {
		head += " " + stDim.Render(fmt.Sprintf("· %d вызова(ов)", n))
	}
	m.pushLine(head)
	m.partial.Reset()
	m.busy.Store(true)
	m.panel.Changed = false

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	m.cancel = cancel
	ch := make(chan tea.Msg, 256)
	m.stream = ch

	go func() {
		defer cancel()
		reply, err := ag.Ask(ctx, text, func(e agent.Event) { ch <- eventMsg(e) })
		ch <- doneMsg{reply: reply, err: err}
	}()

	m.refresh()
	return tea.Batch(m.waitChunk(), m.sp.Tick)
}

// onEvent показывает то, что агент сообщает по ходу ответа.
func (m *Model) onEvent(e agent.Event) {
	switch e.Kind {
	case agent.EventStep:
		m.pushLine(stBot.Render("▸ " + e.Label))
		m.pushLine(stDim.Render(shorten(e.Content, 600)))
		m.pushLine(stDim.Render(fmt.Sprintf("  ↳ %s · in %d / out %d · $%.6f",
			e.Latency.Round(time.Millisecond), e.Usage.PromptTokens, e.Usage.CompletionTokens, e.CostUSD)))
		m.pushLine("")
	case agent.EventFinalStart:
		m.pushLine(stBot.Render("▸ " + e.Label))
	case agent.EventChunk:
		m.partial.WriteString(e.Content)
	}
	m.refresh()
}

func (m *Model) waitChunk() tea.Cmd {
	ch := m.stream
	return func() tea.Msg { return <-ch }
}

func (m *Model) finish(reply *agent.Reply, err error) {
	body := m.partial.String()
	m.partial.Reset()

	if err != nil {
		if body != "" {
			m.pushLine(body)
		}
		m.pushLine(stErr.Render("ошибка: " + err.Error()))
		m.pushLine(stDim.Render("  вопрос в историю агента не попал — его можно задать заново"))
		m.refresh()
		return
	}

	resp := reply.Final
	m.pushLine(body)
	m.pushLine(stDim.Render(fmt.Sprintf(
		"  ↳ finish_reason=%s  latency=%s  prompt=%d  completion=%d  reasoning=%d  $%.6f",
		resp.FinishReason, resp.Latency.Round(time.Millisecond),
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
		resp.Usage.ReasoningTokens, resp.CostUSD)))
	m.refresh()
}

// ---- отрисовка ----

func (m *Model) pushLine(s string) { m.lines = append(m.lines, s) }

func (m *Model) panelW() int {
	if !m.showPanel && m.focus != focusAgents {
		return 0
	}
	w := m.w / 3
	if w < 30 {
		w = 30
	}
	if w > 40 {
		w = 40
	}
	if m.w-w < 40 {
		return 0 // окно слишком узкое — панель прячем
	}
	return w
}

func (m *Model) refreshPanelWidth() {
	if pw := m.panelW(); pw > 0 {
		m.panel.SetWidth(pw - 2)
	}
	m.refresh()
}

// notesVisible — блокнот показываем, только когда в нём что-то есть
// или в него сейчас пишут. Пустая рамка на экране ни к чему.
func (m *Model) notesVisible() bool {
	return m.focus == focusNotes || !m.notes.Empty()
}

func (m *Model) layout() {
	// header(1) + пустая(1) + рамка вьюпорта(2) + ввод 3 строки + рамка(2) + статус(1)
	const chrome = 10
	vpH := m.h - chrome
	if m.notesVisible() {
		vpH -= notesHeight + 2
	}
	if vpH < 3 {
		vpH = 3
	}
	pw := m.panelW()
	bodyW := m.w - 2
	if pw > 0 {
		bodyW = m.w - pw - 4
	}
	if bodyW < 20 {
		bodyW = 20
	}
	if !m.ready {
		m.vp = viewport.New(viewport.WithWidth(bodyW), viewport.WithHeight(vpH))
	} else {
		m.vp.SetWidth(bodyW)
		m.vp.SetHeight(vpH)
	}
	m.ta.SetWidth(m.w - 4)
	m.notes.SetSize(m.w-4, notesHeight)
	m.refreshPanelWidth()
}

func (m *Model) refresh() {
	body := strings.Join(m.lines, "\n")
	if p := m.partial.String(); p != "" {
		body += "\n" + p
	}
	w := m.vp.Width() - 2
	if w < 20 {
		w = 20
	}
	m.vp.SetContent(lipgloss.NewStyle().Width(w).Render(body))
	// К низу только если читатель там и был: отлистал вверх посреди ответа —
	// очередной кусок текста не должен дёргать экран обратно.
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m *Model) View() tea.View {
	if !m.ready {
		v := tea.NewView("инициализация…")
		v.AltScreen = true
		return v
	}
	title := m.opts.Title
	if title == "" {
		title = defaultTitle
	}
	header := stTitle.Render(title) + "  " +
		stDim.Render(fmt.Sprintf("%s / %s", m.opts.Provider, m.set.Model))
	if n := m.pool.Len(); n > 1 {
		header += "  " + stNote.Render(fmt.Sprintf("агент %s · в пуле %d", m.ag.ID(), n))
	}

	transcript := m.frame(m.focus == focusInput).Width(m.vp.Width() + 2).Render(m.vp.View())
	body := transcript
	if pw := m.panelW(); pw > 0 {
		// Рамка панели по высоте содержимого, а не во весь экран: параметров
		// немного, и высокий пустой прямоугольник справа смотрится как брак.
		side := m.panel.View(m.focus == focusPanel)
		if m.focus == focusAgents {
			side = m.agentsView(pw - 2)
		}
		panel := m.frame(m.focus == focusPanel || m.focus == focusAgents).Width(pw).Render(side)
		body = lipgloss.JoinHorizontal(lipgloss.Top, transcript, panel)
	}

	rows := []string{header, "", body}
	if m.notesVisible() {
		rows = append(rows, titledFrame(m.frame(m.focus == focusNotes), m.borderColor(m.focus == focusNotes), m.w-2,
			"блокнот", m.notes.View(m.focus == focusNotes)))
	}
	rows = append(rows,
		m.frame(m.focus == focusInput).Width(m.w-2).Render(m.ta.View()),
		stStatus.Render(m.status()))
	// В v2 альт-экран и мышь — свойства вида, а не опции программы.
	v := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, rows...))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// borderColor — цвет рамки под текущий фокус; нужен titledFrame,
// который рисует верхнюю границу сам.
func (m *Model) borderColor(focused bool) color.Color {
	if focused {
		return cAccent
	}
	return cBorder
}

func (m *Model) frame(focused bool) lipgloss.Style {
	if focused {
		return stFocus
	}
	return stFrame
}

func (m *Model) status() string {
	// Подсказка собирается из тех же привязок, по которым работают клавиши,
	// поэтому не может разойтись с поведением (см. keys.go).
	var left string
	switch {
	case m.busy.Load():
		left = m.sp.View() + " ждём ответ…"
	case m.panel.Editing():
		left = shortHelp(m.help, m.editKeys.Apply, m.editKeys.Cancel)
	case m.focus == focusPanel:
		left = shortHelp(m.help, m.panelKeys.Field, m.panelKeys.Value,
			m.panelKeys.Edit, m.panelKeys.Cycle, m.panelKeys.Back)
	case m.focus == focusNotes:
		left = shortHelp(m.help, m.notesKeys.Line, m.notesKeys.Back)
	case m.focus == focusAgents:
		left = shortHelp(m.help, m.agentKeys.Move, m.agentKeys.Open,
			m.agentKeys.New, m.agentKeys.Close, m.agentKeys.Back)
	default:
		left = shortHelp(m.help, m.keys.Send, m.keys.Scroll, m.keys.Cycle,
			m.keys.Spawn, m.keys.Switch, m.keys.Debug, m.keys.Bench, m.keys.Reset, m.keys.Quit)
	}
	// Пока читатель отлистан вверх, новые строки уходят вниз незаметно —
	// подсказываем, чем вернуться.
	if !m.follow {
		left = stNote.Render("↑ отлистано, End — к последнему ответу") + "  " + left
	}
	if m.flash != "" {
		left = stErr.Render(m.flash)
	}
	// Расход всего пула: сюда же попадают вызовы агентов сравнения по Ctrl+E.
	sp := m.pool.Spent()
	right := fmt.Sprintf("вызовов %d · токенов %d↑ %d↓ · $%.6f",
		sp.Calls, sp.Prompt, sp.Completion, sp.CostUSD)
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func shorten(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func short(s string, n int) string {
	r := []rune(s)
	if n < 1 {
		return ""
	}
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
