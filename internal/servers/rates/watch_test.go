package rates

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/safronov-a1exander/advent/internal/mcp"
)

// steps — рынок, который отдаёт заранее заданные цены по очереди.
type steps struct{ v []float64 }

func (s *steps) Quote(context.Context, string) (float64, error) {
	x := s.v[0]
	if len(s.v) > 1 {
		s.v = s.v[1:]
	}
	return x, nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock               { return &clock{t: time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)} }

func TestTrackerSamplesOnScheduleAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.json")
	c := newClock()
	m := &steps{v: []float64{80, 81, 82, 79, 85}}
	tr, err := NewTracker(path, m)
	if err != nil {
		t.Fatal(err)
	}
	tr.now = c.now
	ctx := context.Background()
	if _, _, err := tr.Add(ctx, "usd", time.Minute); err != nil { // Add спрашивает рынок: 80
		t.Fatal(err)
	}
	if n := tr.Tick(ctx); n != 1 { // первый замер сразу: 81
		t.Fatalf("первый замер: %d", n)
	}
	c.add(30 * time.Second)
	if n := tr.Tick(ctx); n != 0 {
		t.Fatal("срок не подошёл, а замер сделан")
	}
	c.add(30 * time.Second)
	tr.Tick(ctx) // 82

	// Перезапуск: новый трекер из того же файла продолжает с тем же заданием.
	tr2, err := NewTracker(path, m)
	if err != nil {
		t.Fatal(err)
	}
	tr2.now = c.now
	if l := tr2.List(); len(l) != 1 || len(l[0].Samples) != 2 {
		t.Fatalf("после перезапуска: %+v", l)
	}
	// Сервер лежал десять минут: один замер, а не десять задним числом.
	c.add(10 * time.Minute)
	if n := tr2.Tick(ctx); n != 1 { // 79
		t.Fatalf("после простоя: %d замеров", n)
	}
	d, err := tr2.Digest("USD", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if d.Count != 3 || d.First != 81 || d.Last != 79 || d.Min != 79 || d.Max != 82 || d.Gaps != 1 {
		t.Fatalf("сводка: %+v", d)
	}
	if d.ChangePct > -2.4 || d.ChangePct < -2.5 {
		t.Fatalf("изменение: %.3f", d.ChangePct)
	}
	// Окно отрезает старое.
	if d, _ := tr2.Digest("USD", 5*time.Minute); d.Count != 1 {
		t.Fatalf("окно 5 минут: %+v", d)
	}
}

func TestTrackerRejectsBadInput(t *testing.T) {
	tr, _ := NewTracker(filepath.Join(t.TempDir(), "w.json"), &Walk{})
	if _, _, err := tr.Add(context.Background(), "USD", time.Second); err == nil {
		t.Fatal("раз в секунду — слишком часто")
	}
	if _, _, err := tr.Add(context.Background(), "XXX", time.Minute); err == nil {
		t.Fatal("неизвестная валюта")
	}
	if _, err := tr.Digest("EUR", time.Hour); err == nil || !strings.Contains(err.Error(), "watch_rate") {
		t.Fatalf("сводка без слежения: %v", err)
	}
}

// Сервер по HTTP: клиент приходит, заводит слежение, уходит; сервер
// перезапускается и забывает сессию; хаб сам делает новое рукопожатие.
func TestWatchOverHTTPWithRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.json")
	start := func() *httptest.Server {
		tr, err := NewTracker(path, &Walk{})
		if err != nil {
			t.Fatal(err)
		}
		return httptest.NewServer(WithTracker(New(&fake{}), tr).HTTPHandler())
	}
	srv := start()
	url := srv.URL
	hub := mcp.NewHub([]mcp.Spec{{Name: "rates", URL: url}})
	defer hub.Close()
	ctx := context.Background()
	if _, err := hub.Functions(ctx, []string{"rates"}); err != nil {
		t.Fatal(err)
	}
	o := hub.Call(ctx, "rates__watch_rate", `{"currency":"BTC","every":"1m"}`, []string{"rates"})
	if o.IsError || !strings.Contains(o.Text, "BTC") {
		t.Fatalf("watch_rate: %+v", o)
	}

	// Перезапуск на том же адресе: новый процесс, сессий он не знает.
	srv.Close()
	srv2 := httptest.NewUnstartedServer(nil)
	tr, _ := NewTracker(path, &Walk{})
	srv2.Config.Handler = WithTracker(New(&fake{}), tr).HTTPHandler()
	srv2.Listener.Close()
	l, err := netListen(url)
	if err != nil {
		t.Skipf("порт занят: %v", err)
	}
	srv2.Listener = l
	srv2.Start()
	defer srv2.Close()

	o = hub.Call(ctx, "rates__list_watches", `{}`, []string{"rates"})
	if o.IsError || !strings.Contains(o.Text, "BTC: каждые 1m0s") {
		t.Fatalf("после перезапуска сервера: %+v", o)
	}
}

func TestDigestToolIsStructured(t *testing.T) {
	c := newClock()
	tr, _ := NewTracker(filepath.Join(t.TempDir(), "w.json"), &steps{v: []float64{10, 10, 11}})
	tr.now = c.now
	ctx := context.Background()
	tr.Add(ctx, "EUR", time.Minute)
	tr.Tick(ctx)
	c.add(time.Minute)
	tr.Tick(ctx)
	cl := connect(t, WithTracker(New(&fake{}), tr))
	res := call(t, cl, "rate_digest", `{"currency":"eur","window":"1h"}`)
	var d Digest
	if err := json.Unmarshal(res.StructuredContent, &d); err != nil || d.Count != 2 || d.Last != 11 {
		t.Fatalf("сводка: %+v %v (%s)", d, err, res.Text())
	}
	if !strings.Contains(res.Text(), "+10.000%") {
		t.Fatalf("текст: %s", res.Text())
	}
}
