// Package mcp — клиент Model Context Protocol (день 16).
//
// MCP — не фреймворк, а протокол: JSON-RPC 2.0 поверх одного из двух
// транспортов. Локальный сервер запускается подпроцессом и говорит через
// stdin/stdout, удалённый принимает POST на один адрес (Streamable HTTP)
// и отвечает либо JSON, либо потоком SSE.
//
// Здесь он написан руками, как на первой неделе клиент к LLM: SDK есть
// почти на каждом языке, но за ним не видно, что ходит по проводу. А по
// проводу ходит немного — рукопожатие initialize, уведомление initialized
// и дальше обычные вызовы методов вроде tools/list.
package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ProtocolVersion — ревизия спецификации, которую просит клиент.
// Сервер отвечает своей, и дальше обе стороны говорят на ней.
const ProtocolVersion = "2025-06-18"

// ---- JSON-RPC 2.0 ----

// request — вызов метода. У уведомления нет id: ответа на него не будет.
type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// message — любое входящее сообщение: ответ на наш вызов, уведомление
// или встречный запрос сервера. Различаются по тому, какие поля заполнены.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// isResponse — это ответ на вызов, а не запрос или уведомление.
func (m message) isResponse() bool { return m.Method == "" && len(m.ID) > 0 }

// RPCError — ошибка уровня протокола: метода нет, параметры не те.
// Ошибка самого инструмента сюда не попадает — она приходит обычным
// результатом с флагом isError (см. CallResult).
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("mcp %d: %s", e.Code, e.Message) }

// ---- рукопожатие ----

// Implementation — кто на том конце: имя и версия.
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      Implementation `json:"clientInfo"`
}

// ServerInfo — что сервер сообщил о себе в ответ на initialize.
type ServerInfo struct {
	ProtocolVersion string                     `json:"protocolVersion"`
	Capabilities    map[string]json.RawMessage `json:"capabilities"`
	Server          Implementation             `json:"serverInfo"`
	// Instructions — необязательная подсказка модели, как пользоваться
	// сервером. Это тоже текст, который уедет в промпт, если его туда взять.
	Instructions string `json:"instructions,omitempty"`
}

// Has — объявил ли сервер возможность (tools, resources, prompts…).
func (s ServerInfo) Has(capability string) bool {
	_, ok := s.Capabilities[capability]
	return ok
}

// ---- инструменты ----

// Tool — описание инструмента из tools/list. InputSchema — JSON Schema
// аргументов; её модель читает, чтобы понять, что и как передать.
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations *Annotations    `json:"annotations,omitempty"`
}

// Annotations — подсказки о поведении инструмента. Сервер их только
// заявляет: проверить, что «только чтение» правда только читает, клиент
// не может, и спецификация прямо просит им не доверять.
type Annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

type listToolsParams struct {
	Cursor string `json:"cursor,omitempty"`
}

type listToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// Param — один аргумент инструмента, вытащенный из схемы для печати.
type Param struct {
	Name        string
	Type        string
	Required    bool
	Description string
}

// Params разбирает InputSchema до списка аргументов. Схема может быть
// какой угодно, но у инструментов почти всегда это object с properties —
// этого и хватает, чтобы показать человеку, что инструмент принимает.
func (t Tool) Params() []Param {
	var s struct {
		Properties map[string]struct {
			Type        any    `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(t.InputSchema, &s); err != nil {
		return nil
	}
	req := map[string]bool{}
	for _, r := range s.Required {
		req[r] = true
	}
	order := propertyOrder(t.InputSchema)
	out := make([]Param, 0, len(s.Properties))
	for _, name := range order {
		p, ok := s.Properties[name]
		if !ok {
			continue
		}
		typ := ""
		switch v := p.Type.(type) {
		case string:
			typ = v
		case []any:
			for i, x := range v {
				if i > 0 {
					typ += "|"
				}
				typ += fmt.Sprint(x)
			}
		}
		out = append(out, Param{Name: name, Type: typ, Required: req[name], Description: p.Description})
	}
	return out
}

// propertyOrder — имена properties в том порядке, в каком их написал
// сервер. Обычный разбор в map порядок теряет, а в описании инструмента
// он осмысленный: сначала главное.
func propertyOrder(schema json.RawMessage) []string {
	var top map[string]json.RawMessage
	if json.Unmarshal(schema, &top) != nil {
		return nil
	}
	raw, ok := top["properties"]
	if !ok {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var names []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return names
		}
		name, _ := t.(string)
		names = append(names, name)
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return names
		}
	}
	return names
}
