package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// upstream — llama-server в миниатюре: /tokenize считает слова,
// /v1/chat/completions отвечает после паузы и запоминает тело.
type upstream struct {
	mu    sync.Mutex
	delay time.Duration
	last  map[string]any
	gate  chan struct{} // если задан — ответ ждёт, пока его не откроют
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/tokenize":
		var in struct{ Content string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		toks := make([]int, len(strings.Fields(in.Content)))
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": toks})
	case "/v1/chat/completions":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		u.mu.Lock()
		u.last = body
		gate := u.gate
		u.mu.Unlock()
		if gate != nil {
			<-gate
		}
		time.Sleep(u.delay)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	case "/health":
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

func post(t *testing.T, h http.Handler, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const hello = `{"messages":[{"role":"user","content":"привет"}]}`

func TestAuthAndModel(t *testing.T) {
	up := &upstream{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	g := New(Config{Upstream: srv.URL + "/v1", Keys: map[string]string{"k1": "аня"}, Model: "qwen3-4b"})

	if rec := post(t, g, "", hello); rec.Code != http.StatusUnauthorized {
		t.Fatalf("без ключа: %d", rec.Code)
	}
	if rec := post(t, g, "чужой", hello); rec.Code != http.StatusUnauthorized {
		t.Fatalf("чужой ключ: %d", rec.Code)
	}
	if rec := post(t, g, "k1", `{"model":"qwen3-4b-q8","max_tokens":99999,"messages":[{"role":"user","content":"привет"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("свой ключ: %d %s", rec.Code, rec.Body)
	}
	// Модель и потолок ответа решает шлюз, а не клиент.
	if up.last["model"] != "qwen3-4b" || up.last["max_tokens"] != float64(1024) {
		t.Fatalf("в модель ушло: %v", up.last)
	}
	// Страница и здоровье — без ключа.
	for _, path := range []string{"/", "/health"} {
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
}

func TestRateLimit(t *testing.T) {
	srv := httptest.NewServer(&upstream{})
	defer srv.Close()
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	g := New(Config{Upstream: srv.URL, Keys: map[string]string{"k1": "аня", "k2": "боря"},
		RatePerMin: 6, Burst: 2, Now: func() time.Time { return now }})

	for i := 0; i < 2; i++ {
		if rec := post(t, g, "k1", hello); rec.Code != http.StatusOK {
			t.Fatalf("запрос %d в пределах burst: %d", i+1, rec.Code)
		}
	}
	rec := post(t, g, "k1", hello)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "10" {
		t.Fatalf("сверх лимита: %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Лимит — на ключ: соседа чужой всплеск не задевает.
	if rec := post(t, g, "k2", hello); rec.Code != http.StatusOK {
		t.Fatalf("другой ключ: %d", rec.Code)
	}
	// 6 в минуту — один запрос в 10 секунд.
	now = now.Add(10 * time.Second)
	if rec := post(t, g, "k1", hello); rec.Code != http.StatusOK {
		t.Fatalf("через 10 с: %d", rec.Code)
	}
}

func TestContextLimit(t *testing.T) {
	srv := httptest.NewServer(&upstream{})
	defer srv.Close()
	g := New(Config{Upstream: srv.URL, Keys: map[string]string{"k1": "аня"}, MaxContext: 100, MaxTokens: 50})

	long := strings.Repeat("слово ", 60)
	rec := post(t, g, "k1", `{"messages":[{"role":"user","content":"`+long+`"}]}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("длинный промпт: %d", rec.Code)
	}
	// Тот же промпт с коротким ответом помещается: 60 + 4 + 30 ≤ 100.
	rec = post(t, g, "k1", `{"max_tokens":30,"messages":[{"role":"user","content":"`+long+`"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("с коротким ответом: %d %s", rec.Code, rec.Body)
	}
	// Схемы инструментов — тоже промпт: с ними тот же запрос уже не влезает.
	tools := `,"tools":[{"type":"function","function":{"name":"search","description":"` + strings.Repeat("поиск ", 10) + `"}}]`
	rec = post(t, g, "k1", `{"max_tokens":30,"messages":[{"role":"user","content":"`+long+`"}]`+tools+`}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("с инструментами: %d", rec.Code)
	}
}

func TestQueue(t *testing.T) {
	up := &upstream{gate: make(chan struct{})}
	srv := httptest.NewServer(up)
	defer srv.Close()
	g := New(Config{Upstream: srv.URL, Keys: map[string]string{"k1": "аня"},
		RatePerMin: 1000, Burst: 100, Parallel: 1, Queue: 1})

	// Первый занимает модель, второй ждёт в очереди, третьему места нет.
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() { codes <- post(t, g, "k1", hello).Code }()
		time.Sleep(100 * time.Millisecond)
	}
	if rec := post(t, g, "k1", hello); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("очередь полна: %d", rec.Code)
	}
	close(up.gate)
	for i := 0; i < 2; i++ {
		if c := <-codes; c != http.StatusOK {
			t.Fatalf("из очереди: %d", c)
		}
	}
	st := g.Stats()["responses"].(map[string]int)
	if st["200"] != 2 || st["503"] != 1 {
		t.Fatalf("статистика: %v", st)
	}
}
