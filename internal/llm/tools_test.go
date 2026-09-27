package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// В потоке вызов функции приходит кусками: имя в первом, аргументы
// по частям в следующих, и два вызова перемежаются по index.
func TestStreamAccumulatesToolCalls(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"rates__convert","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","type":"function","function":{"name":"rates__exchange_rate","arguments":"{\"currency\""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"amount\":10,"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"from\":\"USD\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":":\"EUR\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`,
	}
	var got wireReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := New("t", srv.URL, "k")
	tool := Tool{Type: "function", Function: ToolFunction{Name: "rates__convert", Parameters: json.RawMessage(`{"type":"object"}`)}}
	resp, err := c.ChatStream(context.Background(), Request{Model: "m", Tools: []Tool{tool},
		Messages: []Message{{Role: RoleUser, Content: "?"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 1 || got.Tools[0].Function.Name != "rates__convert" {
		t.Fatalf("инструменты не ушли в запрос: %+v", got.Tools)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("вызовы: %+v", resp.ToolCalls)
	}
	a, b := resp.ToolCalls[0], resp.ToolCalls[1]
	if a.ID != "c1" || a.Function.Arguments != `{"amount":10,"from":"USD"}` {
		t.Fatalf("первый вызов: %+v", a)
	}
	if b.Function.Name != "rates__exchange_rate" || b.Function.Arguments != `{"currency":"EUR"}` {
		t.Fatalf("второй вызов: %+v", b)
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason: %q", resp.FinishReason)
	}
}

func TestChatParsesToolCallsAndSendsToolMessages(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"x","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	defer srv.Close()
	c := New("t", srv.URL, "k")
	call := ToolCall{ID: "prev", Type: "function"}
	call.Function.Name = "f"
	resp, err := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{
		{Role: RoleUser, Content: "?"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{call}},
		{Role: RoleTool, ToolCallID: "prev", Content: "42"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "x" {
		t.Fatalf("вызовы: %+v", resp.ToolCalls)
	}
	for _, want := range []string{`"tool_call_id":"prev"`, `"role":"tool"`, `"tool_calls":[{"id":"prev"`} {
		if !strings.Contains(body, want) {
			t.Errorf("в запросе нет %s: %s", want, body)
		}
	}
	// Обычное сообщение не должно тащить пустые поля инструментов.
	if strings.Contains(body, `"tool_call_id":""`) || strings.Contains(body, `"reasoning_content":""`) {
		t.Errorf("лишние пустые поля: %s", body)
	}
}
