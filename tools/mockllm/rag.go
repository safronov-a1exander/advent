package main

import (
	"encoding/json"
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
		q := strings.TrimSpace(text[i+len("Вопрос: "):])
		if aboutChatRe.MatchString(q) {
			return "NONE"
		}
		return q
	}
	return text
}

// День 24: вопрос с фрагментами и требованием ответить JSON с цитатами.
// Заглушка цитирует первое предложение первого фрагмента — дословно,
// чтобы проверка цитат на репетиции проходила, — и на него же ссылается.
var citeFragRe = regexp.MustCompile(`(?s)id: (\S+) — [^\n]*\n(.*?)(?:\n\nid: |\n\n---)`)

func citeRequest(msgs []message) bool {
	i := lastUser(msgs)
	return i >= 0 && strings.Contains(msgs[i].Content, `"quotes"`) && strings.Contains(msgs[i].Content, ragMarker)
}

func mockCite(msgs []message) string {
	m := citeFragRe.FindStringSubmatch(msgs[lastUser(msgs)].Content)
	if m == nil {
		b, _ := json.Marshal(map[string]any{"known": false, "answer": "Не знаю. Уточните вопрос.", "sources": []string{}, "quotes": []any{}})
		return string(b)
	}
	text := strings.TrimSpace(m[2])
	quote := text
	if i := strings.IndexAny(text, ".\n"); i > 20 {
		quote = text[:i]
	}
	b, _ := json.Marshal(map[string]any{
		"known":   true,
		"answer":  "По базе знаний: " + quote,
		"sources": []string{m[1]},
		"quotes":  []map[string]string{{"id": m[1], "text": quote}},
	})
	return string(b)
}

// День 25: вопрос о самом разговоре — искать в базе нечего.
var aboutChatRe = regexp.MustCompile(`(?i)напомни|спасибо|что мы (уже )?(решили|выяснили)|какая у нас цель`)
