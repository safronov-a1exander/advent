package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeServer — удалённый MCP-сервер в миниатюре: отвечает JSON или SSE,
// выдаёт сессию и режет список инструментов на страницы.
type fakeServer struct {
	sse      bool
	session  string
	pages    [][]Tool
	noTools  bool
	mu       sync.Mutex
	methods  []string
	sessions []string // какой Mcp-Session-Id пришёл с каждым запросом
	versions []string // какой MCP-Protocol-Version пришёл с каждым запросом
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var m message
	if err := json.Unmarshal(body, &m); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	f.mu.Lock()
	f.methods = append(f.methods, m.Method)
	f.sessions = append(f.sessions, r.Header.Get("Mcp-Session-Id"))
	f.versions = append(f.versions, r.Header.Get("MCP-Protocol-Version"))
	f.mu.Unlock()

	if len(m.ID) == 0 { // уведомление
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if f.session != "" && m.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != f.session {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}

	var result any
	switch m.Method {
	case "initialize":
		caps := map[string]any{"tools": map[string]any{}}
		if f.noTools {
			caps = map[string]any{"prompts": map[string]any{}}
		}
		result = map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    caps,
			"serverInfo":      map[string]any{"name": "fake", "version": "1.0"},
		}
		if f.session != "" {
			w.Header().Set("Mcp-Session-Id", f.session)
		}
	case "tools/list":
		var p listToolsParams
		json.Unmarshal(m.Params, &p)
		page := 0
		if p.Cursor != "" {
			fmt.Sscanf(p.Cursor, "page-%d", &page)
		}
		res := listToolsResult{Tools: f.pages[page]}
		if page+1 < len(f.pages) {
			res.NextCursor = fmt.Sprintf("page-%d", page+1)
		}
		result = res
	default:
		f.reply(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"Method not found"}}`, m.ID))
		return
	}
	raw, _ := json.Marshal(result)
	f.reply(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, m.ID, raw))
}

func (f *fakeServer) reply(w http.ResponseWriter, msg string) {
	if !f.sse {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, msg)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	// До ответа — чужие сообщения: уведомление о прогрессе и встречный
	// запрос сервера. Клиент обязан их пропустить, а не принять за ответ.
	io.WriteString(w, ": keep-alive\n\n")
	io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}\n\n")
	io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":999,\"method\":\"ping\"}\n\n")
	io.WriteString(w, "event: message\ndata: "+msg+"\n\n")
}

func tool(name string) Tool {
	return Tool{Name: name, Description: "инструмент " + name,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"z":{"type":"string"},"a":{"type":"integer"}},"required":["z"]}`)}
}

func TestConnectAndListJSON(t *testing.T) {
	f := &fakeServer{pages: [][]Tool{{tool("one"), tool("two")}}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	cl, err := Connect(context.Background(), "fake", NewHTTP(srv.URL, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if v := cl.Info().ProtocolVersion; v != "2025-03-26" {
		t.Fatalf("клиент должен принять версию сервера, а взял %q", v)
	}
	tools, err := cl.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "one" {
		t.Fatalf("инструменты: %+v", tools)
	}
	want := []string{"initialize", "notifications/initialized", "tools/list"}
	if strings.Join(f.methods, ",") != strings.Join(want, ",") {
		t.Fatalf("порядок рукопожатия: %v, ждали %v", f.methods, want)
	}
	// После initialize клиент шлёт согласованную версию в заголовке.
	if f.versions[0] != "" || f.versions[2] != "2025-03-26" {
		t.Fatalf("MCP-Protocol-Version по запросам: %q", f.versions)
	}
}

func TestSSEResponseSkipsForeignMessages(t *testing.T) {
	f := &fakeServer{sse: true, pages: [][]Tool{{tool("one")}}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	cl, err := Connect(context.Background(), "fake", NewHTTP(srv.URL, nil))
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cl.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("инструменты: %+v", tools)
	}
}

func TestSessionHeaderIsSentAfterInitialize(t *testing.T) {
	f := &fakeServer{session: "s-42", pages: [][]Tool{{tool("one")}}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	h := NewHTTP(srv.URL, nil)
	cl, err := Connect(context.Background(), "fake", h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.ListTools(context.Background()); err != nil {
		t.Fatalf("сервер с сессией отказал: %v", err)
	}
	if h.Session() != "s-42" {
		t.Fatalf("сессия: %q", h.Session())
	}
	if f.sessions[0] != "" || f.sessions[1] != "s-42" || f.sessions[2] != "s-42" {
		t.Fatalf("Mcp-Session-Id по запросам: %q", f.sessions)
	}
}

func TestExpiredSessionIsExplained(t *testing.T) {
	f := &fakeServer{session: "s-1", pages: [][]Tool{{tool("one")}}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	h := NewHTTP(srv.URL, nil)
	cl, err := Connect(context.Background(), "fake", h)
	if err != nil {
		t.Fatal(err)
	}
	f.session = "s-2" // сервер перезапустился и забыл старую сессию
	_, err = cl.ListTools(context.Background())
	if err == nil || !strings.Contains(err.Error(), "initialize") {
		t.Fatalf("ждали объяснение про истёкшую сессию, получили %v", err)
	}
}

func TestPagination(t *testing.T) {
	f := &fakeServer{pages: [][]Tool{{tool("a"), tool("b")}, {tool("c")}, {tool("d")}}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	cl, err := Connect(context.Background(), "fake", NewHTTP(srv.URL, nil))
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cl.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, "") != "abcd" {
		t.Fatalf("страницы склеились не так: %v", names)
	}
}

func TestNoToolsCapability(t *testing.T) {
	f := &fakeServer{noTools: true}
	srv := httptest.NewServer(f)
	defer srv.Close()

	cl, err := Connect(context.Background(), "fake", NewHTTP(srv.URL, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.ListTools(context.Background()); err != ErrNoTools {
		t.Fatalf("сервер без tools: ждали ErrNoTools, получили %v", err)
	}
}

func TestRPCErrorSurfaces(t *testing.T) {
	f := &fakeServer{pages: [][]Tool{{tool("one")}}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	cl, err := Connect(context.Background(), "fake", NewHTTP(srv.URL, nil))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cl.call(context.Background(), "resources/list", nil)
	var rpc *RPCError
	if err == nil || !asRPC(err, &rpc) || rpc.Code != -32601 {
		t.Fatalf("ждали ошибку -32601, получили %v", err)
	}
}

func asRPC(err error, out **RPCError) bool {
	r, ok := err.(*RPCError)
	if ok {
		*out = r
	}
	return ok
}

func TestParamsKeepServerOrder(t *testing.T) {
	ps := tool("x").Params()
	if len(ps) != 2 || ps[0].Name != "z" || !ps[0].Required || ps[1].Name != "a" || ps[1].Required {
		t.Fatalf("аргументы: %+v", ps)
	}
}

func TestTraceSeesWire(t *testing.T) {
	f := &fakeServer{pages: [][]Tool{{tool("one")}}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	var dirs []Direction
	cl, err := Connect(context.Background(), "fake", NewHTTP(srv.URL, nil),
		WithTrace(func(d Direction, raw []byte) { dirs = append(dirs, d) }))
	if err != nil {
		t.Fatal(err)
	}
	cl.ListTools(context.Background())
	// initialize туда и обратно, initialized туда, tools/list туда и обратно
	want := []Direction{Out, In, Out, Out, In}
	if fmt.Sprint(dirs) != fmt.Sprint(want) {
		t.Fatalf("трасса: %v, ждали %v", dirs, want)
	}
}
