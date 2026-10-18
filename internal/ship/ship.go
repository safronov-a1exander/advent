// Package ship — сдача дня челленджа (день 35): реальная рутина, которую
// этот репозиторий проходит каждый день.
//
// Шаг готов, когда: ветка day-NN ответвлена от day-(NN-1), код
// отформатирован и тесты проходят, есть заметка дня, строка в таблице
// README, демо-сценарий и видео, ветка запушена, открыт PR «Day NN — тема»,
// а в таблицу сдачи ушёл комментарий со ссылками. До сих пор это делалось
// руками и каждый раз немного по-разному: забывалась строка README
// (таблица отстала на девять дней), видео писалось битым (ffmpeg без
// libx264), PR описывался с нуля.
//
// Пайплайн устроен по лекции недели:
//   - проверки кодом — цепочкой, дёшево и детерминированно;
//   - субагенты — веером, параллельно, у каждого свой контекст и своя
//     задача: ревьюер, автор описания PR, редактор README;
//   - роутер — редактор README запускается, только если строки нет;
//   - человек в цепи — push и PR только после подтверждения;
//   - метрики — время и цена каждого субагента.
package ship

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Status — итог одной проверки.
type Status int

const (
	OK Status = iota
	Warn
	Fail
)

func (s Status) Mark() string {
	switch s {
	case OK:
		return "✓"
	case Warn:
		return "⚠"
	}
	return "✗"
}

// Check — одна проверка готовности.
type Check struct {
	Name   string
	Status Status
	Detail string
	// Fixer — имя субагента, который может это исправить (роутер).
	Fixer string
}

// Day — что сдаём.
type Day struct {
	N      int
	Branch string // day-35
	Prev   string // day-34
	Title  string // из заголовка заметки: «ассистент для работы с файлами проекта»
	Note   string // docs/days/day35.md
	Video  string // recordings/day35-….mp4, пусто — нет
	Remote string // https://github.com/owner/repo
}

// Result — что вернул субагент.
type Result struct {
	Name   string
	Text   string // для отчёта
	Took   time.Duration
	Cost   float64
	Err    error
	Detail map[string]string // структурированное: title, body, verdict…
}

// Subagent — исполнитель одной подзадачи со своим контекстом. Жизненный
// цикл из лекции: создан → исполнен → вернул результат → умер; живёт
// одну функцию Run.
type Subagent struct {
	Name string
	// When — роутер: запускать ли этого субагента по итогам проверок.
	// nil — всегда.
	When func(checks []Check) bool
	Run  func(ctx context.Context) Result
}

// FanOut запускает выбранных роутером субагентов параллельно и ждёт всех.
// Упавший субагент не роняет остальных: его ошибка — в его результате.
func FanOut(ctx context.Context, subs []Subagent, checks []Check, log func(string, ...any)) []Result {
	var picked []Subagent
	for _, s := range subs {
		if s.When == nil || s.When(checks) {
			picked = append(picked, s)
		} else {
			log("роутер: %s не нужен — пропускаю", s.Name)
		}
	}
	out := make([]Result, len(picked))
	var wg sync.WaitGroup
	for i, s := range picked {
		wg.Add(1)
		go func(i int, s Subagent) {
			defer wg.Done()
			log("▶ субагент «%s»: старт", s.Name)
			started := time.Now()
			r := s.Run(ctx)
			r.Name = s.Name
			if r.Took == 0 {
				r.Took = time.Since(started)
			}
			if r.Err != nil {
				log("■ субагент «%s»: ошибка за %s — %v", s.Name, r.Took.Round(100*time.Millisecond), r.Err)
			} else {
				log("■ субагент «%s»: готово за %s · $%.4f", s.Name, r.Took.Round(100*time.Millisecond), r.Cost)
			}
			out[i] = r
		}(i, s)
	}
	wg.Wait()
	return out
}

// Blocking — проверки, без которых сдавать нельзя.
func Blocking(checks []Check) []Check {
	var out []Check
	for _, c := range checks {
		if c.Status == Fail && c.Fixer == "" {
			out = append(out, c)
		}
	}
	return out
}

// NeedsFix — есть ли проверка, которую чинит этот субагент.
func NeedsFix(name string) func([]Check) bool {
	return func(cs []Check) bool {
		for _, c := range cs {
			if c.Status == Fail && c.Fixer == name {
				return true
			}
		}
		return false
	}
}

// Report — markdown для reports/ship/day-NN.md.
func Report(d Day, checks []Check, results []Result, actions []string, published bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Сдача дня %d — %s\n\nВетка `%s` (от `%s`) · %s\n\n", d.N, d.Title, d.Branch, d.Prev, time.Now().Format("2006-01-02 15:04"))
	b.WriteString("## Проверки\n\n")
	for _, c := range checks {
		fmt.Fprintf(&b, "- %s **%s** — %s\n", c.Status.Mark(), c.Name, c.Detail)
	}
	var cost float64
	for _, r := range results {
		cost += r.Cost
		fmt.Fprintf(&b, "\n## Субагент «%s» · %s · $%.4f\n\n", r.Name, r.Took.Round(100*time.Millisecond), r.Cost)
		if r.Err != nil {
			fmt.Fprintf(&b, "⚠ не справился: %v\n", r.Err)
			continue
		}
		b.WriteString(strings.TrimSpace(r.Text) + "\n")
	}
	b.WriteString("\n## Публикация\n\n")
	for _, a := range actions {
		mark := "–"
		if published {
			mark = "✓"
		}
		fmt.Fprintf(&b, "- %s `%s`\n", mark, a)
	}
	if !published {
		b.WriteString("\nНе выполнено: нужно подтверждение человека (`advent ship -yes`).\n")
	}
	fmt.Fprintf(&b, "\n---\nсубагентов %d · $%.4f\n", len(results), cost)
	return b.String()
}
