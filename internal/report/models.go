package report

import (
	"fmt"
	"strings"

	"github.com/safronov-a1exander/advent/internal/runner"
)

// modelsSection — характеристики задействованных моделей и ссылки на них.
// Для вывода о различиях между моделями нужны их характеристики и ссылки,
// поэтому таблица попадает в отчёт автоматически, как только в прогоне
// участвует больше одной модели.
func modelsSection(res *runner.Result) string {
	seen := map[string]bool{}
	var used []string
	for _, a := range res.Attempts {
		if !seen[a.Model] {
			seen[a.Model] = true
			used = append(used, a.Model)
		}
	}
	if len(used) < 2 || len(res.Models) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Модели\n\n")
	b.WriteString("| модель | класс | параметров | квантизация | контекст | in $/1M | out $/1M | ссылка |\n")
	b.WriteString("|---|---|---:|---|---:|---:|---:|---|\n")
	for _, id := range used {
		m, ok := res.Models[id]
		if !ok {
			fmt.Fprintf(&b, "| `%s` | — | — | — | — | — | — | — |\n", id)
			continue
		}
		link := "—"
		if m.URL != "" {
			link = "[страница модели](" + m.URL + ")"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %.4f | %.4f | %s |\n",
			id, orDef(m.Tier, "—"), paramsText(m.ParamsB), orDef(m.Quant, "—"),
			ctxText(m.MaxContext), m.InPer1M, m.OutPer1M, link)
	}
	b.WriteString("\nПрайс берётся из `config.yaml`. Он должен совпадать с актуальным " +
		"прайсом провайдера — иначе колонка стоимости врёт.\n\n")
	return b.String()
}

func paramsText(b float64) string {
	if b <= 0 {
		return "—"
	}
	return fmt.Sprintf("%gB", b)
}

func ctxText(n int) string {
	if n <= 0 {
		return "—"
	}
	return fmt.Sprintf("%dK", n/1000)
}
