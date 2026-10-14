// Package gateway — приватный сервис поверх локальной модели (день 30).
//
// llama-server слушает только 127.0.0.1 и ничего не знает о том, кто
// к нему пришёл. Шлюз встаёт перед ним в сети и добавляет то, без чего
// модель нельзя открывать даже домашним устройствам:
//
//   - ключи доступа: без Bearer-ключа — 401, по ключу видно, чьё это;
//   - лимит запросов на ключ (ведро токенов): сверх — 429 и Retry-After;
//   - очередь: модель на одной видеокарте считает ограниченное число
//     запросов сразу, остальные ждут; переполненная очередь — 503;
//   - предел контекста: промпт считается токенизатором самой модели,
//     и если вместе с max_tokens он не влезает в окно — 413 сразу,
//     а не обрезанный ответ через минуту; max_tokens сверх потолка
//     урезается.
//
// Наружу — тот же OpenAI-совместимый /v1/chat/completions (и поток SSE),
// поэтому клиент стенда подключается к сервису сменой base_url, а на «/»
// лежит страница чата для браузера и телефона.
package gateway

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// chatPage — страница чата на «/»: открывается с телефона или ноутбука
// в той же сети, ключ вводится на странице.
//
//go:embed chat.html
var chatPage string

// Config — ограничения сервиса. Нули означают «по умолчанию» (см. New).
type Config struct {
	// Upstream — адрес llama-server без /v1: http://127.0.0.1:8080.
	Upstream string
	// Model — имя модели, которое шлюз подставляет в каждый запрос.
	// Клиент модель не выбирает: что крутится на видеокарте, решает
	// владелец сервиса.
	Model string
	// Keys — ключ → имя владельца. Пусто — сервис закрыт для всех.
	Keys map[string]string
	// RatePerMin — запросов в минуту на ключ; Burst — сколько можно
	// сразу подряд, пока ведро полное.
	RatePerMin int
	Burst      int
	// Parallel — сколько запросов одновременно уходят в модель;
	// Queue — сколько ещё могут ждать своей очереди.
	Parallel int
	Queue    int
	// MaxContext — окно в токенах: промпт + max_tokens. MaxTokens —
	// потолок ответа: больше не дадим, даже если попросили.
	MaxContext int
	MaxTokens  int
	// Log — куда писать строку на запрос; nil — никуда.
	Log io.Writer
	// Now — часы для тестов.
	Now func() time.Time
}

type Gateway struct {
	cfg   Config
	http  *http.Client
	slots chan struct{}

	waiting atomic.Int64

	mu      sync.Mutex
	buckets map[string]*bucket
	stats   map[int]int
}

type bucket struct {
	tokens float64
	last   time.Time
}

func New(cfg Config) *Gateway {
	if cfg.RatePerMin <= 0 {
		cfg.RatePerMin = 30
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 5
	}
	if cfg.Parallel <= 0 {
		cfg.Parallel = 1
	}
	if cfg.Queue < 0 {
		cfg.Queue = 0
	}
	if cfg.MaxContext <= 0 {
		cfg.MaxContext = 8192
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 1024
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Upstream = strings.TrimRight(strings.TrimSuffix(strings.TrimRight(cfg.Upstream, "/"), "/v1"), "/")
	return &Gateway{
		cfg:     cfg,
		http:    &http.Client{Timeout: 10 * time.Minute},
		slots:   make(chan struct{}, cfg.Parallel),
		buckets: map[string]*bucket{},
		stats:   map[int]int{},
	}
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := g.cfg.Now()
	rec := &recorder{ResponseWriter: w, status: http.StatusOK}
	who, note := g.route(rec, r)
	g.mu.Lock()
	g.stats[rec.status]++
	g.mu.Unlock()
	if g.cfg.Log != nil && r.URL.Path != "/health" {
		fmt.Fprintf(g.cfg.Log, "%s %-8s %s %s %d %s %s\n", start.Format("15:04:05"), who, r.Method, r.URL.Path,
			rec.status, g.cfg.Now().Sub(start).Round(time.Millisecond), note)
	}
}

func (g *Gateway) route(w http.ResponseWriter, r *http.Request) (who, note string) {
	switch {
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, chatPage)
		return "-", ""
	case r.URL.Path == "/health":
		g.health(w, r)
		return "-", ""
	}

	who, ok := g.auth(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "auth", "нужен ключ: Authorization: Bearer <ключ>")
		return "?", "без ключа"
	}
	switch {
	case r.URL.Path == "/v1/models" && r.Method == http.MethodGet:
		g.proxy(w, r.Context(), http.MethodGet, "/v1/models", nil)
		return who, ""
	case r.URL.Path == "/v1/stats" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, g.Stats())
		return who, ""
	case r.URL.Path == "/v1/chat/completions" && r.Method == http.MethodPost:
		return who, g.chat(w, r, who)
	}
	apiError(w, http.StatusNotFound, "not_found", "нет такого пути: "+r.URL.Path)
	return who, ""
}

func (g *Gateway) auth(r *http.Request) (string, bool) {
	key := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if key == "" {
		return "", false
	}
	who, ok := g.cfg.Keys[key]
	return who, ok
}

