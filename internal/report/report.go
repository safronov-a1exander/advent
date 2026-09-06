// Package report собирает из результатов прогона то, что можно показать
// в видео и приложить к заданию: таблицу в консоль и markdown-отчёт.
package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/metrics"
	"github.com/safronov-a1exander/advent/internal/runner"
)

// Row — строка сводной таблицы.
type Row struct {
	Variant  string
	Label    string
	Model    string
	Params   string
	Attempts int
	AvgMS    int64
	InTok    int
	OutTok   int
	ReasTok  int
	Cost     float64
	Finish   string
	Passed   int    // прогонов, где все проверки прошли
	Checks   string // «прошло/всего»
	OK       bool
	// Similarity — среднее попарное сходство ответов между повторами.
	// 1.00 — модель повторяется дословно, 0.00 — каждый раз новый текст.
	// Считается только при repeat > 1.
	Similarity float64
	Identical  bool
	Empty      bool // ни одного непустого ответа
	// TokPerSec — скорость генерации: completion-токены, делённые на
	// время ответа. Главная метрика «быстрее/медленнее» в дне 5,
	// потому что абсолютная задержка зависит ещё и от длины ответа.
	TokPerSec float64
}

func Rows(res *runner.Result) []Row {
	type agg struct {
		row   Row
		msSum int64
		fails int
		texts []string
	}
	order := []string{}
	m := map[string]*agg{}

	for _, a := range res.Attempts {
		g, ok := m[a.VariantID]
		if !ok {
			g = &agg{row: Row{
				Variant: a.VariantID, Label: a.Label, Model: a.Model,
				Params: a.Params.Describe(), OK: true,
			}}
			m[a.VariantID] = g
			order = append(order, a.VariantID)
		}
		g.row.Attempts++
		g.msSum += a.Latency.Milliseconds()
		g.row.InTok += a.Usage.PromptTokens
		g.row.OutTok += a.Usage.CompletionTokens
		g.row.ReasTok += a.Usage.ReasoningTokens
		g.row.Cost += a.CostUSD
		g.row.Finish = a.Finish
		g.texts = append(g.texts, a.Final)
		if a.Failed() {
			g.fails++
		} else {
			g.row.Passed++
		}
	}

	out := make([]Row, 0, len(order))
	for _, id := range order {
		g := m[id]
		if g.row.Attempts > 0 {
			g.row.AvgMS = g.msSum / int64(g.row.Attempts)
		}
		g.row.OK = g.fails == 0
		// доля успешных прогонов: при repeat > 1 это и есть «стабильность
		// способа», главная метрика дня 3
		g.row.Checks = fmt.Sprintf("%d/%d", g.row.Passed, g.row.Attempts)
		g.row.Similarity = metrics.MeanSimilarity(g.texts)
		g.row.Identical = metrics.AllIdentical(g.texts)
		if g.msSum > 0 {
			g.row.TokPerSec = float64(g.row.OutTok) / (float64(g.msSum) / 1000)
		}
		g.row.Empty = true
		for _, t := range g.texts {
			if strings.TrimSpace(t) != "" {
				g.row.Empty = false
				break
			}
		}
		out = append(out, g.row)
	}
	return out
}

// Table рисует сводку моноширинной таблицей — читается прямо в видео.
func Table(res *runner.Result) string {
	rows := Rows(res)
	head := []string{"ВАРИАНТ", "МОДЕЛЬ", "ПАРАМЕТРЫ", "СРЕД.МС", "ТОК/С", "IN", "OUT", "REAS", "$", "FINISH", "УСПЕХ"}
	multi := res.Scenario.Repeat > 1
	if multi {
		head = append(head, "СХОДСТВО")
	}
	data := [][]string{}
	for _, r := range rows {
		row := []string{
			r.Label, short(r.Model, 24), short(r.Params, 34),
			fmt.Sprint(r.AvgMS), tokText(r),
			fmt.Sprint(r.InTok), fmt.Sprint(r.OutTok),
			fmt.Sprint(r.ReasTok), fmt.Sprintf("%.6f", r.Cost), r.Finish, r.Checks,
		}
		if multi {
			row = append(row, simText(r))
		}
		data = append(data, row)
	}
	return grid(head, data)
}

func grid(head []string, rows [][]string) string {
	w := make([]int, len(head))
	for i, h := range head {
		w[i] = runeLen(h)
	}
	for _, r := range rows {
		for i := range r {
			if i < len(w) && runeLen(r[i]) > w[i] {
				w[i] = runeLen(r[i])
			}
		}
	}
	var b strings.Builder
	writeRow := func(cells []string) {
		for i, c := range cells {
			b.WriteString(c)
			if i < len(cells)-1 {
				b.WriteString(strings.Repeat(" ", w[i]-runeLen(c)+2))
			}
		}
		b.WriteByte('\n')
	}
	writeRow(head)
	sep := make([]string, len(head))
	for i := range sep {
		sep[i] = strings.Repeat("─", w[i])
	}
	writeRow(sep)
	for _, r := range rows {
		writeRow(r)
	}
	return b.String()
}

