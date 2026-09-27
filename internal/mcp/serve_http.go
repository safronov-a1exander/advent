package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sync"
)

// HTTPHandler — сервер на транспорте Streamable HTTP (день 18).
//
// Локальный сервер живёт, пока жив клиент: закрылся чат — закрылся stdin —
// сервер ушёл. Серверу с расписанием так нельзя: он должен собирать данные,
// когда к нему никто не подключён. Поэтому он работает отдельным процессом
// и принимает клиентов по HTTP, а клиенты приходят и уходят.
//
// Отвечает всегда обычным JSON: SSE нужен серверу, который сам шлёт
// уведомления посреди вызова, а наши инструменты отвечают сразу.
// Спецификация разрешает серверу выбрать любой из двух видов.
func (s *Server) HTTPHandler() http.Handler {
	h := &httpServer{srv: s, sessions: map[string]bool{}}
	return h
}

type httpServer struct {
	srv      *Server
	mu       sync.Mutex
	sessions map[string]bool
}

func (h *httpServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		// Клиент завершает сессию явно.
		h.mu.Lock()
		delete(h.sessions, r.Header.Get("Mcp-Session-Id"))
		h.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		// GET открыл бы поток уведомлений от сервера; их у нас нет.
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var m message
	if err := json.Unmarshal(body, &m); err != nil {
		writeRPC(w, encode(nil, nil, &RPCError{Code: -32700, Message: "Parse error: " + err.Error()}))
		return
	}

	session := r.Header.Get("Mcp-Session-Id")
	if m.Method == "initialize" {
		// Сессия выдаётся на рукопожатии и дальше живёт заголовком.
		session = newSessionID()
		h.mu.Lock()
		h.sessions[session] = true
		h.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", session)
	} else {
		h.mu.Lock()
		known := h.sessions[session]
		h.mu.Unlock()
		if !known {
			// Сервер перезапустился или сессию закрыли: 404 — сигнал
			// клиенту начать заново с initialize.
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
	}

	out := h.srv.Handle(r.Context(), body)
	if out == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPC(w, out)
}

func writeRPC(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func newSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
