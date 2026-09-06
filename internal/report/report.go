// Package report собирает из результатов прогона то, что можно показать
// в видео и приложить к заданию: таблицу в консоль и markdown-отчёт.
package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
}

func Rows(res *runner.Result) []Row {
	type agg struct {
		row   Row
		msSum int64
		fails int
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
		if a.Err != nil || !a.ChecksOK() {
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
		out = append(out, g.row)
	}
	return out
}

// Table рисует сводку моноширинной таблицей — читается прямо в видео.
func Table(res *runner.Result) string {
	rows := Rows(res)
	head := []string{"ВАРИАНТ", "МОДЕЛЬ", "ПАРАМЕТРЫ", "СРЕД.МС", "IN", "OUT", "REAS", "$", "FINISH", "УСПЕХ"}
	data := [][]string{}
	for _, r := range rows {
		data = append(data, []string{
			r.Label, short(r.Model, 24), short(r.Params, 34),
			fmt.Sprint(r.AvgMS), fmt.Sprint(r.InTok), fmt.Sprint(r.OutTok),
			fmt.Sprint(r.ReasTok), fmt.Sprintf("%.6f", r.Cost), r.Finish, r.Checks,
		})
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
	b.WriteString("| вариант | модель | параметры | сред. мс | in | out | reasoning | $ | finish | проверки |\n")
	b.WriteString("|---|---|---|---:|---:|---:|---:|---:|---|---|\n")
	for _, r := range Rows(res) {
		fmt.Fprintf(&b, "| %s | `%s` | `%s` | %d | %d | %d | %d | %.6f | %s | %s |\n",
			r.Label, r.Model, r.Params, r.AvgMS, r.InTok, r.OutTok, r.ReasTok, r.Cost, r.Finish, r.Checks)
	}
	b.WriteString("\n")

	var total float64
	for _, a := range res.Attempts {
		total += a.CostUSD
	}
	fmt.Fprintf(&b, "Итого потрачено за прогон: **$%.6f** (по прайсу из `config.yaml`).\n\n", total)

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
