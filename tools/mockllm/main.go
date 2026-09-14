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
	"regexp"
	"strings"
	"time"
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Model          string    `json:"model"`
	Messages       []message `json:"messages"`
	Temperature    *float64  `json:"temperature"`
	MaxTokens      *int      `json:"max_tokens"`
	Stop           []string  `json:"stop"`
	Stream         bool      `json:"stream"`
	ResponseFormat *struct {
		Type string `json:"type"`
	} `json:"response_format"`
}

// sampleJSON — ответ в том формате, которого ждут сценарии разбора выписки.
// Заглушка отдаёт его, когда в запросе стоит response_format json_object
// или когда слово JSON встречается в промпте. Операций столько же, сколько
// в реальной выписке: иначе вариант с маленьким max_tokens на репетиции
// не упирался бы в лимит и картина расходилась бы с боевым прогоном.
const sampleJSON = `{"transactions": [` +
	`{"merchant": "Ozon", "amount": 1990, "category": "покупки"}, ` +
	`{"merchant": "Пятёрочка", "amount": 2340, "category": "продукты"}, ` +
	`{"merchant": "Дринкит", "amount": 390, "category": "кафе"}, ` +
	`{"merchant": "Яндекс Такси", "amount": 620, "category": "транспорт"}, ` +
	`{"merchant": "Wildberries", "amount": 4150, "category": "покупки"}, ` +
	`{"merchant": "Возврат Wildberries", "amount": -1800, "category": "покупки"}, ` +
	`{"merchant": "Перевод на копилку", "amount": 10000, "category": "перевод"}, ` +
	`{"merchant": "Самокат", "amount": 1205, "category": "продукты"}, ` +
	`{"merchant": "Яндекс Еда", "amount": 1480, "category": "кафе"}, ` +
	`{"merchant": "Аптека Горздрав", "amount": 870, "category": "здоровье"}, ` +
	`{"merchant": "Спортзал", "amount": 3500, "category": "здоровье"}, ` +
	`{"merchant": "РЖД", "amount": 2960, "category": "транспорт"}, ` +
	`{"merchant": "Возврат Яндекс Еда", "amount": -480, "category": "кафе"}, ` +
	`{"merchant": "Снятие наличных", "amount": 5000, "category": "наличные"}, ` +
	`{"merchant": "Spotify", "amount": 900, "category": "подписки"}` +
	`]}`

var lorem = strings.Fields(`запрос уходит по HTTP на эндпоинт chat completions где токенизатор
режет текст на токены модель считает распределение вероятностей следующего токена
и сэмплирует его с учётом температуры затем шаг повторяется пока не сработает
условие остановки стоп последовательность лимит токенов или собственный токен конца
последовательности после чего сервер закрывает поток и присылает статистику usage`)

