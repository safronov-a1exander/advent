package tui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Демо-сценарий — простой построчный формат. Приложение само себя «нажимает»,
// поэтому запись видео воспроизводима и не зависит от эмуляции клавиатуры ОС.
//
//	# комментарий
//	note   <текст>        подпись на экране (видно в видео)
//	type   <текст>        печатает текст в поле ввода посимвольно
//	key    <клавиша>      enter | ctrl+r | ctrl+l | ctrl+c | esc | tab | up | …
//	sleep  <длительность> 800ms, 2s
//	wait   <длительность> ждать, пока модель не закончит отвечать (таймаут)
//	speed  <длительность> задержка между символами при type (по умолчанию 18ms)
//	quit                  завершить программу
type Action struct {
	op  string
	arg string
	dur time.Duration
}

// ParseDemo читает сценарий из файла.
func ParseDemo(path string) ([]Action, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Action
	sc := bufio.NewScanner(f)
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimRight(sc.Text(), " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		op, rest, _ := strings.Cut(trimmed, " ")
		rest = strings.TrimSpace(rest)
		a := Action{op: strings.ToLower(op), arg: rest}
		switch a.op {
		case "sleep", "wait", "speed":
			d, err := time.ParseDuration(rest)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: не разобрал длительность %q: %w", path, ln, rest, err)
			}
			a.dur = d
		case "note", "type", "key", "quit":
			// ок
		default:
			return nil, fmt.Errorf("%s:%d: неизвестная команда %q", path, ln, op)
		}
		out = append(out, a)
	}
	return out, sc.Err()
}

var keyMap = map[string]tea.KeyMsg{
	"enter":     {Type: tea.KeyEnter},
	"esc":       {Type: tea.KeyEsc},
	"tab":       {Type: tea.KeyTab},
	"shift+tab": {Type: tea.KeyShiftTab},
	"up":        {Type: tea.KeyUp},
	"down":      {Type: tea.KeyDown},
	"left":      {Type: tea.KeyLeft},
	"right":     {Type: tea.KeyRight},
	"pgup":      {Type: tea.KeyPgUp},
	"pgdown":    {Type: tea.KeyPgDown},
	"backspace": {Type: tea.KeyBackspace},
	"space":     {Type: tea.KeySpace},
	"ctrl+c":    {Type: tea.KeyCtrlC},
	"ctrl+j":    {Type: tea.KeyCtrlJ},
	"ctrl+l":    {Type: tea.KeyCtrlL},
	"ctrl+r":    {Type: tea.KeyCtrlR},
	"ctrl+e":    {Type: tea.KeyCtrlE},
	"ctrl+n":    {Type: tea.KeyCtrlN},
	"ctrl+p":    {Type: tea.KeyCtrlP},
	"ctrl+t":    {Type: tea.KeyCtrlT},
	"ctrl+o":    {Type: tea.KeyCtrlO},
	"home":      {Type: tea.KeyHome},
	"end":       {Type: tea.KeyEnd},
	"delete":    {Type: tea.KeyDelete},
}

// Idler — то, у чего драйвер спрашивает, занят ли экран.
type Idler interface{ Busy() bool }

// RunDemo проигрывает сценарий в уже запущенную программу.
// Вызывать в отдельной горутине после p.Run() старта.
func RunDemo(p *tea.Program, idle Idler, acts []Action) {
	typeDelay := 18 * time.Millisecond
	time.Sleep(900 * time.Millisecond) // дать интерфейсу отрисоваться

	for _, a := range acts {
		switch a.op {
		case "speed":
			typeDelay = a.dur

		case "sleep":
			time.Sleep(a.dur)

		case "note":
			p.Send(NoteMsg(a.arg))

		case "type":
			for _, r := range a.arg {
				p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				time.Sleep(typeDelay)
			}

		case "key":
			k, ok := keyMap[strings.ToLower(a.arg)]
			if !ok {
				p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(a.arg)})
				continue
			}
			p.Send(k)

		case "wait":
			waitIdle(idle, a.dur)

		case "quit":
			p.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
			return
		}
	}
}

func waitIdle(idle Idler, timeout time.Duration) {
	if idle == nil {
		time.Sleep(timeout)
		return
	}
	// даём запросу успеть стартовать, иначе увидим «не занят» до старта
	time.Sleep(400 * time.Millisecond)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !idle.Busy() {
			return
		}
		time.Sleep(120 * time.Millisecond)
	}
}
