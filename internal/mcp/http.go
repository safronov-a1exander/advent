package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

// HTTP — транспорт Streamable HTTP: каждое сообщение клиента — отдельный
// POST на один и тот же адрес. Сервер отвечает на запрос либо обычным
// JSON, либо потоком SSE, в котором среди прочего придёт и ответ; на
// уведомление — пустым 202.
//
// Сессия у такого транспорта — не соединение, а заголовок: если сервер
// выдал Mcp-Session-Id в ответ на initialize, клиент обязан слать его
// дальше в каждом запросе. Без него сервер с сессиями ответит 400.
type HTTP struct {
	url     string
	headers map[string]string
	http    *http.Client

	mu       sync.Mutex
	session  string
	protocol string
	trace    Trace
}

// NewHTTP — транспорт к удалённому серверу. headers уходят в каждый запрос
// (например Authorization для серверов с ключом).
func NewHTTP(url string, headers map[string]string) *HTTP {
	return &HTTP{url: url, headers: headers, http: &http.Client{Timeout: 2 * time.Minute}}
}

func (h *HTTP) SetTrace(t Trace) {
	h.mu.Lock()
	h.trace = t
	h.mu.Unlock()
}

// Session — id сессии, если сервер его выдал.
func (h *HTTP) Session() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.session
}

func (h *HTTP) setProtocol(v string) {
	h.mu.Lock()
	h.protocol = v
	h.mu.Unlock()
}

func (h *HTTP) emit(dir Direction, raw []byte) {
	h.mu.Lock()
	t := h.trace
	h.mu.Unlock()
	if t != nil {
		t(dir, raw)
	}
}

func (h *HTTP) post(ctx context.Context, body []byte) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	// Клиент обязан принимать оба вида ответа: какой выбрать, решает сервер.
	r.Header.Set("Accept", "application/json, text/event-stream")
	h.mu.Lock()
	if h.session != "" {
		r.Header.Set("Mcp-Session-Id", h.session)
	}
	if h.protocol != "" {
		r.Header.Set("MCP-Protocol-Version", h.protocol)
	}
	h.mu.Unlock()
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	h.emit(Out, body)
	return h.http.Do(r)
}

func (h *HTTP) Call(ctx context.Context, id int64, body []byte) (message, error) {
	resp, err := h.post(ctx, body)
	if err != nil {
		return message{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusNotFound && h.Session() != "" {
			return message{}, fmt.Errorf("сессия %s больше не действует: нужно заново initialize", h.Session())
		}
		return message{}, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		h.mu.Lock()
		h.session = s
		h.mu.Unlock()
	}

	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch ct {
	case "text/event-stream":
		return h.readStream(resp.Body, id)
	default:
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return message{}, err
		}
		h.emit(In, raw)
		var m message
		if err := json.Unmarshal(raw, &m); err != nil {
			return message{}, fmt.Errorf("ответ не JSON-RPC: %w (%.200s)", err, raw)
		}
		return m, nil
	}
}

// readStream читает SSE, пока не придёт ответ с нашим id. До него сервер
// может прислать что угодно — прогресс, логи, собственные запросы, — это
// законно и не должно ломать вызов.
func (h *HTTP) readStream(body io.Reader, id int64) (message, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var data strings.Builder
	flush := func() (message, bool) {
		defer data.Reset()
		if data.Len() == 0 {
			return message{}, false
		}
		raw := []byte(data.String())
		h.emit(In, raw)
		var m message
		if json.Unmarshal(raw, &m) != nil || !m.isResponse() {
			return message{}, false
		}
		if got, ok := idOf(m.ID); !ok || got != id {
			return message{}, false
		}
		return m, true
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			// пустая строка закрывает событие
			if m, ok := flush(); ok {
				return m, nil
			}
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		// event:, id:, retry: и комментарии нам не нужны
	}
	if m, ok := flush(); ok {
		return m, nil
	}
	if err := sc.Err(); err != nil {
		return message{}, err
	}
	return message{}, fmt.Errorf("поток закрылся, а ответа на запрос %d так и не было", id)
}

func (h *HTTP) Notify(ctx context.Context, body []byte) error {
	resp, err := h.post(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return nil
}

// Close завершает сессию, если она была: DELETE с её id. Сервер вправе
// ответить 405 — значит, завершать сессии явно у него нельзя, и это не ошибка.
func (h *HTTP) Close() error {
	s := h.Session()
	if s == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodDelete, h.url, nil)
	if err != nil {
		return err
	}
	r.Header.Set("Mcp-Session-Id", s)
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	resp, err := h.http.Do(r)
	if err != nil {
		return nil
	}
	resp.Body.Close()
	return nil
}
