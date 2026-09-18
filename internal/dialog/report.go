package dialog

import (
	"fmt"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/memory"
)

// Markdown — отчёт о прогоне: итоги, рост запроса по ходам, проверки
// на память с ответами, а к концу разговора — сводки, факты и ветки.
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
		provider, started.Format("2006-01-02 15:04:05"), countSays(s), countChecks(s))

	b.WriteString("## Итоги\n\n")
	b.WriteString("| вариант | контекст | вход, всего | из них из кэша | из них служебные | выход | служебных вызовов | память | $ |\n")
	b.WriteString("|---|---|---:|---:|---:|---:|---:|---|---:|\n")
	for _, r := range res {
		t := r.Totals()
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | %d | %d/%d | %.6f |\n",
			r.Variant.Name, VariantLabel(r), t.Input(), t.Cached, t.AuxPrompt,
			t.Output(), t.AuxCalls, t.Passed, t.Checks, t.Cost)
	}
	b.WriteString("\n«Вход, всего» включает служебные вызовы — сжатие в сводку и обновление фактов: " +
		"они сами стоят токенов, и без них сравнение было бы нечестным. «Из кэша» — входные токены, " +
		"которые провайдер взял из кэша префикса и тарифицировал дешевле.\n\n")

	b.WriteString("## Запрос по ходам\n\n")
	b.WriteString("Токены запроса (факт провайдера), сколько сообщений истории ушло вместе с вопросом, " +
		"служебные вызовы и ветка, если не основная. Строки «⎇» — команды веток: вариант без веток их пропускает. " +
		"Строки «🧠» — команды памяти; «↺» значит новый разговор: история стёрта, слои задачи и пользователя остались.\n\n")
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
		fmt.Fprintf(&b, "| %d | %s |", i+1, cell(l.Text(), 60))
		for _, r := range res {
			c := "—"
			if i < len(r.Steps) {
				c = r.Steps[i].Brief(true)
			}
			b.WriteString(" " + cell(c, 80) + " |")
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
			where := ""
			if st.Branch != "" && st.Branch != agent.MainBranch {
				where = " _(ветка «" + st.Branch + "»)_"
			}
			fmt.Fprintf(&b, "- **%s**%s %s\n  > %s\n", r.Variant.Name, where, mark, cell(st.Answer, 400))
		}
		b.WriteString("\n")
	}

	for _, r := range res {
		if len(r.BranchList) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## Ветки варианта «%s»\n\n| ветка | от чекпойнта | сообщений | активна |\n|---|---|---:|---|\n", r.Variant.Name)
		for _, br := range r.BranchList {
			active := ""
			if br.Active {
				active = "да"
			}
			from := br.From
			if from == "" {
				from = "—"
			}
			fmt.Fprintf(&b, "| %s | %s | %d | %s |\n", br.Name, from, br.Messages, active)
		}
		b.WriteString("\n")
	}

	for _, r := range res {
		if len(r.Layers) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## Слои памяти к концу прогона — %s\n\n", r.Variant.Name)
		for _, sc := range memory.Scopes {
			entries := r.Layers[sc]
			if len(entries) == 0 {
				continue
			}
			fmt.Fprintf(&b, "**%s (%s)** — %s\n\n", sc, sc.Label(), sc.Hint())
			b.WriteString("| ключ | значение | положил |\n|---|---|---|\n")
			for _, e := range entries {
				fmt.Fprintf(&b, "| %s | %s | %s |\n", cell(e.Key, 40), cell(e.Value, 120), e.Source)
			}
			b.WriteString("\n")
		}
	}

	for _, r := range res {
		if len(r.Facts) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## Факты к концу разговора — %s\n\n", r.Variant.Name)
		for _, f := range r.Facts {
			fmt.Fprintf(&b, "- **%s**: %s\n", f.Key, f.Value)
		}
		b.WriteString("\n")
	}

	for _, r := range res {
		if r.Summary == "" {
			continue
		}
		fmt.Fprintf(&b, "## Сводка к концу разговора — %s\n\n```\n%s\n```\n\n", r.Variant.Name, strings.TrimSpace(r.Summary))
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

func countSays(s *Scenario) int {
	n := 0
	for _, l := range s.Dialog {
		if l.Command() == "" {
			n++
		}
	}
	return n
}

// VariantLabel — стратегия контекста варианта с параметрами.
func VariantLabel(r Result) string {
	c := r.Variant
	label := agent.ContextLabel(c.Context)
	switch c.Context {
	case agent.ContextSummary:
		label += fmt.Sprintf(" (хвост %d, сжатие каждые %d)", c.KeepLastN(), c.SummarizeEveryN())
	case agent.ContextWindow, agent.ContextFacts:
		label += fmt.Sprintf(" (хвост %d)", c.KeepLastN())
	}
	if r.Branches {
		label += " + ветки"
	}
	if c.Memory != "" {
		label += " + " + agent.MemoryLabel(c.Memory)
	}
	return label
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