func main() {
	addr := flag.String("addr", ":8099", "адрес прослушивания")
	delay := flag.Duration("delay", 25*time.Millisecond, "задержка между токенами в потоке")
	words := flag.Int("words", 45, "сколько слов генерировать")
	window := flag.Int("window", 0, "окно контекста модели в токенах; 0 — без ограничения. Запрос больше окна получает 400, как у настоящего API")
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

		// Если у запроса просят JSON — отдаём JSON. Иначе на заглушке
		// проверки формата всегда красные, и репетиция ничего не проверяет:
		// непонятно, сломан сценарий или просто заглушка отвечает прозой.
		wantJSON := req.ResponseFormat != nil && req.ResponseFormat.Type == "json_object"
		if !wantJSON {
			for _, m := range req.Messages {
				if strings.Contains(strings.ToLower(m.Content), "json") {
					wantJSON = true
					break
				}
			}
		}

		var source []string
		var sep string
		switch {
		case factsRequest(req.Messages):
			source, sep = []string{mockFacts(req.Messages)}, ""
		case wantJSON:
			source, sep = chunkJSON(sampleJSON), ""
		case recallQuestion(req.Messages):
			source, sep = strings.Fields(recall(req.Messages)), " "
		default:
			source, sep = lorem, " "
		}

		// сколько кусочков просили
		n := len(source)
		if !wantJSON && *words < n {
			n = *words
		}
		if req.MaxTokens != nil && *req.MaxTokens < n {
			n = *req.MaxTokens
		}
		out := make([]string, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, source[i%len(source)])
		}
		finish := "stop"
		if req.MaxTokens != nil && len(source) > *req.MaxTokens {
			finish = "length"
		}
		// Токены запроса — примерно как у настоящего токенизатора на смешанном
		// русском тексте: символ-другой на токен плюс шаблон сообщения. Раньше
		// считались слова, и запрос на заглушке выходил втрое легче, чем на API.
		promptTokens := 0
		for _, m := range req.Messages {
			promptTokens += len([]rune(m.Content))/2 + 4
		}

		// Окно контекста: запрос вместе с потолком ответа не должен его
		// превышать. Отказ — тот же 400 с context_length_exceeded, что отдают
		// OpenAI-совместимые API, поэтому клиент проходит ровно тот же путь.
		if *window > 0 {
			need := promptTokens
			if req.MaxTokens != nil {
				need += *req.MaxTokens
			}
			if need > *window {
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]any{"error": map[string]any{
					"message": fmt.Sprintf("This model's maximum context length is %d tokens. However, you requested %d tokens (%d in the messages, %d in the completion). Please reduce the length of the messages or completion.",
						*window, need, promptTokens, need-promptTokens),
					"type": "invalid_request_error",
					"code": "context_length_exceeded",
				}})
				return
			}
		}

		if !req.Stream {
			// Настоящая модель тратит время на каждый токен и без стриминга —
			// просто отдаёт всё разом в конце. Без этой паузы параллельные
			// запросы на заглушке выглядели бы мгновенными и ничего не показывали.
			time.Sleep(time.Duration(len(out)) * *delay)
			writeJSON(w, map[string]any{
				"id": "mock", "model": req.Model,
				"choices": []map[string]any{{
					"index": 0, "finish_reason": finish,
					"message": map[string]string{"role": "assistant", "content": strings.Join(out, sep)},
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
				piece = sep + word
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

// Заглушка, как и настоящая модель, не помнит прошлых запросов: она видит
// только сообщения текущего. Поэтому на «как меня зовут» она отвечает
// по истории, пришедшей в запросе, — и если агент историю не передал,
// ответ честно будет «не знаю». Так на репетиции проверяется ровно то,
// что проверяется на живом API: память живёт у агента, а не у модели.
var nameRe = regexp.MustCompile(`(?i)меня зовут\s+([\p{L}-]+)`)

// factsRequest — служебный запрос sticky facts от агента (день 10).
func factsRequest(msgs []message) bool {
	return len(msgs) >= 2 && strings.HasPrefix(msgs[0].Content, "Ты ведёшь блок ключевых фактов")
}

// mockFacts — обновление фактов без модели: прежние факты остаются,
// имя из «меня зовут» становится фактом, а сама реплика — фактом
// «реплика-N». Этого хватает, чтобы на репетиции прошёл весь путь:
// служебный вызов, JSON, блок фактов в следующем запросе.
func mockFacts(msgs []message) string {
	body := msgs[1].Content
	facts := map[string]string{}
	if _, cur, ok := strings.Cut(body, "Текущие факты:\n"); ok {
		cur, _, _ = strings.Cut(cur, "\n\n")
		_ = json.Unmarshal([]byte(cur), &facts)
	}
	_, msg, _ := strings.Cut(body, "Новое сообщение пользователя:\n")
	if m := nameRe.FindStringSubmatch(msg); m != nil {
		facts["имя"] = m[1]
	} else if s := strings.Join(strings.Fields(msg), " "); s != "" {
		if r := []rune(s); len(r) > 60 {
			s = string(r[:60]) + "…"
		}
		facts[fmt.Sprintf("реплика-%d", len(facts)+1)] = s
	}
	b, _ := json.Marshal(map[string]any{"facts": facts})
	return string(b)
}

func recallQuestion(msgs []message) bool {
	last := lastUser(msgs)
	return last >= 0 && strings.Contains(strings.ToLower(msgs[last].Content), "как меня зовут")
}

func recall(msgs []message) string {
	last := lastUser(msgs)
	for i := last - 1; i >= 0; i-- {
		// Прошлые вопросы «как меня зовут и …» — не представление:
		// иначе регулярка вытащит из них «и» вместо имени.
		if msgs[i].Role != "user" || recallQuestion(msgs[:i+1]) {
			continue
		}
		if m := nameRe.FindStringSubmatch(msgs[i].Content); m != nil {
			return "Тебя зовут " + m[1] + " — ты сам сказал это раньше в нашем разговоре."
		}
	}
	return "Не знаю: в этом разговоре ты не представлялся."
}

func lastUser(msgs []message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return i
		}
	}
	return -1
}

// chunkJSON режет строку на кусочки по нескольку символов.
//
// Заглушка считает «токенами» то, что сама же и нарезала. Если резать JSON
// по пробелам, кусочков выходит втрое меньше, чем даёт настоящий токенизатор
// на кириллице и пунктуации, — и вариант с маленьким max_tokens на репетиции
// не упирается в лимит, хотя на живом API упирается.
func chunkJSON(s string) []string {
	const size = 3
	r := []rune(s)
	out := make([]string, 0, len(r)/size+1)
	for i := 0; i < len(r); i += size {
		end := i + size
		if end > len(r) {
			end = len(r)
		}
		out = append(out, string(r[i:end]))
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
