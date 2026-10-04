package main

import (
	"encoding/json"
	"hash/fnv"
	"math"
	"net/http"
	"strings"
	"unicode"
)

// День 21: эмбеддинги для репетиций. Смысла заглушка не понимает —
// вектор собирается из хэшей основ слов (первые пять букв), так что
// тексты с общими словами оказываются рядом. На живой модели «сколько
// стоит» и «во что обходится» близки; здесь — нет. Для репетиции
// записи и проверки, что конвейер не сломан, этого хватает.
const embedDim = 256

type embedReq struct {
	Model string          `json:"model"`
	Input json.RawMessage `json:"input"`
}

func handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	var req embedReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// input бывает строкой или массивом строк — как у OpenAI
	var inputs []string
	if err := json.Unmarshal(req.Input, &inputs); err != nil {
		var one string
		if err := json.Unmarshal(req.Input, &one); err != nil {
			http.Error(w, "input: строка или массив строк", http.StatusBadRequest)
			return
		}
		inputs = []string{one}
	}
	data := make([]map[string]any, len(inputs))
	tokens := 0
	for i, s := range inputs {
		data[i] = map[string]any{"object": "embedding", "index": i, "embedding": mockVector(s)}
		tokens += len([]rune(s))/3 + 1
	}
	writeJSON(w, map[string]any{
		"object": "list", "model": req.Model, "data": data,
		"usage": map[string]any{"prompt_tokens": tokens, "total_tokens": tokens},
	})
}

func mockVector(s string) []float32 {
	v := make([]float64, embedDim)
	for _, word := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		rs := []rune(word)
		if len(rs) < 3 {
			continue
		}
		if len(rs) > 5 {
			rs = rs[:5]
		}
		h := fnv.New32a()
		h.Write([]byte(string(rs)))
		x := h.Sum32()
		sign := 1.0
		if x&1 == 1 {
			sign = -1
		}
		v[(x>>1)%embedDim] += sign
	}
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	out := make([]float32, embedDim)
	if sum == 0 {
		out[0] = 1
		return out
	}
	n := math.Sqrt(sum)
	for i, x := range v {
		out[i] = float32(x / n)
	}
	return out
}
