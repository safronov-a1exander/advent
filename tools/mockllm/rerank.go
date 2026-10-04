package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
)

// День 23: реранкер для репетиций. Оценка — доля основ слов вопроса
// (первые пять букв), которые есть во фрагменте. Отвечает вероятностью
// 0…1, как Jina и Cohere, — в config.yaml у заглушки logits не стоит.
func handleRerank(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model     string   `json:"model"`
		Query     string   `json:"query"`
		Documents []string `json:"documents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q := stems(req.Query)
	results := make([]map[string]any, len(req.Documents))
	for i, d := range req.Documents {
		doc := stems(d)
		hit := 0
		for s := range q {
			if doc[s] {
				hit++
			}
		}
		score := 0.0
		if len(q) > 0 {
			score = float64(hit) / float64(len(q))
		}
		results[i] = map[string]any{"index": i, "relevance_score": score}
	}
	writeJSON(w, map[string]any{"model": req.Model, "results": results})
}

func stems(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		rs := []rune(w)
		if len(rs) < 3 {
			continue
		}
		if len(rs) > 5 {
			rs = rs[:5]
		}
		out[string(rs)] = true
	}
	return out
}
