package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Field — одна строка панели параметров.
//
// Намеренно структура из замыканий, а не интерфейс: каждый шаг
// добавляет свои параметры в Settings.Fields(), и лишние типы тут только
// мешали бы. Значение живёт в Settings, поле лишь умеет его показать
// и подкрутить.
type Field struct {
	Label string
	Hint  string
	// HintFn — подсказка, зависящая от текущего значения. Если задана,
	// используется вместо Hint.
	HintFn func() string

	// Value — как параметр показывается в панели.
	Value func() string
	// Left/Right — уменьшить/увеличить, предыдущий/следующий вариант.
	Left  func()
	Right func()
	// Text/SetText — необязательный ввод значения с клавиатуры (Enter).
	// Если Text == nil, поле правится только стрелками.
	Text    func() string
	SetText func(string) error
}

func (f Field) hint() string {
	if f.HintFn != nil {
		return f.HintFn()
	}
	return f.Hint
}

// ---- конструкторы под типовые параметры ----

// EnumField — выбор из фиксированного списка. Первый вариант обычно
// «не передавать параметр».
func EnumField(label, hint string, options []string, get func() string, set func(string)) Field {
	idx := func() int {
		cur := get()
		for i, o := range options {
			if o == cur {
				return i
			}
		}
		return 0
	}
	return Field{
		Label: label,
		Hint:  hint,
		Value: func() string { return displayOr(get(), "—") },
		Left: func() {
			i := idx() - 1
			if i < 0 {
				i = len(options) - 1
			}
			set(options[i])
		},
		Right: func() {
			set(options[(idx()+1)%len(options)])
		},
	}
}

// FloatField — вещественный параметр, который можно и не передавать
// (nil = поле не уходит в запрос). Шаг влево из минимума снимает значение.
//
// max специально не является жёстким потолком: иногда нужно
// вылезти за границу диапазона и увидеть 400 от API. Поэтому крутить
// можно до hardMax, а max лишь подсвечивается как «штатный» предел.
func FloatField(label, hint string, get func() *float64, set func(*float64), step, min, hardMax, def float64) Field {
	return Field{
		Label: label,
		Hint:  hint,
		Value: func() string {
			v := get()
			if v == nil {
				return "—"
			}
			return trimZeros(*v)
		},
		Left: func() {
			v := get()
			if v == nil {
				return
			}
			n := round2(*v - step)
			if n < min {
				set(nil)
				return
			}
			set(&n)
		},
		Right: func() {
			v := get()
			if v == nil {
				d := def
				set(&d)
				return
			}
			n := round2(*v + step)
			if n > hardMax {
				n = hardMax
			}
			set(&n)
		},
		Text: func() string {
			if v := get(); v != nil {
				return trimZeros(*v)
			}
			return ""
		},
		SetText: func(s string) error {
			s = strings.TrimSpace(s)
			if s == "" || s == "—" {
				set(nil)
				return nil
			}
			f, err := strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
			if err != nil {
				return fmt.Errorf("нужно число, а не %q", s)
			}
			set(&f)
			return nil
		},
	}
}

// IntField — целочисленный параметр, тоже с состоянием «не задано».
func IntField(label, hint string, get func() *int, set func(*int), step, min, max, def int) Field {
	return Field{
		Label: label,
		Hint:  hint,
		Value: func() string {
			v := get()
			if v == nil {
				return "—"
			}
			return strconv.Itoa(*v)
		},
		Left: func() {
			v := get()
			if v == nil {
				return
			}
			n := *v - step
			if n < min {
				set(nil)
				return
			}
			set(&n)
		},
		Right: func() {
			v := get()
			if v == nil {
				d := def
				set(&d)
				return
			}
			n := *v + step
			if n > max {
				n = max
			}
			set(&n)
		},
		Text: func() string {
			if v := get(); v != nil {
				return strconv.Itoa(*v)
			}
			return ""
		},
		SetText: func(s string) error {
			s = strings.TrimSpace(s)
			if s == "" || s == "—" {
				set(nil)
				return nil
			}
			n, err := strconv.Atoi(s)
			if err != nil {
				return fmt.Errorf("нужно целое число, а не %q", s)
			}
			set(&n)
			return nil
		},
	}
}

// TextField — произвольная строка (системный промпт, стоп-слова).
func TextField(label, hint string, get func() string, set func(string)) Field {
	return Field{
		Label:   label,
		Hint:    hint,
		Value:   func() string { return displayOr(oneLine(get()), "—") },
		Left:    func() {},
		Right:   func() {},
		Text:    get,
		SetText: func(s string) error { set(s); return nil },
	}
}

// BoolField — тумблер.
func BoolField(label, hint string, get func() bool, set func(bool)) Field {
	toggle := func() { set(!get()) }
	return Field{
		Label: label,
		Hint:  hint,
		Value: func() string {
			if get() {
				return "вкл"
			}
			return "выкл"
		},
		Left:  toggle,
		Right: toggle,
	}
}

