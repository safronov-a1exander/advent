package budget

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/safronov-a1exander/advent/internal/mcp"
)

func statement(t *testing.T) []Tx {
	t.Helper()
	txs, err := Load(filepath.Join("..", "..", "..", "data", "statement.txt"), 2026)
	if err != nil {
		t.Fatal(err)
	}
	return txs
}

func connect(t *testing.T, s *mcp.Server) *mcp.Client {
	t.Helper()
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	go func() { s.ServeStdio(context.Background(), sr, sw); sw.Close() }()
	cl, err := mcp.Connect(context.Background(), "budget", mcp.NewStdioPipe(cw, cr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

func call(t *testing.T, cl *mcp.Client, tool string, args any) *mcp.CallResult {
	t.Helper()
	b, _ := json.Marshal(args)
	res, err := cl.CallTool(context.Background(), tool, b)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestLoadStatement(t *testing.T) {
	txs := statement(t)
	if len(txs) != 15 {
		t.Fatalf("операций: %d", len(txs))
	}
	if txs[0].Date != "2026-08-31" || txs[5].Amount != -1800 || txs[14].Currency != "USD" {
		t.Fatalf("разбор: %+v %+v %+v", txs[0], txs[5], txs[14])
	}
	if txs[8].Category != "кафе" { // YANDEX EDA — кафе, а не такси
		t.Fatalf("категория YANDEX EDA: %q", txs[8].Category)
	}
}

// Цепочка по ссылке: search → dataset_id → summarize → save.
func TestPipelineByRef(t *testing.T) {
	dir := t.TempDir()
	cl := connect(t, New(statement(t), Options{ReportsDir: dir,
		Now: func() time.Time { return time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC) }}))
	res := call(t, cl, "search_transactions", map[string]string{"category": "кафе"})
	var ref struct {
		DatasetID string `json:"dataset_id"`
		Count     int
	}
	json.Unmarshal(res.StructuredContent, &ref)
	if ref.Count != 3 || ref.DatasetID != "ds-1" {
		t.Fatalf("поиск: %+v %s", ref, res.Text())
	}
	res = call(t, cl, "summarize_transactions", map[string]string{"dataset_id": ref.DatasetID})
	var s Summary
	json.Unmarshal(res.StructuredContent, &s)
	// 390 + 1480 − 480
	if s.Spent != 1870 || s.Refunds != 480 || s.Net != 1390 || !strings.Contains(s.Audit, "по ссылке") {
		t.Fatalf("сводка: %+v", s)
	}
	res = call(t, cl, "save_report", map[string]string{"title": "Кафе за сентябрь", "content": "Итого на кафе: 1 390 ₽."})
	if res.IsError || !strings.Contains(res.Text(), "итог 1390 RUB из sum-1 в отчёте есть") {
		t.Fatalf("сохранение: %s", res.Text())
	}
	b, err := os.ReadFile(filepath.Join(dir, "кафе-за-сентябрь-20260927-120000.md"))
	if err != nil || !strings.Contains(string(b), "1 390") {
		t.Fatalf("файл: %v %s", err, b)
	}
	if res := call(t, cl, "summarize_transactions", map[string]string{"dataset_id": "ds-9"}); !res.IsError {
		t.Fatal("несуществующая выборка — ошибка для модели")
	}
}

// По значению сервер сверяет переписанное моделью с тем, что отдал.
func TestPipelineByValueAudits(t *testing.T) {
	cl := connect(t, New(statement(t), Options{Handoff: ByValue, ReportsDir: t.TempDir()}))
	res := call(t, cl, "search_transactions", map[string]string{"query": "yandex"})
	text := res.Text()
	arr := text[strings.Index(text, "["):]
	var txs []Tx
	if err := json.Unmarshal([]byte(arr), &txs); err != nil || len(txs) != 3 {
		t.Fatalf("выдача по значению: %v %s", err, text)
	}
	res = call(t, cl, "summarize_transactions", map[string]any{"transactions": txs})
	if !strings.Contains(res.Text(), "совпали все 3") {
		t.Fatalf("честная передача: %s", res.Text())
	}
	// Модель потеряла возврат и «округлила» такси.
	bad := []Tx{txs[0], txs[1]}
	bad[0].Amount = 600
	res = call(t, cl, "summarize_transactions", map[string]any{"transactions": bad})
	if !strings.Contains(res.Text(), "РАСХОЖДЕНИЕ: потеряно 1, изменено 1") {
		t.Fatalf("испорченная передача: %s", res.Text())
	}
}

func TestSaveCannotEscapeReportsDir(t *testing.T) {
	dir := t.TempDir()
	cl := connect(t, New(statement(t), Options{ReportsDir: dir}))
	res := call(t, cl, "save_report", map[string]string{"title": "../../.env", "content": "x"})
	var out struct{ Path string }
	json.Unmarshal(res.StructuredContent, &out)
	if !strings.HasPrefix(filepath.Clean(out.Path), filepath.Clean(dir)) || strings.Contains(out.Path, "..") {
		t.Fatalf("файл ушёл из каталога: %s", out.Path)
	}
	if !strings.Contains(res.Text(), "без summarize") {
		t.Fatalf("отчёт без сводки должен быть отмечен: %s", res.Text())
	}
}

func TestSummaryKeepsForeignApart(t *testing.T) {
	s := summarize(statement(t))
	if s.Foreign["USD"] != 10 || s.ByCategory["подписки"] != 0 {
		t.Fatalf("валюта: %+v", s)
	}
}
