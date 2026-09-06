// Команда mockllm — крошечный OpenAI-совместимый сервер-заглушка.
//
// Нужен, чтобы прогонять демо-сценарии и репетировать запись видео,
// не тратя токены. Отдаёт осмысленный текст, честный usage и держит
// stream (SSE) — интерфейс ведёт себя так же, как с настоящим API.
//
//	go run ./tools/mockllm -addr :8099
//	advent -provider local doctor      # base_url http://127.0.0.1:8099/v1
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	Temperature *float64  `json:"temperature"`
	MaxTokens   *int      `json:"max_tokens"`
	Stop        []string  `json:"stop"`
	Stream      bool      `json:"stream"`
}

var lorem = strings.Fields(`запрос уходит по HTTP на эндпоинт chat completions где токенизатор
режет текст на токены модель считает распределение вероятностей следующего токена
и сэмплирует его с учётом температуры затем шаг повторяется пока не сработает
условие остановки стоп последовательность лимит токенов или собственный токен конца
последовательности после чего сервер закрывает поток и присылает статистику usage`)

func main() {
	addr := flag.String("addr", ":8099", "адрес прослушивания")
	delay := flag.Duration("delay", 25*time.Millisecond, "задержка между токенами в потоке")
	words := flag.Int("words", 45, "сколько слов генерировать")
	flag.Parse()

	mux := http.NewServeMux()

	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"object": "list", "data": []map[string]string{
			{"id": "mock-weak"}, {"id": "mock-medium"}, {"id": "mock-strong"},
		}})
	})

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req chatReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// повторяем поведение DeepSeek: temperature вне [0,2] — это 400
		if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 2) {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": map[string]any{
				"message": "Invalid temperature value, the valid range of temperature is [0, 2]",
				"type":    "invalid_request_error",
			}})
			return
		}

		n := *words
		if req.MaxTokens != nil && *req.MaxTokens < n {
			n = *req.MaxTokens
		}
		out := make([]string, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, lorem[i%len(lorem)])
		}
		finish := "stop"
		if req.MaxTokens != nil && *words > *req.MaxTokens {
			finish = "length"
		}
		promptTokens := 0
		for _, m := range req.Messages {
			promptTokens += len(strings.Fields(m.Content)) + 4
		}

		if !req.Stream {
			writeJSON(w, map[string]any{
				"id": "mock", "model": req.Model,
				"choices": []map[string]any{{
					"index": 0, "finish_reason": finish,
					"message": map[string]string{"role": "assistant", "content": strings.Join(out, " ")},
				}},
				"usage": map[string]any{
					"prompt_tokens": promptTokens, "completion_tokens": len(out),
					"total_tokens": promptTokens + len(out),
				},
			})
			return
		}

		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		send := func(v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
		for i, word := range out {
			piece := word
			if i > 0 {
				piece = " " + word
			}
			send(map[string]any{"model": req.Model, "choices": []map[string]any{
				{"index": 0, "delta": map[string]string{"content": piece}},
			}})
			time.Sleep(*delay)
		}
		send(map[string]any{"model": req.Model, "choices": []map[string]any{
			{"index": 0, "delta": map[string]string{}, "finish_reason": finish},
		}, "usage": map[string]any{
			"prompt_tokens": promptTokens, "completion_tokens": len(out),
			"total_tokens": promptTokens + len(out),
		}})
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	})

	log.Printf("mockllm слушает %s (base_url http://127.0.0.1%s/v1)", *addr, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
