package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/safronov-a1exander/advent/internal/llm"
)

var sampleDiff = `diff --git a/internal/x/x.go b/internal/x/x.go
index 1111111..2222222 100644
--- a/internal/x/x.go
+++ b/internal/x/x.go
@@ -10,3 +10,5 @@ func A() {
 	a := 1
+	b := 2
+	_ = b
 	return
 }
diff --git a/cmd/advent/day40.go b/cmd/advent/day40.go
new file mode 100644
--- /dev/null
+++ b/cmd/advent/day40.go
@@ -0,0 +1,3 @@
+package main
+
+const key = "` + fakeKey + `"
`

// fakeKey собран из частей, чтобы сканеры секретов (и наш же) не видели
// ключа в исходнике теста.
var fakeKey = "s" + "k-" + strings.Repeat("abcdefgh", 4)

func TestParseDiff(t *testing.T) {
	fs := ParseDiff(sampleDiff)
	if len(fs) != 2 {
		t.Fatalf("файлов %d", len(fs))
	}
	x := fs[0]
	if x.Path != "internal/x/x.go" || x.Added != 2 || x.Status != "modified" {
		t.Fatalf("%+v", x)
	}
	if !x.Touches(11) || !x.Touches(14) || x.Touches(20) {
		t.Fatalf("строки хунка 10–14: %+v", x.Lines)
	}
	if fs[1].Status != "added" || !strings.Contains(fs[1].AddedText, "sk-") {
		t.Fatalf("%+v", fs[1])
	}
}

func TestChecksFindSecretMissingNoteAndTests(t *testing.T) {
	fs := ParseDiff(sampleDiff)
	read := func(_ context.Context, p string) ([]byte, error) {
		if p == "internal/x/x.go" {
			return []byte("package x\n\nfunc A()  {}\n"), nil // лишний пробел — не gofmt
		}
		return []byte("package main\n"), nil
	}
	checks := DefaultChecks(read)(context.Background(), Input{Files: fs})
	got := map[string]Check{}
	for _, c := range checks {
		got[c.Name] = c
	}
	if got["gofmt"].OK || !strings.Contains(strings.Join(got["gofmt"].Details, ";"), "x.go") {
		t.Fatalf("gofmt: %+v", got["gofmt"])
	}
	if got["секреты"].OK {
		t.Fatal("ключ sk-… в добавленной строке не замечен")
	}
	if got["заметка дня"].OK {
		t.Fatal("day40.go без docs/days/day40.md не замечен")
	}
	if got["тесты"].OK {
		t.Fatal("internal/x изменён без тестов — должно быть видно")
	}
}

// fakeChat отвечает по очереди заданными ответами или ошибками.
type fakeChat struct {
	out   []string
	errs  []error
	calls int
	last  llm.Request
}

func (f *fakeChat) Chat(_ context.Context, r llm.Request) (*llm.Response, error) {
	i := f.calls
	f.calls++
	f.last = r
	if i < len(f.errs) && f.errs[i] != nil {
		return nil, f.errs[i]
	}
	if i < len(f.out) {
		return &llm.Response{Content: f.out[i], CostUSD: 0.001}, nil
	}
	return nil, errors.New("ответов больше нет")
}

const goodAnswer = `{"summary":"добавляет b","verdict":"comment",
 "bugs":[{"file":"internal/x/x.go","line":11,"severity":"low","problem":"b не нужна"},
         {"file":"internal/y/y.go","line":3,"severity":"high","problem":"выдумано"}],
 "architecture":[],"recommendations":[{"file":"internal/x/x.go","line":40,"problem":"тест"}]}`