// ---- панель ----

// Panel — колонка редактируемых параметров.
type Panel struct {
	fields  []Field
	sel     int
	width   int
	editing bool
	input   textinput.Model
	err     string
	// Changed взводится при любом изменении значения — экран может
	// показать, что настройки разошлись с последним прогоном.
	Changed bool
}

func NewPanel(fields []Field, width int) *Panel {
	in := textinput.New()
	in.Prompt = "› "
	in.CharLimit = 4000
	return &Panel{fields: fields, width: width, input: in}
}

func (p *Panel) SetFields(f []Field) { p.fields = f }
func (p *Panel) SetWidth(w int)      { p.width = w; p.input.Width = w - 6 }
func (p *Panel) Editing() bool       { return p.editing }
func (p *Panel) Selected() string {
	if p.sel < len(p.fields) {
		return p.fields[p.sel].Label
	}
	return ""
}

// Update обрабатывает клавиши, когда фокус на панели.
// Возвращает true, если клавиша обработана.
func (p *Panel) Update(msg tea.KeyMsg) (tea.Cmd, bool) {
	if len(p.fields) == 0 {
		return nil, false
	}
	f := &p.fields[p.sel]

	if p.editing {
		switch msg.String() {
		case "esc":
			p.editing = false
			p.err = ""
			return nil, true
		case "enter":
			if f.SetText != nil {
				if err := f.SetText(p.input.Value()); err != nil {
					p.err = err.Error()
					return nil, true
				}
				p.Changed = true
			}
			p.editing = false
			p.err = ""
			return nil, true
		}
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		return cmd, true
	}

	switch msg.String() {
	case "up", "k":
		if p.sel > 0 {
			p.sel--
		}
		return nil, true
	case "down", "j":
		if p.sel < len(p.fields)-1 {
			p.sel++
		}
		return nil, true
	case "left", "h":
		if f.Left != nil {
			f.Left()
			p.Changed = true
		}
		return nil, true
	case "right", "l":
		if f.Right != nil {
			f.Right()
			p.Changed = true
		}
		return nil, true
	case "enter":
		if f.Text != nil {
			p.input.SetValue(f.Text())
			p.input.CursorEnd()
			p.editing = true
			p.err = ""
			return p.input.Focus(), true
		}
		return nil, true
	}
	return nil, false
}

// View рисует панель. focused=false — приглушённая, без курсора.
func (p *Panel) View(focused bool) string {
	var b strings.Builder
	title := "параметры"
	if focused {
		title += "  (↑↓ поле · ←→ значение · Enter ввод · Tab назад)"
	} else {
		title += "  (Tab — редактировать)"
	}
	b.WriteString(stDim.Render(short(title, p.width)) + "\n\n")

	valW := p.width - 20
	if valW < 8 {
		valW = 8
	}
	for i, f := range p.fields {
		cur := i == p.sel && focused
		marker := "  "
		if cur {
			marker = "› "
		}
		label := padRight(short(f.Label, 16), 16)
		val := short(f.Value(), valW)

		line := marker + label + " " + val
		st := lipgloss.NewStyle()
		switch {
		case cur:
			st = st.Bold(true).Foreground(cAccent)
		case !focused:
			st = st.Foreground(cDim)
		}
		b.WriteString(st.Render(line) + "\n")
	}

	if p.sel < len(p.fields) {
		if h := p.fields[p.sel].Hint; h != "" && focused {
			b.WriteString("\n" + stDim.Render(wrap(h, p.width)) + "\n")
		}
	}
	if p.editing {
		b.WriteString("\n" + p.input.View() + "\n")
		b.WriteString(stDim.Render("Enter — применить, Esc — отмена") + "\n")
	}
	if p.err != "" {
		b.WriteString("\n" + stErr.Render(short(p.err, p.width)) + "\n")
	}
	return b.String()
}

// ---- мелочи ----

func displayOr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func trimZeros(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func round2(f float64) float64 {
	return float64(int(f*100+copysign(0.5, f))) / 100
}

func copysign(v, sign float64) float64 {
	if sign < 0 {
		return -v
	}
	return v
}

func padRight(s string, n int) string {
	d := n - len([]rune(s))
	if d <= 0 {
		return s
	}
	return s + strings.Repeat(" ", d)
}

func wrap(s string, w int) string {
	if w < 10 {
		return s
	}
	words := strings.Fields(s)
	var lines []string
	cur := ""
	for _, wd := range words {
		if cur == "" {
			cur = wd
			continue
		}
		if len([]rune(cur))+1+len([]rune(wd)) > w {
			lines = append(lines, cur)
			cur = wd
			continue
		}
		cur += " " + wd
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n")
}
