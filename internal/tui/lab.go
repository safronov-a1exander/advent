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
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/safronov-a1exander/advent/internal/report"
	"github.com/safronov-a1exander/advent/internal/runner"
	"github.com/safronov-a1exander/advent/internal/scenario"
)

// Lab — экран сравнения вариантов одного сценария.
//
// Слева список вариантов со статусом, справа ответ выбранного варианта
// и его проверки, внизу сводная таблица. Это то, что показывается в видео
// начиная со дня 2.
type Lab struct {
	sc     *scenario.Scenario
	run    *runner.Runner
	report string // куда сохранять отчёт

	set   *Settings
	panel *Panel
	notes *Notes

	vp   viewport.Model
	sp   spinner.Model
	help help.Model

	keys      labKeys
	panelKeys panelKeys
	editKeys  editKeys
	notesKeys notesKeys
	w, h      int

	focus     focusTarget
	showPanel bool
	dirty     bool // параметры правили после последнего прогона

	ready    bool
	busy     atomic.Bool
	sel      int
	attempts []runner.Attempt
	byID     map[string][]int
	cur      string // вариант, который считается прямо сейчас
	res      *runner.Result
	saved    string
	err      error
	events   chan tea.Msg
	showTbl  bool
}

type labEventMsg runner.Event
type labDoneMsg struct {
	res *runner.Result
	err error
}

func NewLab(sc *scenario.Scenario, r *runner.Runner, set *Settings, reportDir string) *Lab {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(cAccent)
	l := &Lab{
		sc: sc, run: r, sp: sp, report: reportDir,
		byID: map[string][]int{}, set: set, showPanel: true,
		help:      newHelp(),
		keys:      newLabKeys(),
		panelKeys: newPanelKeys("к результатам"),
		editKeys:  newEditKeys(),
		notesKeys: newNotesKeys("к результатам"),
	}
	l.panel = NewPanel(set.Fields(), 34)
	l.notes = NewNotes(60, notesHeight)
	return l
}

func (l *Lab) Init() tea.Cmd {
	// см. chat.go: тему терминала запрашиваем сами
	return tea.Batch(tea.RequestBackgroundColor, l.sp.Tick)
}

func (l *Lab) Busy() bool { return l.busy.Load() }

func (l *Lab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if themeFromMsg(msg) {
		l.refresh()
		return l, nil
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		l.w, l.h = msg.Width, msg.Height
		l.resize()
		l.ready = true

	case tea.KeyPressMsg:
		return l.onKey(msg)

	case NoteMsg:
		cmd := l.notes.Type(string(msg))
		l.resize()
		return l, cmd

	case noteTickMsg:
		return l, l.notes.Tick()

	case labEventMsg:
		ev := runner.Event(msg)
		switch ev.Kind {
		case "variant-start":
			l.cur = ev.VariantID
		case "variant-done":
			if ev.Attempt != nil {
				l.attempts = append(l.attempts, *ev.Attempt)
				l.byID[ev.VariantID] = append(l.byID[ev.VariantID], len(l.attempts)-1)
				l.sel = indexOfVariant(l.sc, ev.VariantID)
			}
			l.cur = ""
		}
		l.refresh()
		return l, l.waitEvent()

	case labDoneMsg:
		l.busy.Store(false)
		l.res, l.err = msg.res, msg.err
		l.cur = ""
		l.sel = 0 // после прогона встаём на первый вариант — так демо листает по порядку
		if l.res != nil && l.report != "" {
			if p, err := report.Save(l.report, l.res); err == nil {
				l.saved = p
			}
		}
		l.showTbl = true
		l.refresh()
		return l, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		l.sp, cmd = l.sp.Update(msg)
		l.refresh()
		return l, cmd
	}
	return l, nil
}

