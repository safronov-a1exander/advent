package crm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func call(t *testing.T, s *Store, tool, args string) (string, bool) {
	t.Helper()
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}`
	var resp struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(New(s).Handle(context.Background(), []byte(req)), &resp); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range resp.Result.Content {
		b.WriteString(c.Text)
	}
	return b.String(), resp.Result.IsError
}

func open(t *testing.T) (*Store, string) {
	t.Helper()
	seed := filepath.Join("..", "..", "..", "data", "support", "crm.json")
	state := filepath.Join(t.TempDir(), "crm.json")
	s, err := Open(seed, state, func() time.Time { return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	return s, state
}

func TestTicketCardHasAuthorKeysAndLimit(t *testing.T) {
	s, _ := open(t)
	out, bad := call(t, s, "get_ticket", `{"ticket_id":"t-1042"}`)
	if bad {
		t.Fatal(out)
	}
	for _, want := range []string{"Почему не работает авторизация", `"name": "Анна"`, "телефон-анна", "revoked", "общий домашний чат", `"rate_per_min": 30`} {
		if !strings.Contains(out, want) {
			t.Errorf("в карточке нет %q", want)
		}
	}
	if strings.Contains(out, "anna@home.lan") {
		t.Error("почта автору поддержки не нужна — в карточку она не идёт")
	}
}

func TestUserRequestsOnlyAuthorsKeys(t *testing.T) {
	s, _ := open(t)
	out, _ := call(t, s, "user_requests", `{"ticket_id":"T-1042"}`)
	if !strings.Contains(out, "401×3") || !strings.Contains(out, "ноутбук-анна") {
		t.Fatalf("сводка по кодам и оба ключа автора: %s", out)
	}
	if strings.Contains(out, "игорь") {
		t.Fatal("в журнал попали чужие запросы")
	}
}

func TestWritesGoToStateNotSeed(t *testing.T) {
	s, state := open(t)
	if out, bad := call(t, s, "add_note", `{"ticket_id":"T-1042","text":"нужен новый ключ на телефон"}`); bad {
		t.Fatal(out)
	}
	if out, bad := call(t, s, "set_status", `{"ticket_id":"T-1042","status":"escalated"}`); bad || !strings.Contains(out, "open → escalated") {
		t.Fatal(out)
	}
	if _, bad := call(t, s, "set_status", `{"ticket_id":"T-1042","status":"удалён"}`); !bad {
		t.Fatal("произвольный статус принят")
	}
	b, err := os.ReadFile(state)
	if err != nil || !strings.Contains(string(b), "нужен новый ключ") {
		t.Fatalf("заметка должна лечь в рабочую копию: %v", err)
	}
	// перечитанная рабочая копия — с изменениями
	s2, _ := Open("нет-такого-файла", state, nil)
	if tk, _, _ := s2.Ticket("T-1042"); tk.Status != "escalated" {
		t.Fatalf("статус после перечитывания: %q", tk.Status)
	}
}
