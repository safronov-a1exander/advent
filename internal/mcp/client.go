package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"
)

// Direction — в какую сторону прошло сообщение: для отладочного вывода.
type Direction string

const (
	Out Direction = "→"
	In  Direction = "←"
)

// Trace получает каждое сообщение, ушедшее на сервер и пришедшее от него,
// ровно в том виде, в каком оно было на проводе.
type Trace func(dir Direction, raw []byte)

// Transport — как сообщения доходят до сервера. Клиенту всё равно:
// подпроцесс со stdin/stdout или POST на удалённый адрес.
type Transport interface {
	// Call отправляет запрос и ждёт ответ с тем же id.
	Call(ctx context.Context, id int64, body []byte) (message, error)
	// Notify отправляет уведомление: ответа на него не бывает.
	Notify(ctx context.Context, body []byte) error
	// SetTrace подключает отладочный вывод; nil — выключить.
	SetTrace(Trace)
	// Close закрывает соединение (для HTTP — завершает сессию).
	Close() error
}

// Client — одно соединение с одним MCP-сервером.
type Client struct {
	name  string
	t     Transport
	seq   atomic.Int64
	info  ServerInfo
	trace Trace
	// Elapsed — сколько заняло рукопожатие.
	Elapsed time.Duration
}

// Option настраивает клиента до рукопожатия.
type Option func(*Client)

// WithTrace — печатать всё, что ходит по проводу, начиная с initialize.
func WithTrace(t Trace) Option { return func(c *Client) { c.trace = t } }

// clientInfo — как клиент представляется серверу.
var clientInfo = Implementation{Name: "advent", Title: "Бюджет — ассистент по личным тратам", Version: "day-17"}

// Connect устанавливает соединение: initialize, ответ сервера со своей
// ревизией протокола и возможностями, затем уведомление initialized.
// До этого уведомления сервер вправе не отвечать ни на что, кроме ping.
func Connect(ctx context.Context, name string, t Transport, opts ...Option) (*Client, error) {
	c := &Client{name: name, t: t}
	for _, o := range opts {
		o(c)
	}
	t.SetTrace(c.trace)

	start := time.Now()
	raw, err := c.call(ctx, "initialize", initializeParams{
		ProtocolVersion: ProtocolVersion,
		// Клиент ничего не предлагает серверу: ни корней файловой системы,
		// ни сэмплинга через нашу модель. Только спрашивает сам.
		Capabilities: map[string]any{},
		ClientInfo:   clientInfo,
	})
	if err != nil {
		t.Close()
		return nil, fmt.Errorf("%s: initialize: %w", name, err)
	}
	if err := json.Unmarshal(raw, &c.info); err != nil {
		t.Close()
		return nil, fmt.Errorf("%s: ответ на initialize не разобрать: %w", name, err)
	}
	if c.info.ProtocolVersion == "" {
		t.Close()
		return nil, fmt.Errorf("%s: сервер не назвал версию протокола", name)
	}
	if v, ok := t.(interface{ setProtocol(string) }); ok {
		v.setProtocol(c.info.ProtocolVersion)
	}
	if err := c.notify(ctx, "notifications/initialized", nil); err != nil {
		t.Close()
		return nil, fmt.Errorf("%s: notifications/initialized: %w", name, err)
	}
	c.Elapsed = time.Since(start)
	return c, nil
}

// Name — имя сервера, под которым его знает клиент (из config.yaml).
func (c *Client) Name() string { return c.name }

// Info — что сервер сообщил о себе при рукопожатии.
func (c *Client) Info() ServerInfo { return c.info }

// Transport — через что идёт соединение.
func (c *Client) Transport() Transport { return c.t }

// Close закрывает соединение.
func (c *Client) Close() error { return c.t.Close() }

// ErrNoTools — сервер не объявил возможность tools.
var ErrNoTools = errors.New("сервер не предоставляет инструменты")

// ListTools — все инструменты сервера. Список может приходить страницами:
// пока сервер возвращает nextCursor, спрашиваем следующую.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	if !c.info.Has("tools") {
		return nil, ErrNoTools
	}
	var all []Tool
	cursor := ""
	for page := 0; ; page++ {
		if page > 100 {
			return all, fmt.Errorf("%s: tools/list не кончается после %d страниц", c.name, page)
		}
		var p any
		if cursor != "" {
			p = listToolsParams{Cursor: cursor}
		}
		raw, err := c.call(ctx, "tools/list", p)
		if err != nil {
			return all, fmt.Errorf("%s: tools/list: %w", c.name, err)
		}
		var res listToolsResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return all, fmt.Errorf("%s: ответ tools/list не разобрать: %w", c.name, err)
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			return all, nil
		}
		cursor = res.NextCursor
	}
}

// CallTool вызывает инструмент. args — JSON-объект аргументов, как его
// собрала модель; nil — без аргументов.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallResult, error) {
	raw, err := c.call(ctx, "tools/call", callToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("%s: tools/call %s: %w", c.name, name, err)
	}
	var res CallResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("%s: ответ tools/call не разобрать: %w", c.name, err)
	}
	return &res, nil
}

// call — вызов метода с ожиданием результата.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.seq.Add(1)
	body, err := json.Marshal(request{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	m, err := c.t.Call(ctx, id, body)
	if err != nil {
		return nil, err
	}
	if m.Error != nil {
		return nil, m.Error
	}
	return m.Result, nil
}

// notify — уведомление без ответа.
func (c *Client) notify(ctx context.Context, method string, params any) error {
	body, err := json.Marshal(request{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	return c.t.Notify(ctx, body)
}

// idOf — числовой id ответа. JSON-RPC разрешает и строки, и числа;
// мы шлём числа, но сервер вправе вернуть «1» строкой.
func idOf(raw json.RawMessage) (int64, bool) {
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			return v, true
		}
	}
	return 0, false
}
