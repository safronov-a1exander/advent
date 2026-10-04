package main

import (
	"regexp"
	"strings"
)

// День 22: вопрос пришёл с фрагментами базы знаний. Заглушка не понимает
// смысла, поэтому отвечает началом самого близкого фрагмента со ссылкой
// на него — на репетиции видно, что фрагменты дошли до модели и что
// ответ собран из них.
const ragMarker = "Фрагменты базы знаний"

func ragRequest(msgs []message) bool {
	i := lastUser(msgs)
	return i >= 0 && strings.Contains(msgs[i].Content, ragMarker)
}

var fragRe = regexp.MustCompile(`(?s)\[1\] [^\n]*\n(.*?)(?:\n\n\[2\]|\n\n---)`)

func mockRAG(msgs []message) string {
	m := fragRe.FindStringSubmatch(msgs[lastUser(msgs)].Content)
	if m == nil {
		return "В найденных фрагментах ответа нет."
	}
	words := strings.Fields(m[1])
	if len(words) > 45 {
		words = words[:45]
	}
	return "По фрагменту [1]: " + strings.Join(words, " ") + " …"
}

// День 23: служебный вызов «переписать запрос». Заглушка переписывать
// не умеет и честно возвращает сам вопрос — поиск идёт по нему.
func rewriteRequest(msgs []message) bool {
	return len(msgs) > 0 && msgs[0].Role == "system" && strings.HasPrefix(msgs[0].Content, "Ты переписываешь вопрос")
}

func mockRewrite(msgs []message) string {
	text := msgs[lastUser(msgs)].Content
	if i := strings.LastIndex(text, "Вопрос: "); i >= 0 {
		return strings.TrimSpace(text[i+len("Вопрос: "):])
	}
	return text
}
