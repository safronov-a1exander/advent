// Package tui — терминальный интерфейс стенда на bubbletea.
//
// День 1: слева поток диалога, справа панель параметров запроса,
// снизу ввод и строка состояния с задержкой, токенами и стоимостью.
// Все параметры правятся не перезапуском с флагами, а прямо на экране.
package tui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/store"
)

// ---- стили ----

var (
	cBorder = lipgloss.AdaptiveColor{Light: "#c8ccd4", Dark: "#3b4048"}
	cAccent = lipgloss.AdaptiveColor{Light: "#0b6bcb", Dark: "#7aa2f7"}
	cUser   = lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#9ece6a"}
	cDim    = lipgloss.AdaptiveColor{Light: "#6b7280", Dark: "#7f8694"}
	cErr    = lipgloss.AdaptiveColor{Light: "#b42318", Dark: "#f7768e"}

	stTitle  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	stUser   = lipgloss.NewStyle().Bold(true).Foreground(cUser)
	stBot    = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	stDim    = lipgloss.NewStyle().Foreground(cDim)
	stErr    = lipgloss.NewStyle().Foreground(cErr)
	stFrame  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cBorder)
	stFocus  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent)
	stStatus = lipgloss.NewStyle().Foreground(cDim).Padding(0, 1)
	stNote   = lipgloss.NewStyle().Foreground(cAccent).Italic(true)
)

// ---- сообщения ----

type chunkMsg llm.Chunk
type doneMsg struct {
	resp *llm.Response
	err  error
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
	Client   *llm.Client
	Provider string
	Settings *Settings
	Store    *store.Writer
	Title    string
}

type totals struct {
	prompt, completion, reasoning int
	cost                          float64
	calls                         int
}

type focusTarget int

const (
	focusInput focusTarget = iota
	focusPanel
	focusNotes
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
	ready     bool
	w, h      int
	focus     focusTarget
	showPanel bool

	// history хранит только реплики user/assistant. Системный промпт
	// подставляется из настроек в момент отправки — поэтому его правка
	// в панели действует сразу, а не после сброса контекста.
	history []llm.Message
	lines   []string
	partial strings.Builder

	busy    atomic.Bool
	stream  chan tea.Msg
	cancel  context.CancelFunc
	lastErr error
	last    *llm.Response
	tot     totals
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

	m := &Model{opts: o, set: o.Settings, ta: ta, sp: sp, showPanel: true}
	m.panel = NewPanel(m.set.Fields(), 34)
	m.notes = NewNotes(60, notesHeight)
	return m
}

func (m *Model) Init() tea.Cmd { return tea.Batch(textarea.Blink, m.sp.Tick) }

// Busy сообщает демо-драйверу, ждать ли ответ.
func (m *Model) Busy() bool { return m.busy.Load() }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
		m.ready = true
		return m, nil

	case tea.KeyMsg:
		return m.onKey(msg)

	case NoteMsg:
		cmd := m.notes.Type(string(msg))
		m.layout()
		return m, cmd

	case noteTickMsg:
		return m, m.notes.Tick()

	case chunkMsg:
		if msg.Content != "" {
			m.partial.WriteString(msg.Content)
			m.refresh()
		}
		return m, m.waitChunk()

	case doneMsg:
		m.busy.Store(false)
		m.finish(msg.resp, msg.err)
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m *Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Ctrl+C работает всегда, даже посреди редактирования поля.
	if msg.String() == "ctrl+c" {
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
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
		return m, m.send(text)
	case "ctrl+j":
		m.ta.InsertString("\n")
		return m, nil
	case "pgup", "pgdown", "shift+up", "shift+down":
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m *Model) resetConversation() {
	m.history = nil
	m.last = nil
	m.pushLine("")
	m.pushLine(stDim.Render("— контекст сброшен (счётчики расхода за сессию не обнуляются) —"))
	m.refresh()
}

// buildRequest собирает запрос из текущих настроек панели.
func (m *Model) buildRequest(userText string) llm.Request {
	msgs := make([]llm.Message, 0, len(m.history)+2)
	if s := strings.TrimSpace(m.set.System); s != "" {
		msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: s})
	}
	msgs = append(msgs, m.history...)
	if userText != "" {
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: userText})
	}
	req := llm.Request{Messages: msgs}
	m.set.Apply(&req)
	return req
}

func (m *Model) send(text string) tea.Cmd {
	req := m.buildRequest(text)
	m.history = append(m.history, llm.Message{Role: llm.RoleUser, Content: text})

	m.pushLine("")
	m.pushLine(stUser.Render("вы:"))
	m.pushLine(text)
	m.pushLine("")
	head := stBot.Render(m.set.Model + ":")
	if sum := m.set.Summary(); sum != "" {
		head += " " + stDim.Render(sum)
	}
	m.pushLine(head)
	m.partial.Reset()
	m.busy.Store(true)
	m.lastErr = nil
	m.panel.Changed = false

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	m.cancel = cancel
	ch := make(chan tea.Msg, 256)
	m.stream = ch
	streaming := m.set.Stream

	go func() {
		defer cancel()
		var (
			resp *llm.Response
			err  error
		)
		if streaming {
			resp, err = m.opts.Client.ChatStream(ctx, req, func(c llm.Chunk) error {
				if !c.Done {
					ch <- chunkMsg(c)
				}
				return nil
			})
		} else {
			resp, err = m.opts.Client.Chat(ctx, req)
			if err == nil {
				ch <- chunkMsg(llm.Chunk{Content: resp.Content})
			}
		}
		ch <- doneMsg{resp: resp, err: err}
	}()

	m.refresh()
	return tea.Batch(m.waitChunk(), m.sp.Tick)
}

