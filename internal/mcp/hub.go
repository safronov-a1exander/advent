package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/safronov-a1exander/advent/internal/llm"
)

// Hub — все MCP-серверы, к которым может обратиться агент (день 17).
//
// Модели сервер не виден: она получает плоский список функций. Чтобы
// из этого списка можно было вернуться к серверу, имя функции — это
// «сервер__инструмент». Так же устроены и готовые ассистенты, и так
// у двух серверов могут быть инструменты с одинаковым именем.
//
// Серверы поднимаются лениво, при первом запросе их инструментов: локальный
// сервер — это подпроцесс, и держать его без нужды незачем.
type Hub struct {
	specs map[string]Spec
	opts  []Option

	mu      sync.Mutex
	clients map[string]*Client
	tools   map[string][]Tool
}

// NewHub — хаб поверх описаний серверов из config.yaml.
func NewHub(specs []Spec, opts ...Option) *Hub {
	h := &Hub{specs: map[string]Spec{}, opts: opts, clients: map[string]*Client{}, tools: map[string][]Tool{}}
	for _, s := range specs {
		h.specs[s.Name] = s
	}
	return h
}

// Sep — разделитель сервера и инструмента в имени функции для модели.
const Sep = "__"

// FuncName — имя функции для модели. OpenAI-совместимые API пускают
// в имя только латиницу, цифры, _ и -, не длиннее 64 символов.
func FuncName(server, tool string) string {
	n := badName.ReplaceAllString(server, "_") + Sep + badName.ReplaceAllString(tool, "_")
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

var badName = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// client — соединение с сервером; поднимает его при первом обращении.
func (h *Hub) client(ctx context.Context, name string) (*Client, []Tool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.clients[name]; ok {
		return c, h.tools[name], nil
	}
	spec, ok := h.specs[name]
	if !ok {
		return nil, nil, fmt.Errorf("mcp-сервер %q не описан в config.yaml", name)
	}
	c, err := Open(ctx, spec, h.opts...)
	if err != nil {
		return nil, nil, err
	}
	tools, err := c.ListTools(ctx)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	h.clients[name], h.tools[name] = c, tools
	return c, tools, nil
}

// ServerTools — инструменты одного сервера (с подключением, если нужно).
func (h *Hub) ServerTools(ctx context.Context, server string) ([]Tool, error) {
	_, tools, err := h.client(ctx, server)
	return tools, err
}

// Functions — инструменты серверов в виде функций для модели.
func (h *Hub) Functions(ctx context.Context, servers []string) ([]llm.Tool, error) {
	var out []llm.Tool
	for _, s := range servers {
		_, tools, err := h.client(ctx, s)
		if err != nil {
			return nil, err
		}
		for _, t := range tools {
			desc := t.Description
			if desc == "" {
				desc = t.Title
			}
			out = append(out, llm.Tool{Type: "function", Function: llm.ToolFunction{
				Name: FuncName(s, t.Name), Description: desc, Parameters: t.InputSchema,
			}})
		}
	}
	return out, nil
}

// Outcome — чем кончился вызов инструмента: для модели, для экрана и
// для журнала.
type Outcome struct {
	Func    string        `json:"func"`
	Server  string        `json:"server"`
	Tool    string        `json:"tool"`
	Args    string        `json:"args"`
	Text    string        `json:"text"`
	IsError bool          `json:"is_error,omitempty"`
	Latency time.Duration `json:"latency_ns"`
}

// Call выполняет вызов, который попросила модель. Любая беда — неизвестная
// функция, битые аргументы, упавший сервер — возвращается модели текстом
// с IsError: ход не должен ломаться оттого, что модель ошиблась с вызовом,
// она увидит ошибку и попробует иначе.
func (h *Hub) Call(ctx context.Context, fn, args string, allowed []string) Outcome {
	start := time.Now()
	o := Outcome{Func: fn, Args: args}

	server, tool, ok := h.resolve(fn, allowed)
	o.Server, o.Tool = server, tool
	if !ok {
		o.IsError = true
		o.Text = fmt.Sprintf("функции %q нет среди доступных инструментов", fn)
		o.Latency = time.Since(start)
		return o
	}
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(args), &probe); err != nil {
		o.IsError = true
		o.Text = fmt.Sprintf("аргументы не JSON-объект: %v", err)
		o.Latency = time.Since(start)
		return o
	}
	c, _, err := h.client(ctx, server)
	if err != nil {
		o.IsError, o.Text = true, err.Error()
		o.Latency = time.Since(start)
		return o
	}
	res, err := c.CallTool(ctx, tool, json.RawMessage(args))
	if errors.Is(err, ErrSessionExpired) {
		// Удалённый сервер перезапустился, пока агент жил: для сервера
		// с расписанием это обычное дело. Новое рукопожатие и повтор.
		h.drop(server)
		if c, _, err = h.client(ctx, server); err == nil {
			res, err = c.CallTool(ctx, tool, json.RawMessage(args))
		}
	}
	if err != nil {
		o.IsError, o.Text = true, err.Error()
		o.Latency = time.Since(start)
		return o
	}
	o.Text, o.IsError = res.Text(), res.IsError
	o.Latency = time.Since(start)
	return o
}

// resolve — какой сервер и инструмент стоят за именем функции. Сервер
// должен быть среди разрешённых агенту: имя функции пишет модель, и
// «rates__что-то» не должно открывать сервер, который агенту не выдан.
func (h *Hub) resolve(fn string, allowed []string) (server, tool string, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range allowed {
		for _, t := range h.tools[s] {
			if FuncName(s, t.Name) == fn {
				return s, t.Name, true
			}
		}
	}
	if s, t, cut := strings.Cut(fn, Sep); cut {
		return s, t, false
	}
	return "", fn, false
}

// drop забывает соединение с сервером: следующий вызов поднимет новое.
func (h *Hub) drop(server string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.clients[server]; ok {
		c.Close()
		delete(h.clients, server)
		delete(h.tools, server)
	}
}

// Close закрывает все соединения; локальные серверы при этом завершаются.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for n, c := range h.clients {
		c.Close()
		delete(h.clients, n)
	}
}
