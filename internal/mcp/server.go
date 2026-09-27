package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"
)

// Handler — код инструмента. args — объект аргументов в том виде,
// в каком его прислал клиент. Ошибка возвращается как результат
// с isError: модели её нужно увидеть и поправить вызов.
type Handler func(ctx context.Context, args json.RawMessage) CallResult

// ServerTool — инструмент вместе с его кодом.
type ServerTool struct {
	Tool
	Handle Handler
}

// Server — MCP-сервер: регистрация инструментов и разбор JSON-RPC.
// Транспорт у него снаружи: ServeStdio для подпроцесса, HTTPHandler
// для удалённого (день 18).
type Server struct {
	info         Implementation
	instructions string

	mu    sync.RWMutex
	tools map[string]ServerTool
	order []string
	// Log — журнал сервера. Для stdio это обязательно stderr: stdout занят
	// протоколом, и любая строка туда ломает клиенту разбор.
	Log *log.Logger
}

// NewServer — сервер с именем и версией для ответа на initialize.
func NewServer(name, version, instructions string) *Server {
	return &Server{
		info:         Implementation{Name: name, Version: version},
		instructions: instructions,
		tools:        map[string]ServerTool{},
		Log:          log.New(io.Discard, "", 0),
	}
}

// Register добавляет инструмент. Схема аргументов обязательна: без неё
// модель не знает, что передавать, и начинает угадывать.
func (s *Server) Register(t Tool, h Handler) {
	if len(t.InputSchema) == 0 {
		t.InputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.tools[t.Name]; !dup {
		s.order = append(s.order, t.Name)
	}
	s.tools[t.Name] = ServerTool{Tool: t, Handle: h}
}

// Tools — зарегистрированные инструменты в порядке регистрации.
func (s *Server) Tools() []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tool, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, s.tools[n].Tool)
	}
	return out
}

// Handle разбирает одно входящее сообщение и возвращает ответ.
// nil — ответа не нужно (уведомление или ответ клиента на наш запрос).
func (s *Server) Handle(ctx context.Context, raw []byte) []byte {
	var m message
	if err := json.Unmarshal(raw, &m); err != nil {
		return encode(nil, nil, &RPCError{Code: -32700, Message: "Parse error: " + err.Error()})
	}
	if m.Method == "" {
		return nil // ответ клиента на наш запрос: мы их не шлём
	}
	notification := len(m.ID) == 0
	result, rerr := s.dispatch(ctx, m)
	if notification {
		return nil
	}
	return encode(m.ID, result, rerr)
}

func (s *Server) dispatch(ctx context.Context, m message) (any, *RPCError) {
	switch m.Method {
	case "initialize":
		var p initializeParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, &RPCError{Code: -32602, Message: "Invalid params: " + err.Error()}
		}
		s.Log.Printf("initialize от %s %s, протокол %s", p.ClientInfo.Name, p.ClientInfo.Version, p.ProtocolVersion)
		return map[string]any{
			// Отвечаем ревизией клиента, если знаем её, иначе своей —
			// так требует спецификация.
			"protocolVersion": negotiate(p.ProtocolVersion),
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      s.info,
			"instructions":    s.instructions,
		}, nil
	case "notifications/initialized", "notifications/cancelled":
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return listToolsResult{Tools: s.Tools()}, nil
	case "tools/call":
		var p callToolParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, &RPCError{Code: -32602, Message: "Invalid params: " + err.Error()}
		}
		s.mu.RLock()
		t, ok := s.tools[p.Name]
		s.mu.RUnlock()
		if !ok {
			return nil, &RPCError{Code: -32602, Message: "Unknown tool: " + p.Name}
		}
		args := p.Arguments
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage(`{}`)
		}
		res := t.Handle(ctx, args)
		s.Log.Printf("tools/call %s %s → %s%s", p.Name, compact(args), errMark(res), cutRunes(res.Text(), 200))
		return res, nil
	}
	if strings.HasPrefix(m.Method, "notifications/") {
		return nil, nil
	}
	return nil, &RPCError{Code: -32601, Message: "Method not found: " + m.Method}
}

// supported — ревизии протокола, на которых умеет говорить сервер.
var supported = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

func negotiate(asked string) string {
	for _, v := range supported {
		if v == asked {
			return v
		}
	}
	return supported[0]
}

func encode(id json.RawMessage, result any, rerr *RPCError) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	out := map[string]any{"jsonrpc": "2.0", "id": id}
	if rerr != nil {
		out["error"] = rerr
	} else {
		out["result"] = result
	}
	b, err := json.Marshal(out)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id,
			"error": RPCError{Code: -32603, Message: err.Error()}})
	}
	return b
}

// ServeStdio обслуживает клиента, который запустил сервер подпроцессом:
// сообщение на строку из r, ответ на строку в w. Кончается, когда клиент
// закрыл stdin.
func (s *Server) ServeStdio(ctx context.Context, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		// Каждый вызов в своей горутине: медленный инструмент не должен
		// держать ping и соседние вызовы.
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out := s.Handle(ctx, line); out != nil {
				mu.Lock()
				w.Write(append(out, '\n'))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return sc.Err()
}

// SortedNames — имена инструментов по алфавиту, для печати.
func (s *Server) SortedNames() []string {
	var n []string
	for _, t := range s.Tools() {
		n = append(n, t.Name)
	}
	sort.Strings(n)
	return n
}

func compact(raw json.RawMessage) string {
	return strings.Join(strings.Fields(string(raw)), " ")
}

func errMark(r CallResult) string {
	if r.IsError {
		return "ОШИБКА "
	}
	return ""
}

func cutRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Args разбирает аргументы инструмента в структуру. Ошибка уже
// оформлена как результат для модели.
func Args[T any](raw json.RawMessage) (T, *CallResult) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		r := ErrorResult("аргументы не разобрать: %v", err)
		return v, &r
	}
	return v, nil
}

// Prop — один аргумент в схеме инструмента.
type Prop struct {
	Name        string
	Type        string // string | number | integer | boolean | array | object
	Description string
	Required    bool
	Enum        []string
	// Items — схема элемента для массива.
	Items json.RawMessage
}

// Schema собирает JSON Schema объекта из аргументов — в том порядке,
// в каком они перечислены: модель читает схему сверху вниз, и главное
// должно стоять первым.
func Schema(props ...Prop) json.RawMessage {
	var b strings.Builder
	b.WriteString(`{"type":"object","properties":{`)
	var req []string
	for i, p := range props {
		if i > 0 {
			b.WriteByte(',')
		}
		name, _ := json.Marshal(p.Name)
		d := map[string]any{"type": p.Type}
		if p.Description != "" {
			d["description"] = p.Description
		}
		if len(p.Enum) > 0 {
			d["enum"] = p.Enum
		}
		if len(p.Items) > 0 {
			d["items"] = p.Items
		}
		body, err := json.Marshal(d)
		if err != nil {
			panic(fmt.Sprintf("схема %s: %v", p.Name, err))
		}
		b.Write(name)
		b.WriteByte(':')
		b.Write(body)
		if p.Required {
			req = append(req, p.Name)
		}
	}
	b.WriteString("}")
	if len(req) > 0 {
		r, _ := json.Marshal(req)
		b.WriteString(`,"required":`)
		b.Write(r)
	}
	b.WriteString("}")
	return json.RawMessage(b.String())
}
