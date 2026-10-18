package ship

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFanOutRoutesAndRunsInParallel(t *testing.T) {
	checks := []Check{
		{Name: "README", Status: Fail, Fixer: "редактор"},
		{Name: "тесты", Status: OK},
	}
	var running, peak atomic.Int32
	slow := func(ctx context.Context) Result {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		running.Add(-1)
		return Result{Cost: 0.01}
	}
	subs := []Subagent{
		{Name: "ревьюер", Run: slow},
		{Name: "автор", Run: slow},
		{Name: "редактор", When: NeedsFix("редактор"), Run: slow},
		{Name: "лишний", When: NeedsFix("никто"), Run: func(context.Context) Result {
			t.Error("роутер должен был его пропустить")
			return Result{}
		}},
	}
	var logs []string
	res := FanOut(context.Background(), subs, checks, func(f string, a ...any) { logs = append(logs, f) })
	if len(res) != 3 || res[2].Name != "редактор" {
		t.Fatalf("результаты: %+v", res)
	}
	if peak.Load() < 2 {
		t.Fatal("субагенты должны работать одновременно")
	}
	if !strings.Contains(strings.Join(logs, "\n"), "роутер") {
		t.Fatal("пропуск субагента роутером должен быть виден")
	}
}

func TestBlockingIgnoresFixable(t *testing.T) {
	cs := []Check{
		{Name: "README", Status: Fail, Fixer: "редактор"},
		{Name: "видео", Status: Fail},
		{Name: "push", Status: Warn},
	}
	b := Blocking(cs)
	if len(b) != 1 || b[0].Name != "видео" {
		t.Fatalf("блокирует только то, что не чинит субагент: %+v", b)
	}
}

func TestReportSaysWhatWasNotPublished(t *testing.T) {
	md := Report(Day{N: 35, Branch: "day-35", Prev: "day-34", Title: "реальная задача"},
		[]Check{{Name: "go test", Status: OK, Detail: "ok"}},
		[]Result{{Name: "автор PR", Text: "**Day 35 — сдача дня**", Cost: 0.002}},
		[]string{"git push -u origin day-35"}, false)
	for _, want := range []string{"# Сдача дня 35", "✓ **go test**", "Субагент «автор PR»", "- – `git push -u origin day-35`", "нужно подтверждение человека"} {
		if !strings.Contains(md, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, md)
		}
	}
}
