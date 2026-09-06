package tui

import (
	"fmt"

	"charm.land/bubbles/v2/table"
	"charm.land/lipgloss/v2"

	"github.com/safronov-a1exander/advent/internal/report"
	"github.com/safronov-a1exander/advent/internal/runner"
)

// Сводка прогона в виде настоящей таблицы.
//
// Раньше сетка рисовалась вручную и была просто текстом: длинный список
// вариантов не прокручивался, а строку нельзя было выбрать. Готовый компонент
// даёт и то, и другое, а по Enter экран переключается на ответы выбранного
// варианта — из таблицы сразу видно, что именно смотреть.

// summaryColumns — ширины числовых колонок фиксированы, а текстовые делят
// остаток: иначе на узком окне «ПАРАМЕТРЫ» съедают всё место.
func summaryColumns(width int, showSimilarity bool) []table.Column {
	fixed := []table.Column{
		{Title: "МС", Width: 7},
		{Title: "ТОК/С", Width: 7},
		{Title: "IN", Width: 6},
		{Title: "OUT", Width: 6},
		{Title: "REAS", Width: 6},
		{Title: "$", Width: 9},
		{Title: "FINISH", Width: 7},
		{Title: "УСПЕХ", Width: 6},
	}
	if showSimilarity {
		fixed = append(fixed, table.Column{Title: "СХОДСТВО", Width: 15})
	}

	used := 0
	for _, c := range fixed {
		used += c.Width + 2 // padding ячейки
	}
	rest := width - used - 6
	if rest < 30 {
		rest = 30
	}
	// вариант — половина остатка, модель и параметры делят вторую
	variant := rest / 2
	model := rest / 4
	params := rest - variant - model
	if params < 8 {
		params = 8
	}

	cols := []table.Column{
		{Title: "ВАРИАНТ", Width: variant},
		{Title: "МОДЕЛЬ", Width: model},
		{Title: "ПАРАМЕТРЫ", Width: params},
	}
	return append(cols, fixed...)
}

func summaryRows(res *runner.Result, showSimilarity bool) []table.Row {
	out := make([]table.Row, 0, len(report.Rows(res)))
	for _, r := range report.Rows(res) {
		row := table.Row{
			r.Label, r.Model, r.Params,
			fmt.Sprint(r.AvgMS), report.TokPerSecText(r),
			fmt.Sprint(r.InTok), fmt.Sprint(r.OutTok), fmt.Sprint(r.ReasTok),
			fmt.Sprintf("%.6f", r.Cost), r.Finish, r.Checks,
		}
		if showSimilarity {
			row = append(row, report.SimilarityText(r))
		}
		out = append(out, row)
	}
	return out
}

// newSummaryTable собирает таблицу по результатам прогона.
func newSummaryTable(res *runner.Result, width, height int) table.Model {
	multi := res.Scenario.Repeat > 1
	// Ширину задать обязательно: без неё вьюпорт таблицы нулевой,
	// и на экране остаётся только строка заголовков без единой строки данных.
	t := table.New(
		table.WithColumns(summaryColumns(width, multi)),
		table.WithRows(summaryRows(res, multi)),
		table.WithWidth(width),
		table.WithHeight(height),
		table.WithFocused(true),
	)
	applySummaryStyles(&t)
	return t
}

// applySummaryStyles красит таблицу под текущую тему. Вызывается и при
// пересборке темы: цвета в стилях фиксируются в момент присваивания.
func applySummaryStyles(t *table.Model) {
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true).Foreground(cAccent)
	s.Cell = s.Cell.Foreground(lipgloss.Color("")).UnsetForeground()
	s.Selected = s.Selected.Bold(true).Foreground(cAccent)
	t.SetStyles(s)
}
