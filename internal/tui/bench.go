package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/store"
)

// Сравнение моделей прямо из чата (задание дня 5).
//
// По Ctrl+E последний заданный вопрос уходит на все модели из config.yaml
// по очереди, и рядом печатается таблица: задержка, скорость генерации,
// токены, цена. То есть «сравнить слабую, среднюю и сильную» делается
// без единого YAML-сценария.

type benchRow struct {
	Model   string
	Tier    string
	Latency time.Duration
	Usage   llm.Usage
	Cost    float64
	Answer  string
	Err     string
}

type benchStepMsg benchRow
type benchDoneMsg struct{ rows []benchRow }

func (m *Model) startBench() tea.Cmd {
	if m.lastQuestion == "" {
		m.pushLine(stDim.Render("— нечего сравнивать: сначала задай вопрос —"))
		m.refresh()
		return nil
	}
	models := m.set.Catalog
	if len(models) < 2 {
		m.pushLine(stErr.Render("в config.yaml меньше двух моделей у этого провайдера — сравнивать не с чем"))
		m.refresh()
		return nil
	}

	tmpl := m.buildRequest("")
	tmpl.Messages = nil
	sys := strings.TrimSpace(m.set.System)
	question := m.lastQuestion

	m.pushLine("")
	m.pushLine(stBot.Render(fmt.Sprintf("▸ сравнение моделей (%d шт.) на одном вопросе", len(models))))
	m.pushLine(stDim.Render(shorten(question, 200)))
	m.busy.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	m.cancel = cancel
	ch := make(chan tea.Msg, 64)
	m.stream = ch
	client := m.opts.Client
	journal := m.opts.Store
	provider := m.opts.Provider

	go func() {
		defer cancel()
		var rows []benchRow
		for _, mi := range models {
			req := tmpl
			req.Model = mi.ID
			var msgs []llm.Message
			if sys != "" {
				msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: sys})
			}
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: question})
			req.Messages = msgs

			row := benchRow{Model: mi.ID, Tier: mi.Tier}
			resp, err := client.Chat(ctx, req)

			rec := store.Record{
				Scenario: "chat-bench", Variant: mi.ID,
				Provider: provider, Model: mi.ID,
				Params: store.ParamsOf(req), Messages: msgs,
			}
			if err != nil {
				row.Err = err.Error()
				rec.Error = err.Error()
			} else {
				row.Latency = resp.Latency
				row.Usage = resp.Usage
				row.Cost = resp.CostUSD
				row.Answer = resp.Content

				rec.Content = resp.Content
				rec.Reasoning = resp.Reasoning
				rec.Finish = resp.FinishReason
				rec.Usage = resp.Usage
				rec.LatencyMS = resp.Latency.Milliseconds()
				rec.CostUSD = resp.CostUSD
			}
			if journal != nil {
				_ = journal.Append(rec)
			}
			rows = append(rows, row)
			ch <- benchStepMsg(row)
		}
		ch <- benchDoneMsg{rows: rows}
	}()

	m.refresh()
	return tea.Batch(m.waitChunk(), m.sp.Tick)
}

// benchTable — компактная сводка. Своя, а не из пакета report: тот работает
// с результатами YAML-сценария, а здесь строки собраны на лету.
func benchTable(rows []benchRow) []string {
	head := []string{"МОДЕЛЬ", "КЛАСС", "МС", "ТОК/С", "IN", "OUT", "REAS", "$"}
	data := make([][]string, 0, len(rows))
	for _, r := range rows {
		if r.Err != "" {
			data = append(data, []string{r.Model, r.Tier, "—", "—", "—", "—", "—", "—"})
			continue
		}
		tps := "—"
		if ms := r.Latency.Milliseconds(); ms >= 5 && r.Usage.CompletionTokens > 0 {
			tps = fmt.Sprintf("%.1f", float64(r.Usage.CompletionTokens)/(float64(ms)/1000))
		}
		data = append(data, []string{
			r.Model, orDash(r.Tier),
			fmt.Sprint(r.Latency.Milliseconds()), tps,
			fmt.Sprint(r.Usage.PromptTokens),
			fmt.Sprint(r.Usage.CompletionTokens),
			fmt.Sprint(r.Usage.ReasoningTokens),
			fmt.Sprintf("%.6f", r.Cost),
		})
	}

	w := make([]int, len(head))
	for i, h := range head {
		w[i] = len([]rune(h))
	}
	for _, r := range data {
		for i, c := range r {
			if n := len([]rune(c)); n > w[i] {
				w[i] = n
			}
		}
	}
	render := func(cells []string) string {
		var b strings.Builder
		for i, c := range cells {
			b.WriteString(c)
			if i < len(cells)-1 {
				b.WriteString(strings.Repeat(" ", w[i]-len([]rune(c))+2))
			}
		}
		return b.String()
	}

	out := []string{render(head)}
	sep := make([]string, len(head))
	for i := range sep {
		sep[i] = strings.Repeat("─", w[i])
	}
	out = append(out, render(sep))
	for _, r := range data {
		out = append(out, render(r))
	}
	return out
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
