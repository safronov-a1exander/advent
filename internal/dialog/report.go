package dialog

import (
	"fmt"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/agent"
)

// Markdown — отчёт о прогоне: итоги, рост запроса по ходам, проверки
// на память с ответами и сводки к концу разговора.
func Markdown(s *Scenario, provider string, res []Result, started time.Time) string {
	var b strings.Builder
	title := s.Name
	if title == "" {
		title = "сравнение стратегий контекста"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	if d := strings.TrimSpace(s.Description); d != "" {
		b.WriteString(d + "\n\n")
	}
	fmt.Fprintf(&b, "- провайдер: `%s`\n- прогон: %s\n- реплик в диалоге: %d, из них с проверкой на память: %d\n\n",
		provider, started.Format("2006-01-02 15:04:05"), len(s.Dialog), countChecks(s))

	b.WriteString("## Итоги\n\n")
	b.WriteString("| вариант | контекст | вход, всего | из них из кэша | из них на сжатие | выход | вызовов сжатия | память | $ |\n")
	b.WriteString("|---|---|---:|---:|---:|---:|---:|---|---:|\n")
	for _, r := range res {
		t := r.Totals()
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | %d | %d/%d | %.6f |\n",
			r.Variant.Name, contextOf(r.Variant), t.Input(), t.Cached, t.CompressPrompt,
			t.Output(), t.CompressCalls, t.Passed, t.Checks, t.Cost)
	}
	b.WriteString("\n«Вход, всего» включает служебные вызовы сжатия: сжатие само стоит токенов, " +
		"и без них сравнение было бы нечестным. «Из кэша» — входные токены, которые провайдер " +
		"взял из кэша префикса и тарифицировал дешевле.\n\n")

	b.WriteString("## Запрос по ходам\n\n")
	b.WriteString("Токены запроса (факт провайдера) и сколько сообщений истории ушло вместе с вопросом.\n\n")
	b.WriteString("| ход | реплика |")
	for _, r := range res {
		fmt.Fprintf(&b, " %s |", r.Variant.Name)
	}
	b.WriteString("\n|---:|---|")
	for range res {
		b.WriteString("---:|")
	}
	b.WriteString("\n")
	for i, l := range s.Dialog {
		fmt.Fprintf(&b, "| %d | %s |", i+1, cell(l.Say, 60))
		for _, r := range res {
			b.WriteString(" " + stepCell(r, i) + " |")
		}
		b.WriteString("\n")
	}

	b.WriteString("\n## Проверки на память\n\n")
	for i, l := range s.Dialog {
		if len(l.Expect) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %d. %s\n\n", i+1, l.Say)
		if l.Note != "" {
			b.WriteString("_" + l.Note + "_\n\n")
		}
		fmt.Fprintf(&b, "Ожидали в ответе: %s\n\n", "`"+strings.Join(l.Expect, "`, `")+"`")
		for _, r := range res {
			if i >= len(r.Steps) {
				continue
			}
			st := r.Steps[i]
			mark := "✓"
			switch {
			case st.Err != "":
				mark = "✗ ошибка: " + st.Err
			case !st.Passed:
				mark = "✗ нет: " + strings.Join(st.Missing, ", ")
			}
			fmt.Fprintf(&b, "- **%s** %s\n  > %s\n", r.Variant.Name, mark, cell(st.Answer, 400))
		}
		b.WriteString("\n")
	}

	var withSummary bool
	for _, r := range res {
		if r.Summary != "" {
			withSummary = true
		}
	}
	if withSummary {
		b.WriteString("## Сводка к концу разговора\n\n")
		for _, r := range res {
			if r.Summary == "" {
				continue
			}
			fmt.Fprintf(&b, "**%s**\n\n```\n%s\n```\n\n", r.Variant.Name, strings.TrimSpace(r.Summary))
		}
	}
	return b.String()
}

func countChecks(s *Scenario) int {
	n := 0
	for _, l := range s.Dialog {
		if len(l.Expect) > 0 {
			n++
		}
	}
	return n
}

func contextOf(c agent.Config) string {
	sum := agent.ContextLabel(c.Context)
	if c.Context == agent.ContextSummary {
		sum += fmt.Sprintf(" (хвост %d, сжатие каждые %d)", c.KeepLastN(), c.SummarizeEveryN())
	}
	return sum
}

func stepCell(r Result, i int) string {
	if i >= len(r.Steps) {
		return "—"
	}
	st := r.Steps[i]
	if st.Err != "" {
		return "ошибка"
	}
	c := fmt.Sprintf("%d · %d сообщ.", st.Turn.Prompt, st.Turn.Sent)
	if st.Turn.CompressCalls > 0 {
		c += fmt.Sprintf(" · +сжатие %d", st.Turn.CompressPrompt)
	}
	if st.Checked {
		if st.Passed {
			c += " ✓"
		} else {
			c += " ✗"
		}
	}
	return c
}

// cell — текст в одну строку для таблицы markdown.
func cell(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, "|", "/")
	r := []rune(s)
	if len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return s
}