func (l *Lab) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return l, tea.Quit
	}

	// Esc из любого места возвращает к результатам.
	if msg.String() == "esc" && !l.panel.Editing() && l.focus != focusInput {
		l.focus = focusInput
		l.resize()
		return l, nil
	}

	if l.focus == focusPanel {
		if cmd, handled := l.panel.Update(msg); handled {
			l.dirty = l.dirty || l.panel.Changed
			l.applyOverrides()
			l.refresh()
			return l, cmd
		}
		if msg.String() == "tab" && !l.panel.Editing() {
			l.focus = focusNotes
			l.resize()
			return l, nil
		}
		// в панели остальные клавиши игнорируем, чтобы «r» не запускало прогон
		// посреди правки значения
		return l, nil
	}

	if l.focus == focusNotes {
		if msg.String() == "tab" {
			l.focus = focusInput
			l.resize()
			return l, nil
		}
		if l.notes.Key(msg) {
			l.resize()
			return l, nil
		}
		return l, nil
	}

	switch msg.String() {
	case "q":
		return l, tea.Quit
	case "tab":
		if l.showPanel {
			l.focus = focusPanel
		} else {
			l.focus = focusNotes
		}
		l.resize()
		return l, nil
	case "ctrl+p":
		l.showPanel = !l.showPanel
		if !l.showPanel {
			l.focus = focusInput
		}
		l.resize()
		return l, nil
	case "r":
		if !l.busy.Load() {
			return l, l.start()
		}
	case "t":
		l.showTbl = !l.showTbl
		l.refresh()
	case "up", "k":
		if l.sel > 0 {
			l.sel--
			l.refresh()
		}
	case "down", "j":
		if l.sel < len(l.sc.Variants)-1 {
			l.sel++
			l.refresh()
		}
	case "pgup", "pgdown":
		var cmd tea.Cmd
		l.vp, cmd = l.vp.Update(msg)
		return l, cmd
	}
	return l, nil
}

// applyOverrides перекладывает панель в runner и сценарий.
func (l *Lab) applyOverrides() {
	l.run.Override = l.set.ScenarioOverride()
	l.run.ModelOverride = strings.TrimSpace(l.set.Model)
	l.run.SystemOverride = strings.TrimSpace(l.set.System)
	if l.set.Repeat > 0 {
		l.sc.Repeat = l.set.Repeat
	}
}

func (l *Lab) start() tea.Cmd {
	l.applyOverrides()
	l.dirty = false
	l.attempts = nil
	l.byID = map[string][]int{}
	l.res, l.err, l.saved = nil, nil, ""
	l.showTbl = false
	l.busy.Store(true)

	ch := make(chan tea.Msg, 64)
	l.events = ch
	go func() {
		res, err := l.run.Run(context.Background(), l.sc, func(ev runner.Event) {
			ch <- labEventMsg(ev)
		})
		ch <- labDoneMsg{res: res, err: err}
	}()
	return tea.Batch(l.waitEvent(), l.sp.Tick)
}

func (l *Lab) waitEvent() tea.Cmd {
	ch := l.events
	return func() tea.Msg { return <-ch }
}

// ---- отрисовка ----

// notesVisible — блокнот показываем, только когда в нём что-то есть
// или в него сейчас пишут.
func (l *Lab) notesVisible() bool {
	return l.focus == focusNotes || !l.notes.Empty()
}

func (l *Lab) resize() {
	vpH := l.h - 6
	if l.notesVisible() {
		vpH -= notesHeight + 2
	}
	if vpH < 4 {
		vpH = 4
	}
	if l.vp.Width() == 0 && l.vp.Height() == 0 {
		l.vp = viewport.New(viewport.WithWidth(l.paneW()), viewport.WithHeight(vpH))
	} else {
		l.vp.SetWidth(l.paneW())
		l.vp.SetHeight(vpH)
	}
	if pw := l.panelW(); pw > 0 {
		l.panel.SetWidth(pw - 2)
	}
	l.notes.SetSize(l.w-4, notesHeight)
	l.refresh()
}

// panelW — ширина колонки параметров; на узком окне панель прячется.
func (l *Lab) panelW() int {
	if !l.showPanel {
		return 0
	}
	w := 34
	if l.w-l.listW()-w < 40 {
		return 0
	}
	return w
}

func (l *Lab) listW() int {
	w := l.w / 4
	if w < 26 {
		w = 26
	}
	if w > 44 {
		w = 44
	}
	return w
}

func (l *Lab) paneW() int {
	w := l.w - l.listW() - l.panelW() - 7
	if w < 30 {
		w = 30
	}
	return w
}

