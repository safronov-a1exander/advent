package dialog

import (
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
