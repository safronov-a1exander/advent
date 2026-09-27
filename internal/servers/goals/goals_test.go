package goals

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/safronov-a1exander/advent/internal/mcp"
)

func callTool(t *testing.T, s *mcp.Server, tool, args string) string {
	t.Helper()
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}`
	var m struct {
		Result mcp.CallResult `json:"result"`
	}
	if err := json.Unmarshal(s.Handle(context.Background(), []byte(req)), &m); err != nil {
		t.Fatal(err)
	}
	out := m.Result.Text()
	if m.Result.IsError {
		out = "ERR " + out
	}
	return out
}

func TestGoalPlan(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC) }
	path := filepath.Join(t.TempDir(), "goals.json")
	s, err := New(path, now)
	if err != nil {
		t.Fatal(err)
	}
	r := callTool(t, s, "add_goal", `{"name":"Стамбул","amount_rub":103674,"deadline":"2027-03-01"}`)
	if !strings.Contains(r, "полных месяцев: 5") {
		t.Fatalf("add_goal: %s", r)
	}
	r = callTool(t, s, "plan_goal", `{"name":"стамбул","monthly_rub":10000}`)
	if !strings.Contains(r, "нужно месяцев: 11") || !strings.Contains(r, "НЕ успеваешь") || !strings.Contains(r, "20735 RUB в месяц") {
		t.Fatalf("plan_goal: %s", r)
	}
	if r := callTool(t, s, "plan_goal", `{"name":"Стамбул","monthly_rub":25000}`); !strings.Contains(r, "успеваешь: накопится к 02.2027") {
		t.Fatalf("plan_goal с запасом: %s", r)
	}
	// Переживает перезапуск.
	s, _ = New(path, now)
	if r := callTool(t, s, "list_goals", `{}`); !strings.Contains(r, "103674 RUB к 2027-03-01") {
		t.Fatalf("list_goals после перезапуска: %s", r)
	}
	for args, want := range map[string]string{
		`{"name":"x","amount_rub":1,"deadline":"2020-01-01"}`: "прошёл",
		`{"name":"x","amount_rub":0,"deadline":"2027-01-01"}`: "больше нуля",
		`{"name":"x","amount_rub":5,"deadline":"1 марта"}`:    "ГГГГ-ММ-ДД",
	} {
		if r := callTool(t, s, "add_goal", args); !strings.HasPrefix(r, "ERR") || !strings.Contains(r, want) {
			t.Errorf("%s: %s", args, r)
		}
	}
	if r := callTool(t, s, "plan_goal", `{"name":"Луна","monthly_rub":1}`); !strings.Contains(r, "add_goal") {
		t.Fatalf("план для несуществующей цели: %s", r)
	}
}
