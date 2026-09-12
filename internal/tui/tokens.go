package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/agent"
)

// Токены на экране (день 8).
//
// Под каждым ответом — из чего сложился запрос: сколько весит сам вопрос,
// сколько история, сколько ответ. По Ctrl+T — таблица по ходам разговора:
// видно, что вопрос каждый раз примерно одинаковый, а запрос растёт за счёт
// истории, и каждый следующий вопрос оплачивает весь разговор заново.
// В строке состояния — заполнение контекста следующим запросом.

// window — окно, против которого считать заполнение: свой лимит агента,
// а если его нет — окно модели из config.yaml. Второе значение — чей это
// лимит, чтобы подпись не выдавала окно модели за лимит агента.
func (m *Model) window(cfg agent.Config) (int, string) {
	if cfg.ContextLimit != nil && *cfg.ContextLimit > 0 {
		return *cfg.ContextLimit, "лимит агента"
	}
	for _, mi := range m.set.Catalog {
		if mi.ID == cfg.Model && mi.MaxContext > 0 {
			return mi.MaxContext, "окно модели"
		}
	}
	return 0, ""
}

// tokenLine — подпись под ответом.
func (m *Model) tokenLine(reply *agent.Reply) string {
	resp := reply.Final
	turns := m.ag.Turns()
	parts := []string{
		"finish=" + resp.FinishReason,
		resp.Latency.Round(time.Millisecond).String(),
	}
	if len(turns) > 0 {
		t := turns[len(turns)-1]
		parts = append(parts,
			fmt.Sprintf("вопрос ~%d", t.Question),
			fmt.Sprintf("system+история %d", t.History()),
			fmt.Sprintf("запрос %d", t.Prompt))
		if t.Cached > 0 {
			parts = append(parts, fmt.Sprintf("из кэша %d", t.Cached))
		}
		ans := fmt.Sprintf("ответ %d", t.Completion)
		if t.Reasoning > 0 {
			ans += fmt.Sprintf(" (рассуждения %d)", t.Reasoning)
		}
		parts = append(parts, ans)
		if t.Calls > 1 {
			parts = append(parts, fmt.Sprintf("вызовов %d", t.Calls))
		}
	} else {
		// ответ, начатый до сброса, в учёт не попал — показываем голый usage
		parts = append(parts,
			fmt.Sprintf("запрос %d", resp.Usage.PromptTokens),
			fmt.Sprintf("ответ %d", resp.Usage.CompletionTokens))
	}
	if u := m.contextLabel(); u != "" {
		parts = append(parts, u)
	}
	parts = append(parts, fmt.Sprintf("$%.6f", resp.CostUSD))
	return "  ↳ " + strings.Join(parts, " · ")
}

// contextLabel — «контекст 1830 / 4000 (46%)» для следующего запроса.
func (m *Model) contextLabel() string {
	cfg := m.ag.Config()
	win, whose := m.window(cfg)
	est := m.ag.Context("").Estimated
	if win <= 0 {
		return fmt.Sprintf("контекст ~%d", est)
	}
	return fmt.Sprintf("контекст ~%d / %d %s (%d%%)", est, win, whose, est*100/win)
}

// explainError — что сломалось и что с этим делать. Переполнение контекста —
// не просто строка ошибки: разговор дальше не пойдёт, пока его не укоротить.
func (m *Model) explainError(err error) []string {
	var ov *agent.ErrContextOverflow
	switch {
	case errors.As(err, &ov):
		return []string{
			stErr.Render("контекст переполнен: следующий запрос ~" + fmt.Sprint(ov.Estimated) +
				" токенов, а лимит агента " + fmt.Sprint(ov.Limit)),
			stDim.Render("  запрос не отправлен — ни токенов, ни времени не потрачено; история не изменилась"),
			stDim.Render("  дальше этот разговор не пойдёт: Ctrl+R — сбросить контекст, Ctrl+N — новый агент, или поднять лимит в панели"),
		}
	case agent.IsContextOverflow(err):
		return []string{
			stErr.Render("провайдер отклонил запрос: не влезает в окно модели"),
			stDim.Render("  " + shorten(err.Error(), 240)),
			stDim.Render("  время на запрос ушло, ответа нет; вопрос в историю не попал — Ctrl+R или Ctrl+N"),
		}
	default:
		return []string{
			stErr.Render("ошибка: " + err.Error()),
			stDim.Render("  вопрос в историю агента не попал — его можно задать заново"),
		}
	}
}

// dumpTokens — Ctrl+T: таблица расхода по ходам текущего разговора.
func (m *Model) dumpTokens() {
	m.ag.SetConfig(m.set.AgentConfig())
	turns := m.ag.Turns()
	m.pushLine("")
	m.pushLine(stBot.Render("▸ токены по ходам · агент " + m.ag.ID()))
	if len(turns) == 0 {
		m.pushLine(stDim.Render("  в этом разговоре ещё не было ответов"))
		m.refresh()
		return
	}

	maxPrompt := 0
	for _, t := range turns {
		if t.Prompt > maxPrompt {
			maxPrompt = t.Prompt
		}
	}
	const barW = 24
	head := fmt.Sprintf("  %-3s %7s %8s %8s %7s %8s %8s  %s", "ход", "вопрос~", "sys+ист", "запрос", "ответ", "оценка", "итого", "рост запроса")
	m.pushLine(stDim.Render(head))
	total := 0
	for i, t := range turns {
		total += t.Prompt + t.Completion
		bar := strings.Repeat("█", max(1, t.Prompt*barW/max(maxPrompt, 1)))
		m.pushLine(fmt.Sprintf("  %-3d %7d %8d %8d %7d %8s %8d  %s",
			i+1, t.Question, t.History(), t.Prompt, t.Completion,
			estError(t.Estimated, t.Prompt), total, stNote.Render(bar)))
	}

	first, last := turns[0], turns[len(turns)-1]
	m.pushLine("")
	if len(turns) > 1 && first.Prompt > 0 {
		m.pushLine(stDim.Render(fmt.Sprintf(
			"  запрос вырос в %.1f раза: %d → %d; вопрос весит ~%d, история — %d%% последнего запроса",
			float64(last.Prompt)/float64(first.Prompt), first.Prompt, last.Prompt,
			last.Question, last.History()*100/max(last.Prompt, 1))))
	}
	m.pushLine(stDim.Render(fmt.Sprintf(
		"  расход разговора %d токенов — каждый ход заново оплачивает всю историю · %s",
		total, m.contextLabel())))
	m.pushLine(stDim.Render(fmt.Sprintf(
		"  «оценка» — насколько агент ошибся, считая запрос до отправки; калибровка по факту ×%.2f",
		m.ag.Calibration())))
	m.refresh()
}

// estError — ошибка оценки со знаком: «+4%» — агент насчитал больше факта.
func estError(est, actual int) string {
	if actual <= 0 {
		return "—"
	}
	return fmt.Sprintf("%+d%%", (est-actual)*100/actual)
}
