package tui

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
)

// Раскладка клавиш описана данными, а не строками в status().
//
// Раньше подсказка собиралась вручную в каждом экране и в каждом состоянии
// фокуса — шесть отдельных строк, которые разъезжались с реальным поведением
// при каждой правке. Теперь источник один: key.Binding знает и клавишу,
// и её описание, а строку подсказки печатает help.Model.

type chatKeys struct {
	Send     key.Binding
	Newline  key.Binding
	Scroll   key.Binding
	Cycle    key.Binding
	Back     key.Binding
	Panel    key.Binding
	Spawn    key.Binding
	Switch   key.Binding
	Branches key.Binding
	Debug    key.Binding
	Tokens   key.Binding
	Bench    key.Binding
	Reset    key.Binding
	Clear    key.Binding
	Quit     key.Binding
}

type panelKeys struct {
	Field key.Binding
	Value key.Binding
	Edit  key.Binding
	Cycle key.Binding
	Back  key.Binding
}

type editKeys struct {
	Apply  key.Binding
	Cancel key.Binding
}

type notesKeys struct {
	Line key.Binding
	Back key.Binding
}

type agentKeys struct {
	Move  key.Binding
	Open  key.Binding
	New   key.Binding
	Close key.Binding
	Back  key.Binding
}

type branchKeys struct {
	Move       key.Binding
	Open       key.Binding
	Checkpoint key.Binding
	Back       key.Binding
}

func newBranchKeys() branchKeys {
	return branchKeys{
		Move:       key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "выбор")),
		Open:       key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "в ветку / ветка от чекпойнта")),
		Checkpoint: key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("Ctrl+S", "чекпойнт здесь")),
		Back:       key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", "в диалог")),
	}
}

type labKeys struct {
	Run     key.Binding
	Scroll  key.Binding
	Variant key.Binding
	Summary key.Binding
	Cycle   key.Binding
	Panel   key.Binding
	Quit    key.Binding
}

func newChatKeys(withBench bool) chatKeys {
	k := chatKeys{
		Send:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "отправить")),
		Newline: key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("Ctrl+J", "перенос")),
		Scroll: key.NewBinding(key.WithKeys("pgup", "pgdown", "shift+up", "shift+down"),
			key.WithHelp("PgUp/PgDn", "прокрутка")),
		Cycle:    key.NewBinding(key.WithKeys("tab"), key.WithHelp("Tab", "параметры и блокнот")),
		Back:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", "в диалог")),
		Panel:    key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("Ctrl+P", "панель")),
		Spawn:    key.NewBinding(key.WithKeys("ctrl+n"), key.WithHelp("Ctrl+N", "новый агент")),
		Switch:   key.NewBinding(key.WithKeys("ctrl+o"), key.WithHelp("Ctrl+O", "агенты")),
		Branches: key.NewBinding(key.WithKeys("ctrl+b"), key.WithHelp("Ctrl+B", "ветки")),
		Debug:    key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("Ctrl+D", "внутри агента")),
		Tokens:   key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("Ctrl+T", "токены")),
		Bench:    key.NewBinding(key.WithKeys("ctrl+e"), key.WithHelp("Ctrl+E", "все модели")),
		Reset:    key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("Ctrl+R", "сброс")),
		Clear:    key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("Ctrl+L", "очистить")),
		Quit:     key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("Ctrl+C", "выход")),
	}
	// Сравнение моделей появляется только там, где оно реализовано:
	// подсказка не должна обещать клавишу, которой нет.
	k.Bench.SetEnabled(withBench)
	return k
}

func newPanelKeys(backHelp string) panelKeys {
	return panelKeys{
		Field: key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "поле")),
		Value: key.NewBinding(key.WithKeys("left", "right"), key.WithHelp("←→", "значение")),
		Edit:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "ввести")),
		Cycle: key.NewBinding(key.WithKeys("tab"), key.WithHelp("Tab", "дальше")),
		Back:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", backHelp)),
	}
}

func newEditKeys() editKeys {
	return editKeys{
		Apply:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "применить")),
		Cancel: key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", "отмена")),
	}
}

func newNotesKeys(backHelp string) notesKeys {
	return notesKeys{
		Line: key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "новая строка")),
		Back: key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", backHelp)),
	}
}

func newAgentKeys() agentKeys {
	return agentKeys{
		Move:  key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "выбор")),
		Open:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "открыть")),
		New:   key.NewBinding(key.WithKeys("ctrl+n"), key.WithHelp("Ctrl+N", "новый")),
		Close: key.NewBinding(key.WithKeys("ctrl+w", "delete"), key.WithHelp("Ctrl+W", "закрыть")),
		Back:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", "в диалог")),
	}
}

func newLabKeys() labKeys {
	return labKeys{
		Run: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "прогнать")),
		Scroll: key.NewBinding(key.WithKeys("pgup", "pgdown"),
			key.WithHelp("PgUp/PgDn", "прокрутка")),
		Variant: key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "вариант")),
		Summary: key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "сводка")),
		Cycle:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("Tab", "параметры и блокнот")),
		Panel:   key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("Ctrl+P", "панель")),
		Quit:    key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "выход")),
	}
}

// newHelp — модель подсказки в один ряд.
func newHelp() help.Model {
	h := help.New()
	h.ShowAll = false
	return h
}

// shortHelp печатает подсказку по списку привязок, пропуская выключенные.
//
// Стили берутся при каждой отрисовке: тема терминала приходит сообщением
// уже после создания модели, и зафиксированные при конструировании цвета
// остались бы от значений по умолчанию.
func shortHelp(h help.Model, bindings ...key.Binding) string {
	h.Styles.ShortKey = stDim
	h.Styles.ShortDesc = stDim
	h.Styles.ShortSeparator = stDim
	// многоточие, когда подсказка не влезла; стиль по умолчанию на тёмном
	// фоне почти не виден, и обрыв выглядит как висящий разделитель
	h.Styles.Ellipsis = stDim

	live := make([]key.Binding, 0, len(bindings))
	for _, b := range bindings {
		if b.Enabled() {
			live = append(live, b)
		}
	}
	return h.ShortHelpView(live)
}