func TestRetryRepairAndValidation(t *testing.T) {
	primary := &fakeChat{errs: []error{errors.New("503")}, out: []string{"", "это не json", goodAnswer}}
	p := &Pipeline{Primary: Model{Client: primary, ID: "flash"}, Tries: 3}
	res, err := p.Run(context.Background(), Input{Diff: sampleDiff, SHA: "abcdef0"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed || len(res.Attempts) != 3 || res.Fallback {
		t.Fatalf("ошибка API → повтор, плохой JSON → повтор с просьбой: %+v", res.Attempts)
	}
	last := primary.last.Messages[len(primary.last.Messages)-1].Content
	if !strings.Contains(last, "Верни только JSON") {
		t.Fatalf("повтор после плохого JSON должен просить исправить формат: %q", last)
	}
	if len(res.Answer.Bugs) != 1 || len(res.Dropped) != 1 || res.Dropped[0].File != "internal/y/y.go" {
		t.Fatalf("замечание про файл не из diff отбрасывается: %+v / %+v", res.Answer.Bugs, res.Dropped)
	}
	if res.Answer.Recommendations[0].Note != "строка вне изменений" {
		t.Fatalf("строка 40 вне хунка: %+v", res.Answer.Recommendations[0])
	}
	md := res.Markdown()
	if !strings.Contains(md, Marker("abcdef0")) || !strings.Contains(md, "Отброшено сверкой: 1") {
		t.Fatalf("в отчёте нет метки версии или отброшенного:\n%s", md)
	}
}

func TestBadRequestGoesStraightToFallback(t *testing.T) {
	bad := &llm.APIError{Status: 400, Body: `{"error":{"message":"no such model (request_id: x)"}}`}
	primary := &fakeChat{errs: []error{bad, bad}}
	backup := &fakeChat{out: []string{goodAnswer}}
	p := &Pipeline{Primary: Model{Client: primary, ID: "broken"}, Fallback: Model{Client: backup, ID: "pro"}, Tries: 2}
	res, _ := p.Run(context.Background(), Input{Diff: sampleDiff})
	if primary.calls != 1 || !res.Fallback {
		t.Fatalf("400 не повторяется: вызовов основной %d", primary.calls)
	}
	if res.Attempts[0].Error != "400 no such model" {
		t.Fatalf("в попытке — текст ошибки, а не JSON: %q", res.Attempts[0].Error)
	}
	busy := &llm.APIError{Status: 503, Body: "busy"}
	primary = &fakeChat{errs: []error{busy}, out: []string{"", goodAnswer}}
	p = &Pipeline{Primary: Model{Client: primary, ID: "flash"}, Tries: 2}
	if res, _ := p.Run(context.Background(), Input{Diff: sampleDiff}); res.Failed || primary.calls != 2 {
		t.Fatalf("503 — временная, повтор нужен: вызовов %d", primary.calls)
	}
}

func TestFallbackModelThenHumanWhenAllFail(t *testing.T) {
	primary := &fakeChat{errs: []error{errors.New("400"), errors.New("400")}}
	backup := &fakeChat{out: []string{goodAnswer}}
	p := &Pipeline{Primary: Model{Client: primary, ID: "flash"}, Fallback: Model{Client: backup, ID: "pro"}, Tries: 2}
	res, _ := p.Run(context.Background(), Input{Diff: sampleDiff})
	if res.Failed || !res.Fallback || res.Model != "pro" {
		t.Fatalf("после двух отказов основной — ответ запасной: %+v", res)
	}

	dead := &fakeChat{errs: []error{errors.New("x"), errors.New("x")}}
	p = &Pipeline{Primary: Model{Client: dead, ID: "flash"}, Tries: 2,
		Checks: DefaultChecks(nil)}
	res, _ = p.Run(context.Background(), Input{Diff: sampleDiff})
	md := res.Markdown()
	if !res.Failed || !strings.Contains(md, "нужен человек") || !strings.Contains(md, "Проверки кодом") {
		t.Fatalf("без модели ревью всё равно выходит — с проверками и просьбой к человеку:\n%s", md)
	}
}

func TestSummarizeAlertsOnDoubleCost(t *testing.T) {
	var ms []Metric
	for i := 0; i < 5; i++ {
		ms = append(ms, Metric{OK: true, CostUSD: 0.002, TookMS: int64(1000 * (i + 1))})
	}
	ms = append(ms, Metric{OK: false, CostUSD: 0.005, TookMS: 9000})
	s := Summarize(ms)
	if s.OK != 5 || s.LastX < 2 || !strings.Contains(s.String(), "АЛЕРТ") {
		t.Fatalf("%+v\n%s", s, s)
	}
	if s.P50 != 3e9 || s.P95 != 9e9 {
		t.Fatalf("p50 %s p95 %s", s.P50, s.P95)
	}
}

func TestSecretsCheckSkipsOnlyEnvReferences(t *testing.T) {
	fs := []File{{Path: "config.yaml", AddedText: "    api_key_env: DEEPSEEK_API_KEY\n" +
		"      CONTEXT7_API_KEY: ${CONTEXT7_API_KEY}\n" +
		"    token: abcdefghijklmnopqrstuv  # потом перенести в _env\n"}}
	c := secretsCheck(fs)
	if c.OK || len(c.Details) != 1 || !strings.Contains(c.Details[0], "строке 3") {
		t.Fatalf("ссылки на окружение пропускаются, ключ рядом со словом env — нет: %+v", c)
	}
}

func TestRunRejectsUnparsableDiff(t *testing.T) {
	p := &Pipeline{Primary: Model{Client: &fakeChat{}, ID: "m"}}
	if _, err := p.Run(context.Background(), Input{Diff: "просто текст"}); err == nil {
		t.Fatal("diff без файлов — ошибка, а не пустое «чистое» ревью")
	}
}
