package main

import (
	"strings"
	"testing"
)

func tools(names ...string) []toolDef {
	var out []toolDef
	for _, n := range names {
		var t toolDef
		t.Type, t.Function.Name = "function", n
		out = append(out, t)
	}
	return out
}

func TestMockCallsToolThenAnswers(t *testing.T) {
	req := chatReq{Tools: tools("rates__exchange_rate", "rates__convert"),
		Messages: []message{{Role: "user", Content: "Сколько в рублях 10 долларов?"}}}
	calls, _, ok := mockTools(req)
	if !ok || len(calls) != 1 || calls[0].Function.Name != "rates__convert" ||
		calls[0].Function.Arguments != `{"amount":10,"from":"USD"}` {
		t.Fatalf("первый круг: %+v", calls)
	}
	req.Messages = append(req.Messages,
		message{Role: "assistant", ToolCalls: calls},
		message{Role: "tool", ToolCallID: calls[0].ID, Content: "10 USD = 843.41 RUB"})
	calls, text, ok := mockTools(req)
	if !ok || len(calls) != 0 || !strings.Contains(text, "843.41") {
		t.Fatalf("второй круг: %+v %q", calls, text)
	}
}

func TestMockWithoutMatchingToolFallsBack(t *testing.T) {
	req := chatReq{Tools: tools("rates__convert"), Messages: []message{{Role: "user", Content: "Привет!"}}}
	if _, _, ok := mockTools(req); ok {
		t.Fatal("на «привет» инструмент не нужен")
	}
}
