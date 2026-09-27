package main

// Вызовы функций на заглушке (день 17).
//
// Настоящая модель сама решает, какой инструмент звать и с какими
// аргументами. Заглушке решать нечем, поэтому у неё есть «сценарий хода»:
// правила по порядку — какой инструмент, при каком вопросе, с какими
// аргументами. На каждом круге она берёт первое правило, чей инструмент
// есть в запросе и ещё не вызывался в этом ходе. Правил не осталось —
// отвечает текстом по результатам инструментов.
//
// Этого хватает, чтобы на репетиции прошёл весь путь агента: схема ушла,
// вызов пришёл, MCP-сервер ответил, результат вернулся модели, модель
// ответила. Что заглушка не проверяет — умение модели выбрать инструмент.
// Это видно только на живом API.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type toolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// rule — когда и как звать инструмент.
type rule struct {
	tool string // имя инструмента без префикса сервера
	when func(q string) bool
	args func(q string, results []string) map[string]any
}

var (
	numRe      = regexp.MustCompile(`\d+(?:[.,]\d+)?`)
	currencies = []struct{ word, code string }{
		{"доллар", "USD"}, {"usd", "USD"}, {"$", "USD"},
		{"евро", "EUR"}, {"eur", "EUR"},
		{"лир", "TRY"}, {"try", "TRY"},
		{"юан", "CNY"}, {"cny", "CNY"},
		{"тенге", "KZT"}, {"драм", "AMD"},
	}
)

func currencyOf(q string) string {
	q = strings.ToLower(q)
	for _, c := range currencies {
		if strings.Contains(q, c.word) {
			return c.code
		}
	}
	return ""
}

func firstNumber(q string) (float64, bool) {
	m := numRe.FindString(strings.ReplaceAll(q, " ", ""))
	if m == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(m, ",", "."), 64)
	return v, err == nil
}

var rules = []rule{
	{
		tool: "convert",
		when: func(q string) bool {
			_, n := firstNumber(q)
			return currencyOf(q) != "" && n && strings.Contains(strings.ToLower(q), "сколько")
		},
		args: func(q string, _ []string) map[string]any {
			v, _ := firstNumber(q)
			if l := strings.ToLower(q); strings.Contains(l, "рубл") && !strings.Contains(l, "в рублях") {
				// «30 000 рублей — сколько в лирах?»: из рублей в валюту
				return map[string]any{"amount": v, "from": "RUB", "to": currencyOf(q)}
			}
			return map[string]any{"amount": v, "from": currencyOf(q)}
		},
	},
	{
		tool: "exchange_rate",
		when: func(q string) bool {
			l := strings.ToLower(q)
			return currencyOf(q) != "" && strings.Contains(l, "курс") && !strings.Contains(l, "сколько")
		},
		args: func(q string, _ []string) map[string]any { return map[string]any{"currency": currencyOf(q)} },
	},
}

// mockTools решает, что делать на этом круге: позвать инструмент или
// ответить. ok=false — инструменты тут ни при чём, отвечает обычная заглушка.
func mockTools(req chatReq) (calls []toolCall, text string, ok bool) {
	if len(req.Tools) == 0 {
		return nil, "", false
	}
	u := lastUser(req.Messages)
	if u < 0 {
		return nil, "", false
	}
	q := req.Messages[u].Content
	called := map[string]bool{}
	var results []string
	for _, m := range req.Messages[u+1:] {
		for _, tc := range m.ToolCalls {
			called[tc.Function.Name] = true
		}
		if m.Role == "tool" {
			results = append(results, m.Content)
		}
	}
	for _, r := range rules {
		fn := findTool(req.Tools, r.tool)
		if fn == "" || called[fn] || !r.when(q) {
			continue
		}
		args, _ := json.Marshal(r.args(q, results))
		tc := toolCall{ID: fmt.Sprintf("call_%d", len(called)+1), Type: "function"}
		tc.Function.Name, tc.Function.Arguments = fn, string(args)
		return []toolCall{tc}, "", true
	}
	if len(results) == 0 {
		return nil, "", false
	}
	return nil, "По данным инструментов: " + strings.Join(results, "; "), true
}

// findTool — полное имя функции (с префиксом сервера) по имени инструмента.
func findTool(tools []toolDef, name string) string {
	for _, t := range tools {
		n := t.Function.Name
		if n == name || strings.HasSuffix(n, "__"+name) {
			return n
		}
	}
	return ""
}

// writeToolTurn — ответ круга с инструментами: либо вызовы, либо текст.
// Схема функций считается во вход так же, как у настоящего API: она
// в запросе, значит, оплачивается.
func writeToolTurn(w http.ResponseWriter, req chatReq, calls []toolCall, text string, delay time.Duration) {
	prompt := 0
	for _, m := range req.Messages {
		prompt += len([]rune(m.Content))/2 + 4
		for _, tc := range m.ToolCalls {
			prompt += len(tc.Function.Arguments)/3 + 4
		}
	}
	schema, _ := json.Marshal(req.Tools)
	prompt += len(schema) / 4
	out := len([]rune(text))/2 + 1
	finish := "stop"
	msg := map[string]any{"role": "assistant", "content": text}
	if len(calls) > 0 {
		finish = "tool_calls"
		msg["tool_calls"] = calls
		out = 0
		for _, c := range calls {
			out += len(c.Function.Arguments)/3 + 8
		}
	}
	usage := map[string]any{"prompt_tokens": prompt, "completion_tokens": out, "total_tokens": prompt + out}
	time.Sleep(time.Duration(out) * delay)
	if !req.Stream {
		writeJSON(w, map[string]any{"id": "mock", "model": req.Model,
			"choices": []map[string]any{{"index": 0, "finish_reason": finish, "message": msg}},
			"usage":   usage})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	delta := map[string]any{"content": text}
	if len(calls) > 0 {
		var ds []map[string]any
		for i, c := range calls {
			ds = append(ds, map[string]any{"index": i, "id": c.ID, "type": "function", "function": c.Function})
		}
		delta = map[string]any{"tool_calls": ds}
	}
	for _, v := range []any{
		map[string]any{"model": req.Model, "choices": []map[string]any{{"index": 0, "delta": delta}}},
		map[string]any{"model": req.Model, "choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}, "usage": usage},
	} {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}
