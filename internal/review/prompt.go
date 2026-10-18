package review

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/safronov-a1exander/advent/internal/rag"
)

const systemPrompt = `Ты — ревьюер pull request в проекте на Go. Тебе дают описание PR, правила проекта и связанный код из базы знаний, результаты проверок кодом и diff.
Найди то, что стоит поправить до мёржа:
- bugs — потенциальные ошибки: неверная логика, гонки, необработанные ошибки, утечки, падения на граничных случаях, уязвимости;
- architecture — архитектурные проблемы: нарушение устройства проекта и его правил, связность, дублирование, не тот слой;
- recommendations — рекомендации: тесты, читаемость, документация.
Правила:
- замечание только про файлы из diff; file — путь как в diff, line — номер строки новой версии файла, если он понятен из хунка;
- не пересказывай diff и не хвали; нет замечаний — пустой список, не выдумывай;
- проверки кодом уже выполнены: не повторяй их находки, но учитывай;
- verdict: request_changes — если есть баг high; comment — если есть что поправить; approve — если нечего;
- пиши по-русски, коротко.
Ответ — только JSON-объект:
{"summary": "что делает PR и общее впечатление, 1-2 предложения",
 "verdict": "approve | comment | request_changes",
 "bugs": [{"file": "...", "line": 0, "severity": "high | medium | low", "problem": "...", "fix": "..."}],
 "architecture": [{"file": "...", "problem": "...", "fix": "..."}],
 "recommendations": [{"file": "...", "problem": "..."}]}`

func buildPrompt(in Input, checks []Check, ctxHits []rag.Hit, maxDiff int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PR: %s\nветка %s → %s", in.Title, in.Head, in.Base)
	if in.SHA != "" {
		fmt.Fprintf(&b, " · %s", short(in.SHA))
	}
	b.WriteString("\n")
	if s := strings.TrimSpace(in.Body); s != "" {
		b.WriteString("Описание PR:\n" + clipRunes(s, 2000) + "\n")
	}
	b.WriteString("\nИзменённые файлы:\n")
	for _, f := range in.Files {
		fmt.Fprintf(&b, "- %s (%s, +%d −%d)\n", f.Path, f.Status, f.Added, f.Deleted)
	}
	if len(ctxHits) > 0 {
		b.WriteString("\nИз базы знаний проекта — правила и связанный код (ближайшие к изменениям):\n")
		for _, h := range ctxHits {
			fmt.Fprintf(&b, "\n[%s] %s\n%s\n", h.ID, h.Header(), clipRunes(h.Text, 1800))
		}
	}
	if len(checks) > 0 {
		b.WriteString("\nПроверки кодом:\n")
		for _, c := range checks {
			fmt.Fprintf(&b, "- %s: %s\n", c.Name, c.Status())
			for _, d := range c.Details {
				b.WriteString("  - " + d + "\n")
			}
		}
	}
	diff := in.Diff
	if utf8.RuneCountInString(diff) > maxDiff {
		diff = clipRunes(diff, maxDiff) + "\n… diff обрезан: показано " + fmt.Sprint(maxDiff) + " символов"
	}
	b.WriteString("\nDiff:\n" + diff)
	return b.String()
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Marker — метка в теле комментария: по ней видно, что ревью этой версии
// уже опубликовано, и повторный запуск его не дублирует.
func Marker(sha string) string { return "<!-- advent-review sha=" + sha + " -->" }

var verdictLabel = map[string]string{
	Approve:        "✅ можно мёржить",
	Comment:        "💬 есть что поправить",
	RequestChanges: "⛔ нужны изменения",
}

var sevLabel = map[string]string{"high": "🔴 high", "medium": "🟠 medium", "low": "🟡 low"}

// Markdown — ревью для человека: файл в reports/ и комментарий в PR.
func (r *Result) Markdown() string {
	var b strings.Builder
	title := "AI-ревью"
	if r.Input.PR > 0 {
		title += fmt.Sprintf(" PR #%d", r.Input.PR)
	}
	fmt.Fprintf(&b, "## %s · `%s` → `%s`", title, r.Input.Head, r.Input.Base)
	if r.Input.SHA != "" {
		fmt.Fprintf(&b, " · %s", short(r.Input.SHA))
	}
	b.WriteString("\n\n")

	if r.Failed {
		b.WriteString("> ⚠ **AI-ревью не получилось** — нужен человек. ")
		fmt.Fprintf(&b, "Попыток: %d, последняя ошибка: %s. Ниже — только проверки кодом.\n\n", len(r.Attempts), r.Error)
	} else {
		a := r.Answer
		fmt.Fprintf(&b, "**%s** — %s\n\n", verdictLabel[a.Verdict], strings.TrimSpace(a.Summary))
		section(&b, "Потенциальные баги", a.Bugs, true)
		section(&b, "Архитектура", a.Architecture, false)
		section(&b, "Рекомендации", a.Recommendations, false)
	}

	b.WriteString("### Проверки кодом\n\n")
	for _, c := range r.Checks {
		mark := "✓"
		switch {
		case c.Skipped:
			mark = "–"
		case !c.OK:
			mark = "✗"
		}
		fmt.Fprintf(&b, "- %s **%s**: %s\n", mark, c.Name, c.Status())
		for _, d := range c.Details {
			b.WriteString("  - " + d + "\n")
		}
	}
	if len(r.Dropped) > 0 {
		fmt.Fprintf(&b, "\n<details><summary>Отброшено сверкой: %d замечаний про файлы не из diff</summary>\n\n", len(r.Dropped))
		for _, f := range r.Dropped {
			fmt.Fprintf(&b, "- `%s` — %s\n", f.File, f.Problem)
		}
		b.WriteString("\n</details>\n")
	}

	cost, in, out := r.Cost()
	model := r.Model
	if r.Fallback {
		model += " (запасная)"
	}
	if model == "" {
		model = "—"
	}
	fmt.Fprintf(&b, "\n---\n<sub>advent review · модель %s · попыток %d · контекст из RAG: правил %d, связанного %d · in %d / out %d · $%.4f · %s</sub>\n",
		model, len(r.Attempts), r.Rules, r.Related, in, out, cost, r.Took.Round(time.Second))
	b.WriteString(Marker(r.Input.SHA) + "\n")
	return b.String()
}

func section(b *strings.Builder, title string, list []Finding, sev bool) {
	fmt.Fprintf(b, "### %s\n\n", title)
	if len(list) == 0 {
		b.WriteString("_нет_\n\n")
		return
	}
	for _, f := range list {
		b.WriteString("- ")
		if sev && f.Severity != "" {
			b.WriteString(sevLabel[f.Severity] + " · ")
		}
		if f.File != "" {
			loc := f.File
			if f.Line > 0 {
				loc += fmt.Sprintf(":%d", f.Line)
			}
			b.WriteString("`" + loc + "` — ")
		}
		b.WriteString(strings.TrimSpace(f.Problem))
		if f.Fix != "" {
			b.WriteString(" → " + strings.TrimSpace(f.Fix))
		}
		if f.Note != "" {
			b.WriteString(" _(" + f.Note + ")_")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}