// Markdown — полный отчёт: сводка, ответы, проверки. Кладётся в reports/.
func Markdown(res *runner.Result) string {
	s := res.Scenario
	var b strings.Builder

	fmt.Fprintf(&b, "# %s\n\n", orDef(s.Name, "прогон"))
	if s.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", s.Description)
	}
	fmt.Fprintf(&b, "- сценарий: `%s`\n", s.Path())
	fmt.Fprintf(&b, "- провайдер: `%s`\n", res.Provider)
	fmt.Fprintf(&b, "- журнал: `runs/%s.jsonl`\n", res.RunID)
	fmt.Fprintf(&b, "- повторов на вариант: %d\n", s.Repeat)
	fmt.Fprintf(&b, "- прогон: %s, длительность %s\n\n",
		res.Started.Format("2006-01-02 15:04:05"),
		res.Finished.Sub(res.Started).Round(time.Millisecond))

	b.WriteString("## Сводка\n\n")
	multi := s.Repeat > 1
	b.WriteString("| вариант | модель | параметры | сред. мс | ток/с | in | out | reasoning | $ | finish | успех |")
	if multi {
		b.WriteString(" сходство |")
	}
	b.WriteString("\n|---|---|---|---:|---:|---:|---:|---:|---:|---|---|")
	if multi {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, r := range Rows(res) {
		fmt.Fprintf(&b, "| %s | `%s` | `%s` | %d | %s | %d | %d | %d | %.6f | %s | %s |",
			r.Label, r.Model, r.Params, r.AvgMS, tokText(r),
			r.InTok, r.OutTok, r.ReasTok, r.Cost, r.Finish, r.Checks)
		if multi {
			fmt.Fprintf(&b, " %s |", simText(r))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	var total float64
	for _, a := range res.Attempts {
		total += a.CostUSD
	}
	fmt.Fprintf(&b, "Итого потрачено за прогон: **$%.6f** (по прайсу из `config.yaml`).\n\n", total)

	b.WriteString(modelsSection(res))

	b.WriteString("## Ответы\n")
	for _, a := range res.Attempts {
		fmt.Fprintf(&b, "\n### %s", a.Label)
		if s.Repeat > 1 {
			fmt.Fprintf(&b, " · прогон %d", a.N)
		}
		b.WriteString("\n\n")
		if note := noteOf(res, a.VariantID); note != "" {
			fmt.Fprintf(&b, "> %s\n\n", note)
		}
		fmt.Fprintf(&b, "`%s` · `%s` · %s · in %d / out %d / reasoning %d · $%.6f · finish `%s`\n\n",
			a.Model, a.Params.Describe(), a.Latency.Round(time.Millisecond),
			a.Usage.PromptTokens, a.Usage.CompletionTokens, a.Usage.ReasoningTokens,
			a.CostUSD, a.Finish)

		if a.Err != nil {
			fmt.Fprintf(&b, "**Ошибка:** `%s`\n\n", a.Err)
		}
		if len(a.Steps) > 1 {
			for i, st := range a.Steps {
				fmt.Fprintf(&b, "<details><summary>%s — промпт</summary>\n\n```\n%s\n```\n</details>\n\n", st.Label, st.Prompt)
				if i < len(a.Steps)-1 {
					fmt.Fprintf(&b, "<details><summary>%s — ответ</summary>\n\n```\n%s\n```\n</details>\n\n", st.Label, st.Content)
				}
			}
		}
		if a.Final != "" {
			fmt.Fprintf(&b, "```\n%s\n```\n\n", a.Final)
		}
		if len(a.Checks) > 0 {
			b.WriteString("проверки:\n\n")
			for _, c := range a.Checks {
				mark := "✅"
				if !c.OK {
					mark = "❌"
				}
				fmt.Fprintf(&b, "- %s %s", mark, c.Name)
				if c.Detail != "" {
					fmt.Fprintf(&b, " — %s", c.Detail)
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// Save пишет markdown-отчёт в reports/.
func Save(dir string, res *runner.Result) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := res.RunID
	if name == "" {
		name = "report-" + time.Now().Format("20060102-150405")
	}
	path := filepath.Join(dir, name+".md")
	return path, os.WriteFile(path, []byte(Markdown(res)), 0o644)
}

// noteOf — пояснение варианта из сценария («что этот вариант показывает»).
func noteOf(res *runner.Result, id string) string {
	for _, v := range res.Scenario.Variants {
		if v.ID == id {
			return v.Note
		}
	}
	return ""
}

// simText: у идентичных ответов пишем это словом — в видео читается лучше
// любого числа.
// tokText: на сверхкоротких ответах (или на заглушке) деление на почти
// нулевую задержку даёт бессмысленные тысячи токенов в секунду — такие
// значения не показываем.
func tokText(r Row) string {
	if r.AvgMS < 5 || r.OutTok == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f", r.TokPerSec)
}

func simText(r Row) string {
	if r.Empty {
		return "—" // ответов нет: сравнивать нечего (например ожидаемая ошибка API)
	}
	if r.Identical {
		return "1.00 (совпали)"
	}
	return fmt.Sprintf("%.2f", r.Similarity)
}

func runeLen(s string) int { return len([]rune(s)) }

func short(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func orDef(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
