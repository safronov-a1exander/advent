package tui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
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

// keyMap — как имя клавиши из сценария превращается в сообщение.
//
// В Bubble Tea v2 клавиша описывается не типом, а кодом плюс модификаторами:
// печатный символ приходит в Code вместе с Text, служебные клавиши — только
// кодом. Сообщение, собранное здесь, неотличимо от настоящего нажатия,
// поэтому сценарий идёт ровно тем же путём, что и человек за клавиатурой.
var keyMap = map[string]tea.KeyPressMsg{
	"enter":     {Code: tea.KeyEnter},
	"esc":       {Code: tea.KeyEsc},
	"tab":       {Code: tea.KeyTab},
	"shift+tab": {Code: tea.KeyTab, Mod: tea.ModShift},
	"up":        {Code: tea.KeyUp},
	"down":      {Code: tea.KeyDown},
	"left":      {Code: tea.KeyLeft},
	"right":     {Code: tea.KeyRight},
	"pgup":      {Code: tea.KeyPgUp},
	"pgdown":    {Code: tea.KeyPgDown},
	"backspace": {Code: tea.KeyBackspace},
	"space":     {Code: tea.KeySpace, Text: " "},
	"home":      {Code: tea.KeyHome},
	"end":       {Code: tea.KeyEnd},
	"delete":    {Code: tea.KeyDelete},
	"ctrl+b":    {Code: 'b', Mod: tea.ModCtrl},
	"ctrl+c":    {Code: 'c', Mod: tea.ModCtrl},
	"ctrl+d":    {Code: 'd', Mod: tea.ModCtrl},
	"ctrl+e":    {Code: 'e', Mod: tea.ModCtrl},
	"ctrl+j":    {Code: 'j', Mod: tea.ModCtrl},
	"ctrl+l":    {Code: 'l', Mod: tea.ModCtrl},
	"ctrl+m":    {Code: 'm', Mod: tea.ModCtrl},
	"ctrl+n":    {Code: 'n', Mod: tea.ModCtrl},
	"ctrl+o":    {Code: 'o', Mod: tea.ModCtrl},
	"ctrl+p":    {Code: 'p', Mod: tea.ModCtrl},
	"ctrl+r":    {Code: 'r', Mod: tea.ModCtrl},
	"ctrl+s":    {Code: 's', Mod: tea.ModCtrl},
	"ctrl+t":    {Code: 't', Mod: tea.ModCtrl},
	"ctrl+w":    {Code: 'w', Mod: tea.ModCtrl},
}

// typeKey — сообщение о нажатии печатного символа.
func typeKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
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
				p.Send(typeKey(r))
				time.Sleep(typeDelay)
			}

		case "key":
			k, ok := keyMap[strings.ToLower(a.arg)]
			if !ok {
				// не служебная клавиша — печатаем как текст
				for _, r := range a.arg {
					p.Send(typeKey(r))
				}
				continue
			}
			p.Send(k)

		case "wait":
			waitIdle(idle, a.dur)

		case "quit":
			p.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
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