func (l *Lab) refresh() {
	if l.vp.Width() == 0 {
		return
	}
	l.vp.SetContent(l.pane())
}

// borderColor — см. chat.go.
func (l *Lab) borderColor(focused bool) color.Color {
	if focused {
		return cAccent
	}
	return cBorder
}

func (l *Lab) frame(focused bool) lipgloss.Style {
	if focused {
		return stFocus
	}
	return stFrame
}

func (l *Lab) pane() string {
	if l.showTbl && l.res != nil {
		return l.tableView()
	}
	if l.sel >= len(l.sc.Variants) {
		return ""
	}
	v := l.sc.Variants[l.sel]
	idx := l.byID[v.ID]

	var b strings.Builder
	b.WriteString(stTitle.Render(v.Label) + "\n")
	if v.Note != "" {
		b.WriteString(stNote.Render(v.Note) + "\n")
	}
	p := l.sc.EffectiveParams(v, v.Steps[len(v.Steps)-1])
	b.WriteString(stDim.Render("параметры: "+p.Describe()) + "\n\n")

	if len(idx) == 0 {
		b.WriteString(stDim.Render("ещё не запускался — нажми r\n"))
		b.WriteString("\n" + stDim.Render("промпт:") + "\n" + v.Steps[0].User + "\n")
		return b.String()
	}

	for _, i := range idx {
		a := l.attempts[i]
		if l.sc.Repeat > 1 {
			b.WriteString(stDim.Render(fmt.Sprintf("── прогон %d ──", a.N)) + "\n")
		}
		if a.Err != nil {
			b.WriteString(stErr.Render("ошибка: "+a.Err.Error()) + "\n\n")
			continue
		}
		for si, st := range a.Steps {
			if len(a.Steps) > 1 {
				b.WriteString(stBot.Render("▸ "+st.Label) + "\n")
				b.WriteString(stDim.Render("  промпт: "+shorten(st.Prompt, 160)) + "\n")
			}
			if si == len(a.Steps)-1 {
				b.WriteString(st.Content + "\n")
			} else {
				b.WriteString(stDim.Render(shorten(st.Content, 300)) + "\n")
			}
			b.WriteString("\n")
		}
		b.WriteString(stDim.Render(fmt.Sprintf(
			"finish=%s · %s · in %d / out %d / reasoning %d · $%.6f",
			a.Finish, a.Latency.Round(time.Millisecond),
			a.Usage.PromptTokens, a.Usage.CompletionTokens,
			a.Usage.ReasoningTokens, a.CostUSD)) + "\n")

		for _, c := range a.Checks {
			mark, style := "✔", lipgloss.NewStyle().Foreground(cUser)
			if !c.OK {
				mark, style = "✘", lipgloss.NewStyle().Foreground(cErr)
			}
			line := fmt.Sprintf("%s %s", mark, c.Name)
			if c.Detail != "" {
				line += " — " + c.Detail
			}
			b.WriteString(style.Render(line) + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (l *Lab) tableView() string {
	var b strings.Builder
	b.WriteString(stTitle.Render("Сводка") + "\n\n")
	b.WriteString(report.Table(l.res) + "\n")
	var total float64
	for _, a := range l.res.Attempts {
		total += a.CostUSD
	}
	b.WriteString(stDim.Render(fmt.Sprintf("итого за прогон: $%.6f", total)) + "\n")
	if l.saved != "" {
		b.WriteString(stDim.Render("отчёт: "+l.saved) + "\n")
	}
	if l.res.RunID != "" {
		b.WriteString(stDim.Render("журнал: runs/"+l.res.RunID+".jsonl") + "\n")
	}
	b.WriteString("\n" + stDim.Render("t — вернуться к ответам") + "\n")
	return b.String()
}

func (l *Lab) listView() string {
	var b strings.Builder
	for i, v := range l.sc.Variants {
		mark := " "
		style := lipgloss.NewStyle()
		done := l.byID[v.ID]
		switch {
		case l.cur == v.ID:
			mark = l.sp.View()
			style = style.Foreground(cAccent)
		case len(done) > 0:
			ok := true
			for _, di := range done {
				if l.attempts[di].Failed() {
					ok = false
				}
			}
			if ok {
				mark, style = "✔", style.Foreground(cUser)
			} else {
				mark, style = "✘", style.Foreground(cErr)
			}
		case l.busy.Load():
			mark, style = "·", style.Foreground(cDim)
		default:
			mark, style = "○", style.Foreground(cDim)
		}
		line := fmt.Sprintf("%s %s", mark, short(v.Label, l.listW()-4))
		if i == l.sel {
			line = lipgloss.NewStyle().Bold(true).Render("› " + line)
		} else {
			line = "  " + line
		}
		b.WriteString(style.Render(line) + "\n")
	}
	return b.String()
}

func (l *Lab) View() tea.View {
	if !l.ready {
		v := tea.NewView("инициализация…")
		v.AltScreen = true
		return v
	}
	repeat := l.sc.Repeat
	if l.set.Repeat > 0 {
		repeat = l.set.Repeat
	}
	// На экране — продукт и его текущий режим, а не имя yaml-файла:
	// пояснения к заданию живут в блокноте и в docs/days.
	head := stTitle.Render(defaultTitle+" · "+l.sc.DisplayTitle()) + "  " +
		stDim.Render(fmt.Sprintf("%s / %s", l.run.Provider, l.modelLabel()))
	// строку режем по ширине окна: перенос сдвинул бы вниз всю раскладку
	// и обрезал бы нижнюю рамку
	sub := stDim.Render(short(fmt.Sprintf("вариантов %d · повторов %d · %s",
		len(l.sc.Variants), repeat, l.set.OverrideSummary()), l.w-1))

	cols := []string{
		stFrame.Width(l.listW()).Height(l.vp.Height()).Render(l.listView()),
		l.frame(l.focus == focusInput).Width(l.paneW() + 2).Render(l.vp.View()),
	}
	if pw := l.panelW(); pw > 0 {
		cols = append(cols, l.frame(l.focus == focusPanel).
			Width(pw).Height(l.vp.Height()).Render(l.panel.View(l.focus == focusPanel)))
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, cols...)

	rows := []string{head, sub, "", body}
	if l.notesVisible() {
		rows = append(rows, titledFrame(l.frame(l.focus == focusNotes), l.borderColor(l.focus == focusNotes), l.w-2,
			"блокнот", l.notes.View(l.focus == focusNotes)))
	}

	// Подсказка собирается из тех же привязок, по которым работают клавиши.
	var status string
	switch {
	case l.busy.Load():
		status = l.sp.View() + " прогон… " + l.cur
	case l.panel.Editing():
		status = shortHelp(l.help, l.editKeys.Apply, l.editKeys.Cancel)
	case l.focus == focusPanel:
		status = shortHelp(l.help, l.panelKeys.Field, l.panelKeys.Value,
			l.panelKeys.Edit, l.panelKeys.Cycle, l.panelKeys.Back)
	case l.focus == focusNotes:
		status = shortHelp(l.help, l.notesKeys.Line, l.notesKeys.Back)
	case l.dirty:
		status = "параметры изменены — нажми r, чтобы прогнать заново"
	default:
		status = shortHelp(l.help, l.keys.Run, l.keys.Variant, l.keys.Summary,
			l.keys.Cycle, l.keys.Quit)
	}

	rows = append(rows, stStatus.Render(status))
	// В v2 альт-экран — свойство вида, а не опция программы.
	v := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, rows...))
	v.AltScreen = true
	return v
}

// modelLabel — какая модель реально пойдёт в запрос: переопределение из
// панели, модель сценария или «по вариантам», если у каждого своя.
func (l *Lab) modelLabel() string {
	if m := strings.TrimSpace(l.set.Model); m != "" {
		return m
	}
	if l.sc.Model != "" {
		return l.sc.Model
	}
	first := l.sc.Variants[0].Model
	for _, v := range l.sc.Variants {
		if v.Model != first {
			return "по вариантам"
		}
	}
	if first != "" {
		return first
	}
	return l.run.Fallback
}

func indexOfVariant(s *scenario.Scenario, id string) int {
	for i, v := range s.Variants {
		if v.ID == id {
			return i
		}
	}
	return 0
}
