package rag

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Chunk — кусок документа, который попадает в индекс и потом в промпт.
//
// Метаданные — не украшение. По ним ответ ссылается на источник
// («docs/days/day20.md, раздел „Маршрут строит агент“»), по ним видно,
// что поиск достал не тот документ, и по ним же отбирают чанки,
// когда одного смысла мало (день 23).
type Chunk struct {
	// ID — «источник#номер»: стабилен, пока не поменялся сам документ.
	ID     string `json:"id"`
	Source string `json:"source"`
	Title  string `json:"title"`
	// Section — путь заголовков от второго уровня вниз: «Проверено ›
	// Живой API». У кода — объявление: «func plan_goal».
	Section string `json:"section"`
	// Start и End — смещения в байтах внутри документа.
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

// Header — строка контекста, которая уходит в эмбеддинг перед текстом.
// Кусок из середины раздела без неё теряет, о чём он: «шесть вызовов на
// трёх серверах» — чего? С заголовком — «День 20 — оркестрация MCP ›
// Маршрут строит агент».
func (c Chunk) Header() string {
	if c.Section == "" {
		return c.Title
	}
	return c.Title + " › " + c.Section
}

// EmbedText — что именно превращается в вектор.
func (c Chunk) EmbedText() string { return c.Header() + "\n\n" + c.Text }

// Strategy — способ нарезки.
type Strategy string

const (
	// Fixed — окно фиксированной длины с перекрытием. Не знает ничего
	// о тексте: режет посреди предложения, раздела, таблицы.
	Fixed Strategy = "fixed"
	// Structure — по структуре документа: разделы markdown, объявления Go.
	// Большой раздел делится по абзацам, крошечный прилипает к следующему.
	Structure Strategy = "structure"
)

// Strategies — все способы по порядку, для сравнения.
var Strategies = []Strategy{Fixed, Structure}

// ParseStrategy — разбор имени из флага или конфига.
func ParseStrategy(s string) (Strategy, error) {
	for _, st := range Strategies {
		if string(st) == s {
			return st, nil
		}
	}
	return "", fmt.Errorf("стратегия нарезки %q неизвестна (есть: fixed, structure)", s)
}

// Chunking — размеры. Считаются в символах, а не токенах: нарезка не должна
// зависеть от токенизатора модели эмбеддингов, а у bge-m3 на этой базе
// выходит около трёх символов на токен — 1200 символов это примерно 400 токенов.
type Chunking struct {
	Size    int `yaml:"size"`    // fixed: длина окна
	Overlap int `yaml:"overlap"` // fixed: перекрытие соседних окон
	Max     int `yaml:"max"`     // structure: длиннее — делить по абзацам
	Min     int `yaml:"min"`     // structure: короче — склеить со следующим
}

// DefaultChunking — размеры по умолчанию. Окно fixed и потолок structure
// выбраны так, чтобы средний чанк у обеих стратегий был сопоставим:
// сравнивается способ резать, а не длина куска.
var DefaultChunking = Chunking{Size: 1200, Overlap: 200, Max: 1800, Min: 250}

func (c Chunking) withDefaults() Chunking {
	d := DefaultChunking
	if c.Size > 0 {
		d.Size = c.Size
	}
	if c.Overlap > 0 {
		d.Overlap = c.Overlap
	}
	if c.Max > 0 {
		d.Max = c.Max
	}
	if c.Min > 0 {
		d.Min = c.Min
	}
	if d.Overlap >= d.Size {
		d.Overlap = d.Size / 4
	}
	return d
}

// Split режет документ выбранным способом.
func Split(d Document, st Strategy, c Chunking) []Chunk {
	c = c.withDefaults()
	secs := sections(d)
	var (
		spans  [][2]int
		labels []int // structure: по какому месту куска определять раздел
	)
	switch st {
	case Fixed:
		spans = fixedSpans(d.Text, c.Size, c.Overlap)
	default:
		spans, labels = structureSpans(d.Text, secs, c.Max, c.Min)
	}
	chunks := make([]Chunk, 0, len(spans))
	for i, sp := range spans {
		raw := d.Text[sp[0]:sp[1]]
		text := strings.TrimSpace(raw)
		if text == "" {
			continue
		}
		// раздел определяется по первому непустому символу: окно, начатое
		// на пустой строке перед заголовком, относится к этому заголовку
		at := sp[0] + len(raw) - len(strings.TrimLeft(raw, " \t\n"))
		if labels != nil {
			at = labels[i]
		}
		chunks = append(chunks, Chunk{
			ID:      fmt.Sprintf("%s#%d", d.Source, len(chunks)),
			Source:  d.Source,
			Title:   d.Title,
			Section: sectionAt(secs, at),
			Start:   sp[0],
			End:     sp[1],
			Text:    text,
		})
	}
	return chunks
}

// section — раздел документа: где начинается и как называется.
type section struct {
	start int
	path  string
}

// sections — разделы документа по порядку. У markdown это заголовки
// второго уровня и глубже (первый — название документа), у Go —
// объявления верхнего уровня вместе с комментарием над ними.
func sections(d Document) []section {
	switch d.Kind {
	case KindMarkdown:
		return mdSections(d.Text)
	case KindGo:
		return goSections(d.Text)
	}
	return nil
}

func mdSections(text string) []section {
	var (
		out   []section
		stack []string // заголовки уровней 2, 3, …
		fence bool
		off   int
	)
	for _, line := range strings.SplitAfter(text, "\n") {
		start := off
		off += len(line)
		trim := strings.TrimSpace(line)
		// Внутри блока кода «# …» — комментарий shell, а не заголовок.
		if strings.HasPrefix(trim, "```") {
			fence = !fence
			continue
		}
		if fence || !strings.HasPrefix(trim, "#") {
			continue
		}
		level := len(trim) - len(strings.TrimLeft(trim, "#"))
		if level < 2 || level > 6 || len(trim) <= level || trim[level] != ' ' {
			continue
		}
		depth := level - 2
		if depth > len(stack) {
			depth = len(stack)
		}
		stack = append(stack[:depth], strings.TrimSpace(trim[level:]))
		out = append(out, section{start: start, path: strings.Join(stack, " › ")})
	}
	return out
}

func goSections(text string) []section {
	var (
		out     []section
		off     int
		comment = -1 // где начался комментарий над объявлением
	)
	for _, line := range strings.SplitAfter(text, "\n") {
		start := off
		off += len(line)
		switch {
		case strings.HasPrefix(line, "//"):
			if comment < 0 {
				comment = start
			}
			continue
		case strings.HasPrefix(line, "package "):
			at := start
			if comment >= 0 {
				at = comment
			}
			out = append(out, section{start: at, path: "пакет " + strings.TrimSpace(strings.TrimPrefix(line, "package "))})
		default:
			if name := goDecl(line); name != "" {
				at := start
				if comment >= 0 {
					at = comment
				}
				out = append(out, section{start: at, path: name})
			}
		}
		comment = -1
	}
	return out
}

// goDecl — «func plan», «func (Server) handle», «type Goal» или пусто.
func goDecl(line string) string {
	for _, kw := range []string{"func ", "type ", "var ", "const "} {
		rest, ok := strings.CutPrefix(line, kw)
		if !ok {
			continue
		}
		recv := ""
		if kw == "func " && strings.HasPrefix(rest, "(") {
			end := strings.Index(rest, ")")
			if end < 0 {
				return ""
			}
			fields := strings.Fields(rest[1:end])
			if len(fields) > 0 {
				recv = "(" + strings.TrimPrefix(fields[len(fields)-1], "*") + ") "
			}
			rest = strings.TrimSpace(rest[end+1:])
		}
		name := strings.FieldsFunc(rest, func(r rune) bool {
			return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
		})
		if len(name) == 0 {
			// «var (» и «const (» — блок без одного имени
			return strings.TrimSpace(kw)
		}
		return strings.TrimSpace(kw) + " " + recv + name[0]
	}
	return ""
}

// sectionAt — раздел, который действует в позиции at.
func sectionAt(secs []section, at int) string {
	name := ""
	for _, s := range secs {
		if s.start > at {
			break
		}
		name = s.path
	}
	return name
}

// fixedSpans — окна по size символов, соседние перекрываются на overlap.
// Конец окна подтягивается к пробелу, чтобы не рвать слово, — и только:
// предложения, абзацы и разделы эта стратегия рвёт как придётся. Ровно это
// и сравнивается со structure.
func fixedSpans(text string, size, overlap int) [][2]int {
	var spans [][2]int
	n := utf8.RuneCountInString(text)
	if n == 0 {
		return nil
	}
	// смещения рун в байтах, чтобы резать по символам, а не по байтам
	offs := make([]int, 0, n+1)
	for i := range text {
		offs = append(offs, i)
	}
	offs = append(offs, len(text))

	step := size - overlap
	for s := 0; s < n; s += step {
		e := s + size
		if e >= n {
			spans = append(spans, [2]int{offs[s], len(text)})
			break
		}
		// к пробелу назад, но не дальше чем на десятую часть окна
		for back := e; back > e-size/10; back-- {
			if unicode.IsSpace(rune(text[offs[back]])) {
				e = back
				break
			}
		}
		spans = append(spans, [2]int{offs[s], offs[e]})
		// следующее окно начинается с начала слова
		next := e - overlap
		if next <= s {
			next = e
		}
		for next < e && next > s && !unicode.IsSpace(rune(text[offs[next]])) {
			next++
		}
		s = next - step
	}
	return spans
}

// structureSpans — разделы целиком; длинный раздел — по абзацам, пока
// влезает в max; кусок короче min прилипает к следующему, чтобы заголовок
// без текста («## Проверено» перед «### Живой API») не стал отдельным чанком.
func structureSpans(text string, secs []section, max, min int) (spans [][2]int, labels []int) {
	// границы разделов; текст до первого раздела — свой кусок
	bounds := []int{0}
	for _, s := range secs {
		if s.start > bounds[len(bounds)-1] {
			bounds = append(bounds, s.start)
		}
	}
	bounds = append(bounds, len(text))

	var pieces [][2]int
	for i := 0; i+1 < len(bounds); i++ {
		pieces = append(pieces, splitLong(text, bounds[i], bounds[i+1], max)...)
	}

	// склейка коротких с последующим. Потолок для склейки — max+min: заголовок
	// без текста перед длинным разделом должен прилипнуть к нему, даже если
	// вместе чуть длиннее max, — отдельным чанком он бесполезен.
	for i := 0; i < len(pieces); i++ {
		p := pieces[i]
		// раздел склеенного куска — раздел последней части: введение
		// документа или голый заголовок только предваряют её
		label := p[0]
		for runes(text, p) < min && i+1 < len(pieces) && runes(text, [2]int{p[0], pieces[i+1][1]}) <= max+min {
			i++
			p[1] = pieces[i][1]
			label = pieces[i][0]
		}
		spans = append(spans, p)
		labels = append(labels, label)
	}
	return spans, labels
}

// splitLong делит [start, end) по абзацам — пустым строкам вне блоков
// кода — на куски не длиннее max. Абзац длиннее max режется окном.
func splitLong(text string, start, end, max int) [][2]int {
	if runes(text, [2]int{start, end}) <= max {
		return [][2]int{{start, end}}
	}
	// границы абзацев
	var paras []int
	fence := false
	off := start
	for _, line := range strings.SplitAfter(text[start:end], "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
		}
		off += len(line)
		if !fence && strings.TrimSpace(line) == "" && off < end {
			paras = append(paras, off)
		}
	}
	paras = append(paras, end)

	var out [][2]int
	cur := start
	last := start
	for _, p := range paras {
		if runes(text, [2]int{cur, p}) > max && last > cur {
			out = append(out, [2]int{cur, last})
			cur = last
		}
		last = p
	}
	if cur < end {
		out = append(out, [2]int{cur, end})
	}
	// абзац, который сам длиннее max, — окном без перекрытия
	var fin [][2]int
	for _, sp := range out {
		if runes(text, sp) <= max {
			fin = append(fin, sp)
			continue
		}
		for _, w := range fixedSpans(text[sp[0]:sp[1]], max, 0) {
			fin = append(fin, [2]int{sp[0] + w[0], sp[0] + w[1]})
		}
	}
	return fin
}

func runes(text string, sp [2]int) int { return utf8.RuneCountInString(text[sp[0]:sp[1]]) }
