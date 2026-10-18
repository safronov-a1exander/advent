package main

import (
	"context"
	"strings"
	"testing"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/mcp"
)

type recBox struct{ calls []string }

func (r *recBox) Functions(context.Context, []string) ([]llm.Tool, error) { return nil, nil }
func (r *recBox) Call(_ context.Context, fn, args string, _ []string) mcp.Outcome {
	r.calls = append(r.calls, fn+" "+args)
	return mcp.Outcome{Func: fn, Text: "ok"}
}

// Сессия поддержки видит только своё обращение — это проверяет код,
// а не промпт.
func TestTicketScopeBlocksOtherTickets(t *testing.T) {
	box := &recBox{}
	s := &ticketScope{Toolbox: box, ticket: "T-1042"}
	if out := s.Call(context.Background(), "crm__user_requests", `{"ticket_id":"t-1042"}`, nil); out.IsError {
		t.Fatalf("своё обращение (в любом регистре) — можно: %s", out.Text)
	}
	out := s.Call(context.Background(), "crm__user_requests", `{"ticket_id":"T-1043"}`, nil)
	if !out.IsError || !strings.Contains(out.Text, "только к обращению T-1042") {
		t.Fatalf("чужое обращение должно отклоняться: %+v", out)
	}
	if len(box.calls) != 1 {
		t.Fatalf("до сервера дошёл только разрешённый вызов: %v", box.calls)
	}
}