func (m *Model) waitChunk() tea.Cmd {
	ch := m.stream
	return func() tea.Msg { return <-ch }
}

func (m *Model) finish(resp *llm.Response, err error) {
	body := m.partial.String()
	m.partial.Reset()

	if err != nil {
		m.lastErr = err
		m.pushLine(stErr.Render("ошибка: " + err.Error()))
		if n := len(m.history); n > 0 {
			m.history = m.history[:n-1] // откатываем неотвеченную реплику
		}
		m.refresh()
		m.log(nil, err)
		return
	}

	m.pushLine(body)
	m.history = append(m.history, llm.Message{Role: llm.RoleAssistant, Content: resp.Content})
	m.last = resp
	m.tot.calls++
	m.tot.prompt += resp.Usage.PromptTokens
	m.tot.completion += resp.Usage.CompletionTokens
	m.tot.reasoning += resp.Usage.ReasoningTokens
	m.tot.cost += resp.CostUSD

	m.pushLine(stDim.Render(fmt.Sprintf(
		"  ↳ finish_reason=%s  latency=%s  prompt=%d  completion=%d  reasoning=%d  $%.6f",
		resp.FinishReason, resp.Latency.Round(time.Millisecond),
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
		resp.Usage.ReasoningTokens, resp.CostUSD)))
	m.refresh()
	m.log(resp, nil)
}

func (m *Model) log(resp *llm.Response, err error) {
	if m.opts.Store == nil {
		return
	}
	req := m.buildRequest("")
	rec := store.Record{
		Provider: m.opts.Provider,
		Model:    req.Model,
		Params:   store.ParamsOf(req),
		Messages: req.Messages,
	}
	if err != nil {
		rec.Error = err.Error()
	}
	if resp != nil {
		rec.Content = resp.Content
		rec.Reasoning = resp.Reasoning
		rec.Finish = resp.FinishReason
		rec.Usage = resp.Usage
		rec.LatencyMS = resp.Latency.Milliseconds()
		rec.CostUSD = resp.CostUSD
	}
	_ = m.opts.Store.Append(rec)
}

// ---- отрисовка ----

func (m *Model) pushLine(s string) { m.lines = append(m.lines, s) }

func (m *Model) panelW() int {
	if !m.showPanel {
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
		m.vp = viewport.New(bodyW, vpH)
	} else {
		m.vp.Width, m.vp.Height = bodyW, vpH
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
	w := m.vp.Width - 2
	if w < 20 {
		w = 20
	}
	m.vp.SetContent(lipgloss.NewStyle().Width(w).Render(body))
	m.vp.GotoBottom()
}

func (m *Model) View() string {
	if !m.ready {
		return "инициализация…"
	}
	title := m.opts.Title
	if title == "" {
		title = defaultTitle
	}
	header := stTitle.Render(title) + "  " +
		stDim.Render(fmt.Sprintf("%s / %s", m.opts.Provider, m.set.Model))

	transcript := m.frame(m.focus == focusInput).Width(m.vp.Width + 2).Render(m.vp.View())
	body := transcript
	if pw := m.panelW(); pw > 0 {
		// Рамка панели по высоте содержимого, а не во весь экран: параметров
		// немного, и высокий пустой прямоугольник справа смотрится как брак.
		panel := m.frame(m.focus == focusPanel).Width(pw).
			Render(m.panel.View(m.focus == focusPanel))
		body = lipgloss.JoinHorizontal(lipgloss.Top, transcript, panel)
	}

	rows := []string{header, "", body}
	if m.notesVisible() {
		rows = append(rows, titledFrame(m.frame(m.focus == focusNotes), m.w-2,
			"блокнот", m.notes.View(m.focus == focusNotes)))
	}
	rows = append(rows,
		m.frame(m.focus == focusInput).Width(m.w-2).Render(m.ta.View()),
		stStatus.Render(m.status()))
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m *Model) frame(focused bool) lipgloss.Style {
	if focused {
		return stFocus
	}
	return stFrame
}

func (m *Model) status() string {
	left := "Enter отправить · Tab параметры и блокнот · Ctrl+R сброс · Ctrl+L очистить · Ctrl+C выход"
	switch {
	case m.busy.Load():
		left = m.sp.View() + " ждём ответ…"
	case m.panel.Editing():
		left = "ввод значения: Enter применить · Esc отмена"
	case m.focus == focusPanel:
		left = "параметры: ↑↓ поле · ←→ значение · Enter ввести · Tab дальше · Esc в диалог"
	case m.focus == focusNotes:
		left = "блокнот: печатай текст · Enter — новая строка · Esc вернуться в диалог"
	}
	right := fmt.Sprintf("вызовов %d · токенов %d↑ %d↓ · $%.6f",
		m.tot.calls, m.tot.prompt, m.tot.completion, m.tot.cost)
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
