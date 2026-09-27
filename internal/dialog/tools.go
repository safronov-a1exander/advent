package dialog

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/safronov-a1exander/advent/internal/mcp"
)

// numberRe — число с разделителями разрядов и дробной частью.
var numberRe = regexp.MustCompile(`\d[\d \x{00a0}\x{202f}]*(?:[.,]\d+)?`)

// numbers — числа из текста в одном виде: без пробелов внутри, с точкой,
// не точнее копеек. «51 940,5» и «51940.50» — одно число, а 84.3414
// совпадает с 84.34: модель вправе округлить.
func numbers(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range numberRe.FindAllString(s, -1) {
		n := strings.Map(func(r rune) rune {
			switch r {
			case ' ', ' ', ' ':
				return -1
			}
			return r
		}, strings.TrimSpace(m))
		n = strings.ReplaceAll(n, ",", ".")
		if i := strings.Index(n, "."); i >= 0 {
			if len(n) > i+3 {
				n = n[:i+3]
			}
			n = strings.TrimRight(strings.TrimRight(n, "0"), ".")
		}
		if len(n) >= 2 {
			out[n] = true
		}
	}
	return out
}

// usesResult — есть ли в ответе число из результатов инструментов,
// которого не было в вопросе. Без инструментов проверка не проходит:
// опираться не на что.
func usesResult(question, answer string, tools []mcp.Outcome) bool {
	asked := numbers(question)
	ans := numbers(answer)
	for _, o := range tools {
		if o.IsError {
			continue
		}
		for n := range numbers(o.Text) {
			if !asked[n] && ans[n] {
				return true
			}
		}
	}
	return false
}

// checkToolResult — есть ли want в результате последнего вызова tool.
// Пусто — всё хорошо, иначе — что не так.
func checkToolResult(got []mcp.Outcome, tool, want string) string {
	for i := len(got) - 1; i >= 0; i-- {
		o := got[i]
		if !toolIs(o, tool) {
			continue
		}
		if strings.Contains(strings.ToLower(o.Text), strings.ToLower(want)) {
			return ""
		}
		return fmt.Sprintf("в результате %s нет «%s»", tool, want)
	}
	return "не вызван: " + tool
}

// toolIs — тот ли это вызов: «сервер.инструмент» или просто «инструмент».
func toolIs(o mcp.Outcome, name string) bool {
	if strings.Contains(name, ".") {
		return o.Server+"."+o.Tool == name
	}
	return o.Tool == name
}

// checkPass — дошло ли число из результата p.From до аргументов p.To.
// Числа из вопроса не считаются: их модель могла взять оттуда, не глядя
// на результат. Пусто — стык в порядке.
func checkPass(question string, got []mcp.Outcome, p Pass) string {
	asked := numbers(question)
	var from, to []mcp.Outcome
	for _, o := range got {
		if o.IsError {
			continue
		}
		if toolIs(o, p.From) {
			from = append(from, o)
		}
		if toolIs(o, p.To) {
			to = append(to, o)
		}
	}
	switch {
	case len(from) == 0:
		return "стык " + p.From + " → " + p.To + ": не вызван " + p.From
	case len(to) == 0:
		return "стык " + p.From + " → " + p.To + ": не вызван " + p.To
	}
	for _, f := range from {
		for n := range numbers(f.Text) {
			if asked[n] {
				continue
			}
			for _, t := range to {
				if numbers(t.Args)[n] {
					return ""
				}
			}
		}
	}
	return "стык " + p.From + " → " + p.To + ": в аргументах нет чисел из результата"
}
