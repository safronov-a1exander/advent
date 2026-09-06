// Package metrics считает, насколько ответы на один и тот же запрос
// похожи друг на друга. Нужен для дня 4: «разнообразие» — это не ощущение,
// а число, которое видно в таблице.
package metrics

import (
	"strings"
	"unicode"
)

// Tokens режет текст на слова в нижнем регистре, выкидывая пунктуацию.
func Tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// TypeTokenRatio — доля уникальных слов в одном ответе (лексическое богатство).
// 1.0 — ни одно слово не повторяется, 0.1 — текст ходит по кругу.
func TypeTokenRatio(s string) float64 {
	t := Tokens(s)
	if len(t) == 0 {
		return 0
	}
	uniq := make(map[string]struct{}, len(t))
	for _, w := range t {
		uniq[w] = struct{}{}
	}
	return float64(len(uniq)) / float64(len(t))
}

// shingles — множество словосочетаний длины n. Биграммы ловят перефразировки
// лучше, чем отдельные слова: совпадение отдельных слов у любых двух текстов
// на одну тему высокое просто из-за общей лексики.
func shingles(s string, n int) map[string]struct{} {
	t := Tokens(s)
	out := make(map[string]struct{})
	if len(t) < n {
		for _, w := range t {
			out[w] = struct{}{}
		}
		return out
	}
	for i := 0; i+n <= len(t); i++ {
		out[strings.Join(t[i:i+n], " ")] = struct{}{}
	}
	return out
}

// Jaccard — сходство двух текстов по биграммам: 1.0 — тексты совпадают,
// 0.0 — не пересекаются вовсе.
func Jaccard(a, b string) float64 {
	sa, sb := shingles(a, 2), shingles(b, 2)
	if len(sa) == 0 && len(sb) == 0 {
		return 1
	}
	inter := 0
	for k := range sa {
		if _, ok := sb[k]; ok {
			inter++
		}
	}
	union := len(sa) + len(sb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// MeanSimilarity — среднее попарное сходство набора ответов на один запрос.
// Высокое значение = модель повторяется, низкое = каждый прогон свой.
// При temperature=0 ожидаемо близко к 1.
func MeanSimilarity(texts []string) float64 {
	if len(texts) < 2 {
		return 1
	}
	var sum float64
	var n int
	for i := 0; i < len(texts); i++ {
		for j := i + 1; j < len(texts); j++ {
			sum += Jaccard(texts[i], texts[j])
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return sum / float64(n)
}

// AllIdentical — точное совпадение всех ответов. Нагляднее любых долей,
// когда показываешь, что temperature=0 у DeepSeek почти детерминирована.
func AllIdentical(texts []string) bool {
	if len(texts) < 2 {
		return true
	}
	first := strings.TrimSpace(texts[0])
	for _, t := range texts[1:] {
		if strings.TrimSpace(t) != first {
			return false
		}
	}
	return true
}
