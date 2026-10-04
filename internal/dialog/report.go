package dialog

import (
	"fmt"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/mcp"
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
		if !l.Checked() {
			continue
		}
		fmt.Fprintf(&b, "### %d. %s\n\n", i+1, l.Say)
		if l.Note != "" {
			b.WriteString("_" + l.Note + "_\n\n")
		}
		if len(l.Expect) > 0 {
			fmt.Fprintf(&b, "Ожидали у всех: %s\n\n", "`"+strings.Join(l.Expect, "`, `")+"`")
		}
		if len(l.Forbid) > 0 {
			fmt.Fprintf(&b, "Не должно быть ни у кого: %s\n\n", "`"+strings.Join(l.Forbid, "`, `")+"`")
		}
		if len(l.Tools) > 0 {
			fmt.Fprintf(&b, "Инструменты по порядку: %s\n\n", "`"+strings.Join(l.Tools, "` → `")+"`")
		}
		for _, ch := range l.Chains {
			fmt.Fprintf(&b, "Цепочка: %s\n\n", "`"+strings.Join(ch, "` → `")+"`")
		}
		for _, p := range l.Passes {
			fmt.Fprintf(&b, "Стык: результат `%s` → аргументы `%s`\n\n", p.From, p.To)
		}
		if l.NoTools {
			b.WriteString("Инструменты звать незачем.\n\n")
		}
		if l.UsesResult {
			b.WriteString("Ответ должен опираться на результат инструмента.\n\n")
		}
		if len(l.Sources) > 0 {
			fmt.Fprintf(&b, "Источники в базе знаний: %s\n\n", "`"+strings.Join(l.Sources, "`, `")+"`")
		}
		if l.NoSources {
			b.WriteString("Ответа в базе нет — фрагментов в запросе быть не должно.\n\n")
		}
		// Ожидания у профилей разные по построению: джуниору код нужен,
		// продакту запрещён. Поэтому персональные проверки печатаются рядом,
		// иначе по отчёту непонятно, почему один вариант «прошёл», а другой
		// с тем же ответом — нет.
		for _, r := range res {
			own, ok := l.ExpectBy[r.Variant.Name]
			if !ok || own.Empty() {
				continue
			}
			fmt.Fprintf(&b, "Отдельно у «%s»:", r.Variant.Name)
			if len(own.Expect) > 0 {
				fmt.Fprintf(&b, " ждём %s", "`"+strings.Join(own.Expect, "`, `")+"`")
			}
			if len(own.Forbid) > 0 {
				fmt.Fprintf(&b, " · не должно быть %s", "`"+strings.Join(own.Forbid, "`, `")+"`")
			}
			b.WriteString("\n\n")
		}
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
			fmt.Fprintf(&b, "- **%s**%s %s\n", r.Variant.Name, where, mark)
			// День 17: что агент вызывал и что получил — до ответа, потому
			// что ответ собран из этого.
			for _, o := range st.Tools {
				res := "↳"
				if o.IsError {
					res = "✗"
				}
				fmt.Fprintf(&b, "  - ⚙ `%s` `%s` %s %s\n", displayTool(o), cell(o.Args, 160), res, cell(o.Text, 200))
			}
			// День 22: с какими фрагментами ушёл вопрос.
			for _, h := range st.Sources {
				fmt.Fprintf(&b, "  - ⌕ %.3f `%s` %s\n", h.Score, h.ID, cell(h.Section, 100))
			}
			// День 24: что показала проверка цитат и что решил судья.
			if c := st.Citation; c != nil {
				switch {
				case c.Refused:
					b.WriteString("  - ∅ «не знаю» сказал код: ни один фрагмент не прошёл порог\n")
				case !c.Known:
					b.WriteString("  - ∅ «не знаю» сказала модель\n")
				default:
					line := fmt.Sprintf("  - ❝ источников %d, цитат %d, дословно %d", len(c.IDs), len(c.Quotes), c.Verbatim())
					if p := c.Problems(); len(p) > 0 {
						line += " · " + strings.Join(p, "; ")
					}
					b.WriteString(line + "\n")
				}
			}
			if v := st.Verdict; v != nil {
				if v.Supported {
					b.WriteString("  - ⚖ судья: смысл ответа подтверждён цитатами\n")
				} else {
					fmt.Fprintf(&b, "  - ⚖ судья: не подтверждено — %s\n", cell(v.Unsupported, 200))
				}
			}
			fmt.Fprintf(&b, "  > %s\n", cell(st.Answer, 400))
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
		if r.Task == nil {
			continue
		}
		fmt.Fprintf(&b, "## Задача к концу прогона — %s\n\n", r.Variant.Name)
		fmt.Fprintf(&b, "%s\n\n", r.Task.Resume())
		// День 15: сколько раз задаче отказали в переходе. Ноль у варианта
		// без карты — не порядок, а отсутствие проверки.
		if n := r.Task.Rejected(); n > 0 {
			fmt.Fprintf(&b, "Отказов в переходах: **%d** (подробности в журнале ниже).\n\n", n)
		}
		if len(r.Task.Plan) > 0 {
			b.WriteString("| # | шаг плана | сделан |\n|---:|---|---|\n")
			for i, s := range r.Task.Plan {
				mark := ""
				if i+1 < r.Task.Step {
					mark = "да"
				} else if i+1 == r.Task.Step {
					mark = "← сейчас"
				}
				fmt.Fprintf(&b, "| %d | %s | %s |\n", i+1, cell(s, 90), mark)
			}
			b.WriteString("\n")
		}
		// Журнал стадий — не отладка. Главное свойство дня 13 (пауза и
		// продолжение) видно именно по нему: где остановились и почему.
		if len(r.Task.Log) > 0 {
			b.WriteString("Журнал стадий:\n\n")
			for _, e := range r.Task.Log {
				// Запись без стадий — это событие вроде «план утверждён»:
				// оно про задачу, но не про переход, и «— текст» без стрелки
				// в отчёте выглядел бы обрывком.
				line := "- "
				switch {
				case e.From != "" && e.To != "":
					line += string(e.From) + " → " + string(e.To)
				case e.To != "":
					line += string(e.To)
				}
				if e.Note != "" {
					if strings.TrimSpace(line) != "-" {
						line += " — "
					}
					line += e.Note
				}
				b.WriteString(line + "\n")
			}
			b.WriteString("\n")
		}
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
		if l.Checked() {
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
	if c.Profile != "" {
		label += " + профиль " + c.Profile
	}
	if len(c.MCP) > 0 {
		label += " + MCP " + strings.Join(c.MCP, ", ")
	}
	if c.TaskState != "" {
		label += " + " + agent.TaskLabel(c.TaskState)
		if c.TaskMap == agent.TaskMapPrompt {
			label += " (карта только в промпте)"
		}
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

// displayTool — «сервер.инструмент», а если модель назвала функцию,
// которой нет, — то имя, что она назвала.
func displayTool(o mcp.Outcome) string {
	if o.Server == "" || o.Tool == "" {
		return o.Func
	}
	return o.Server + "." + o.Tool
}
