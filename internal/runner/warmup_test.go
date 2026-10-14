package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/scenario"
)

// Прогрев — по одному лишнему запросу на каждую новую модель, перед её
// первым вариантом; в результатах и проверках его нет.
func TestWarmup(t *testing.T) {
	var mu sync.Mutex
	var calls []string // модель + max_tokens каждого запроса по порядку
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model     string `json:"model"`
			MaxTokens *int   `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		tag := req.Model
		if req.MaxTokens != nil && *req.MaxTokens == 1 {
			tag += ":warm"
		}
		mu.Lock()
		calls = append(calls, tag)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	defer srv.Close()

	step := []scenario.Step{{User: "q"}}
	sc := &scenario.Scenario{Name: "w", Repeat: 2, Variants: []scenario.Variant{
		{ID: "a", Model: "m1", Steps: step},
		{ID: "b", Model: "m1", Steps: step},
		{ID: "c", Model: "m2", Steps: step},
	}}
	var events []string
	r := &Runner{Client: llm.New("t", srv.URL, "k"), Warmup: true}
	res, err := r.Run(context.Background(), sc, func(ev Event) {
		if ev.Kind == "warmup" {
			events = append(events, ev.Label)
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"m1:warm", "m1", "m1", "m1", "m1", "m2:warm", "m2", "m2"}
	if len(calls) != len(want) {
		t.Fatalf("запросы: %v, ждали %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("запросы: %v, ждали %v", calls, want)
		}
	}
	if len(res.Attempts) != 6 {
		t.Fatalf("прогонов %d, ждали 6 — прогрев не должен попадать в результаты", len(res.Attempts))
	}
	if len(events) != 2 || events[0] != "m1" || events[1] != "m2" {
		t.Fatalf("события прогрева: %v", events)
	}

	// Без флага — ни одного лишнего запроса.
	calls = nil
	r.Warmup = false
	if _, err := r.Run(context.Background(), sc, nil); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 6 {
		t.Fatalf("без прогрева запросов %d, ждали 6", len(calls))
	}
}
