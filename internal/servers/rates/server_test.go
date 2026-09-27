package rates

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/safronov-a1exander/advent/internal/mcp"
)

// Fake — курсы без сети: фиксированная таблица на любую дату.
type fake struct{ asked []time.Time }

func (f *fake) Daily(_ context.Context, date time.Time) (*Daily, error) {
	f.asked = append(f.asked, date)
	return &Daily{
		Date: time.Date(2026, 9, 26, 11, 30, 0, 0, time.UTC),
		Valute: map[string]Rate{
			"USD": {Code: "USD", Nominal: 1, Name: "Доллар США", Value: 84.0, Previous: 83.0},
			"TRY": {Code: "TRY", Nominal: 10, Name: "Турецких лир", Value: 17.0, Previous: 17.0},
		},
	}, nil
}

// connect — клиент и сервер в одном процессе, соединённые трубами
// ровно так, как их соединил бы подпроцесс: построчный JSON в обе стороны.
func connect(t *testing.T, s *mcp.Server) *mcp.Client {
	t.Helper()
	cr, sw := io.Pipe() // сервер пишет → клиент читает
	sr, cw := io.Pipe() // клиент пишет → сервер читает
	go func() {
		s.ServeStdio(context.Background(), sr, sw)
		sw.Close()
	}()
	cl, err := mcp.Connect(context.Background(), "rates", mcp.NewStdioPipe(cw, cr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

func call(t *testing.T, cl *mcp.Client, tool, args string) *mcp.CallResult {
	t.Helper()
	res, err := cl.CallTool(context.Background(), tool, json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestToolsAreRegisteredWithSchema(t *testing.T) {
	cl := connect(t, New(&fake{}))
	tools, err := cl.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "exchange_rate" || tools[1].Name != "convert" {
		t.Fatalf("инструменты: %+v", tools)
	}
	ps := tools[1].Params()
	if len(ps) != 4 || ps[0].Name != "amount" || !ps[0].Required || ps[2].Required {
		t.Fatalf("аргументы convert: %+v", ps)
	}
}

func TestConvert(t *testing.T) {
	cl := connect(t, New(&fake{}))
	res := call(t, cl, "convert", `{"amount":10,"from":"usd"}`)
	if res.IsError || !strings.Contains(res.Text(), "10 USD = 840 RUB") {
		t.Fatalf("результат: %+v", res)
	}
	// Кросс-курс через рубль и номинал: 10 лир стоят 17 рублей.
	res = call(t, cl, "convert", `{"amount":100,"from":"USD","to":"TRY"}`)
	var v struct{ Result float64 }
	json.Unmarshal(res.StructuredContent, &v)
	if want := 100 * 84.0 / 1.7; v.Result < want-0.01 || v.Result > want+0.01 {
		t.Fatalf("100 USD в TRY: %v, ждали %.2f (%s)", v.Result, want, res.Text())
	}
}

func TestToolErrorsAreResultsNotFailures(t *testing.T) {
	cl := connect(t, New(&fake{}))
	for args, want := range map[string]string{
		`{"amount":1,"from":"XXX"}`:                     "XXX",
		`{"from":"USD"}`:                                "amount",
		`{"amount":1,"from":"USD","date":"вчера"}`:      "ГГГГ-ММ-ДД",
		`{"amount":1,"from":"USD","date":"2999-01-01"}`: "будущем",
	} {
		res := call(t, cl, "convert", args)
		if !res.IsError || !strings.Contains(res.Text(), want) {
			t.Errorf("%s: ждали ошибку про %q, получили %+v", args, want, res)
		}
	}
	if _, err := cl.CallTool(context.Background(), "нет_такого", nil); err == nil {
		t.Error("неизвестный инструмент — ошибка протокола, а не тишина")
	}
}

func TestExchangeRateShowsChange(t *testing.T) {
	cl := connect(t, New(&fake{}))
	res := call(t, cl, "exchange_rate", `{"currency":"USD"}`)
	if !strings.Contains(res.Text(), "1 USD = 84 RUB") || !strings.Contains(res.Text(), "+1.20%") {
		t.Fatalf("результат: %s", res.Text())
	}
}

// ЦБ не публикует курс в выходные: на субботу берётся пятничный.
func TestCBRWalksBackOverWeekend(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/archive/2026/09/25/daily_json.js" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"Date":"2026-09-25T11:30:00+03:00","Valute":{"USD":{"CharCode":"USD","Nominal":1,"Value":84.1,"Previous":83.9}}}`)
	}))
	defer srv.Close()
	c := NewCBR()
	c.Base = srv.URL
	d, err := c.Daily(context.Background(), time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := d.Lookup("USD"); r.Value != 84.1 {
		t.Fatalf("курс: %+v", r)
	}
	if len(paths) != 3 {
		t.Fatalf("запросы: %v — ждали воскресенье, субботу, пятницу", paths)
	}
	// Второй раз — из кэша, без сети.
	c.Daily(context.Background(), time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC))
	if len(paths) != 3 {
		t.Fatalf("прошедший день спросили повторно: %v", paths)
	}
}
