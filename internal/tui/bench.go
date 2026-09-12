package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/llm"
)

// Сравнение моделей прямо из чата (задание дня 5).
//
// По Ctrl+E последний заданный вопрос уходит на все модели из config.yaml,
// и рядом печатается таблица: задержка, скорость генерации, токены, цена.
// То есть «сравнить слабую, среднюю и сильную» делается без YAML-сценария.
//
// С шестого дня это пачка агентов: на каждую модель пул порождает агента
// с копией текущего конфига и пустой историей, все отвечают параллельно,
// потом агенты убираются. Отдельной логики запроса здесь больше нет.

type benchRow struct {
	Agent   string
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

	// Сравниваем модели, а не разговоры: у агентов сравнения пустая история
	// и прямой ответ без цепочки, иначе разница в таблице была бы не про модель.
	base := m.set.AgentConfig()
	base.Strategy = agent.StrategyDirect
	base.Stream = false
	agents := make([]*agent.Agent, 0, len(models))
	tiers := map[string]string{}
	for _, mi := range models {
		cfg := base.Clone()
		cfg.Name = "bench"
		cfg.Model = mi.ID
		// временные: сравнение не должно оставлять разговоров в списке
		a := m.pool.SpawnTemp(cfg)
		agents = append(agents, a)
		tiers[a.ID()] = mi.Tier
	}
	question := m.lastQuestion

	m.pushLine("")
	m.pushLine(stBot.Render(fmt.Sprintf("▸ сравнение моделей: %d агента(ов) из пула отвечают параллельно", len(agents))))
	m.pushLine(stDim.Render(shorten(question, 200)))
	m.busy.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	m.cancel = cancel
	ch := make(chan tea.Msg, 64)
	m.stream = ch
	pool := m.pool

	go func() {
		defer cancel()
		row := func(r agent.Result) benchRow {
			cfg := r.Agent.Config()
			out := benchRow{Agent: r.Agent.ID(), Model: cfg.Model, Tier: tiers[r.Agent.ID()]}
			if r.Err != nil {
				out.Err = r.Err.Error()
				return out
			}
			resp := r.Reply.Final
			out.Latency, out.Usage, out.Cost, out.Answer = resp.Latency, resp.Usage, resp.CostUSD, resp.Content
			return out
		}
		results := pool.AskAll(ctx, agents, question, 0, func(r agent.Result) { ch <- benchStepMsg(row(r)) })
		rows := make([]benchRow, 0, len(results))
		for _, r := range results {
			rows = append(rows, row(r))
			pool.Remove(r.Agent.ID())
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