// chat — главный путь: лимит, контекст, очередь, модель.
func (g *Gateway) chat(w http.ResponseWriter, r *http.Request, who string) string {
	if wait, ok := g.allow(who); !ok {
		secs := int(math.Ceil(wait.Seconds()))
		w.Header().Set("Retry-After", fmt.Sprint(secs))
		apiError(w, http.StatusTooManyRequests, "rate_limit",
			fmt.Sprintf("лимит %d запросов в минуту на ключ; повтори через %d с", g.cfg.RatePerMin, secs))
		return "лимит"
	}

	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, "invalid_request", "тело — не JSON: "+err.Error())
		return ""
	}
	maxTokens := g.cfg.MaxTokens
	if v, ok := body["max_tokens"].(float64); ok && int(v) > 0 && int(v) < maxTokens {
		maxTokens = int(v)
	}
	body["max_tokens"] = maxTokens
	if g.cfg.Model != "" {
		body["model"] = g.cfg.Model
	}

	prompt, err := g.promptTokens(r.Context(), body["messages"], body["tools"])
	if err != nil {
		apiError(w, http.StatusBadGateway, "upstream", "модель недоступна: "+err.Error())
		return ""
	}
	if prompt+maxTokens > g.cfg.MaxContext {
		apiError(w, http.StatusRequestEntityTooLarge, "context_length_exceeded",
			fmt.Sprintf("промпт %d + ответ %d токенов > окна %d", prompt, maxTokens, g.cfg.MaxContext))
		return fmt.Sprintf("промпт %d", prompt)
	}

	// Очередь: слот модели или ожидание, пока ждущих не больше Queue.
	select {
	case g.slots <- struct{}{}:
	default:
		if g.waiting.Add(1) > int64(g.cfg.Queue) {
			g.waiting.Add(-1)
			apiError(w, http.StatusServiceUnavailable, "overloaded",
				fmt.Sprintf("модель занята: %d в работе, очередь %d полна", g.cfg.Parallel, g.cfg.Queue))
			return "очередь полна"
		}
		select {
		case g.slots <- struct{}{}:
			g.waiting.Add(-1)
		case <-r.Context().Done():
			g.waiting.Add(-1)
			return "клиент ушёл из очереди"
		}
	}
	defer func() { <-g.slots }()

	buf, _ := json.Marshal(body)
	g.proxy(w, r.Context(), http.MethodPost, "/v1/chat/completions", buf)
	return fmt.Sprintf("промпт %d, ответ ≤%d", prompt, maxTokens)
}

// allow — ведро токенов на ключ: пополняется RatePerMin/60 в секунду,
// вмещает Burst. Возвращает, сколько ждать, если пусто.
func (g *Gateway) allow(who string) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.cfg.Now()
	b, ok := g.buckets[who]
	if !ok {
		b = &bucket{tokens: float64(g.cfg.Burst), last: now}
		g.buckets[who] = b
	}
	rate := float64(g.cfg.RatePerMin) / 60
	b.tokens = math.Min(float64(g.cfg.Burst), b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens < 1 {
		return time.Duration((1 - b.tokens) / rate * float64(time.Second)), false
	}
	b.tokens--
	return 0, true
}

// promptTokens считает промпт токенизатором самой модели (/tokenize
// у llama-server): оценка по символам для русского врёт в полтора раза.
// Плюс по 4 токена на сообщение — разметка ролей в шаблоне чата.
// Схемы инструментов и вызовы инструментов модель тоже читает как
// промпт — они идут в счёт JSON-ом, как их и видит шаблон.
func (g *Gateway) promptTokens(ctx context.Context, messages, tools any) (int, error) {
	list, _ := messages.([]any)
	var text strings.Builder
	for _, m := range list {
		if mm, ok := m.(map[string]any); ok {
			if s, ok := mm["content"].(string); ok {
				text.WriteString(s)
				text.WriteString("\n")
			}
			if calls, ok := mm["tool_calls"]; ok {
				b, _ := json.Marshal(calls)
				text.Write(b)
				text.WriteString("\n")
			}
		}
	}
	if tools != nil {
		b, _ := json.Marshal(tools)
		text.Write(b)
	}
	buf, _ := json.Marshal(map[string]any{"content": text.String()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.cfg.Upstream+"/tokenize", bytes.NewReader(buf))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		Tokens []any `json:"tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("/tokenize: %w", err)
	}
	return len(out.Tokens) + 4*len(list), nil
}

// proxy отдаёт ответ модели как есть, поток — кусками по мере прихода.
func (g *Gateway) proxy(w http.ResponseWriter, ctx context.Context, method, path string, body []byte) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.cfg.Upstream+path, rd)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.http.Do(req)
	if err != nil {
		apiError(w, http.StatusBadGateway, "upstream", "модель недоступна: "+err.Error())
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	fl, _ := w.(http.Flusher)
	chunk := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(chunk)
		if n > 0 {
			w.Write(chunk[:n])
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func (g *Gateway) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, g.cfg.Upstream+"/health", nil)
	resp, err := g.http.Do(req)
	status := "ok"
	if err != nil {
		status = "модель недоступна"
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			status = fmt.Sprintf("модель отвечает %d", resp.StatusCode)
		}
	}
	code := http.StatusOK
	if status != "ok" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"status": status, "in_flight": len(g.slots), "waiting": g.waiting.Load()})
}

// Stats — сколько ответов с каким кодом отдал сервис с запуска.
func (g *Gateway) Stats() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	codes := make([]int, 0, len(g.stats))
	for c := range g.stats {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	byCode := map[string]int{}
	for _, c := range codes {
		byCode[fmt.Sprint(c)] = g.stats[c]
	}
	return map[string]any{
		"responses": byCode, "in_flight": len(g.slots), "waiting": g.waiting.Load(),
		"limits": map[string]int{
			"rate_per_min": g.cfg.RatePerMin, "burst": g.cfg.Burst, "parallel": g.cfg.Parallel,
			"queue": g.cfg.Queue, "max_context": g.cfg.MaxContext, "max_tokens": g.cfg.MaxTokens,
		},
	}
}

func apiError(w http.ResponseWriter, code int, typ, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": code}})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
