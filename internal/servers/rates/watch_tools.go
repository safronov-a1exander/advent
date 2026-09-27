package rates

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/mcp"
)

// WithTracker добавляет серверу инструменты слежения (день 18). Сам
// планировщик запускает тот, кто держит сервер, — tr.Run.
func WithTracker(s *mcp.Server, tr *Tracker) *mcp.Server {
	t, f := true, false
	s.Register(mcp.Tool{
		Name:  "watch_rate",
		Title: "Следить за курсом",
		Description: "Завести слежение за рыночным курсом валюты к рублю по расписанию: сервер сам делает замеры " +
			"с заданным периодом и хранит их. Повторный вызов для той же валюты меняет период. Криптовалюты тоже можно: BTC, ETH.",
		InputSchema: mcp.Schema(
			mcp.Prop{Name: "currency", Type: "string", Required: true, Description: "код валюты: USD, EUR, TRY, BTC…"},
			mcp.Prop{Name: "every", Type: "string", Required: true, Description: "период замеров: 30s, 5m, 1h"},
		),
		Annotations: &mcp.Annotations{ReadOnlyHint: &f, DestructiveHint: &f, IdempotentHint: &t},
	}, func(ctx context.Context, raw json.RawMessage) mcp.CallResult {
		a, bad := mcp.Args[struct {
			Currency string `json:"currency"`
			Every    string `json:"every"`
		}](raw)
		if bad != nil {
			return *bad
		}
		every, err := time.ParseDuration(strings.TrimSpace(a.Every))
		if err != nil {
			return mcp.ErrorResult("период %q не понять: пиши как 30s, 5m, 1h", a.Every)
		}
		w, existed, err := tr.Add(ctx, a.Currency, every)
		if err != nil {
			return mcp.ErrorResult("%v", err)
		}
		verb := "Слежу"
		if existed {
			verb = "Период изменён: слежу"
		}
		return structured(fmt.Sprintf("%s за %s каждые %s; первый замер — сейчас. Замеров в истории: %d.",
			verb, w.Code, every, len(w.Samples)),
			map[string]any{"currency": w.Code, "every": every.String(), "samples": len(w.Samples)})
	})

	s.Register(mcp.Tool{
		Name:        "unwatch_rate",
		Title:       "Перестать следить",
		Description: "Снять слежение за валютой. История замеров удаляется вместе с ним.",
		InputSchema: mcp.Schema(mcp.Prop{Name: "currency", Type: "string", Required: true, Description: "код валюты"}),
		Annotations: &mcp.Annotations{ReadOnlyHint: &f, DestructiveHint: &t},
	}, func(ctx context.Context, raw json.RawMessage) mcp.CallResult {
		a, bad := mcp.Args[struct {
			Currency string `json:"currency"`
		}](raw)
		if bad != nil {
			return *bad
		}
		ok, err := tr.Remove(a.Currency)
		switch {
		case err != nil:
			return mcp.ErrorResult("%v", err)
		case !ok:
			return mcp.ErrorResult("за %s и так не следим", strings.ToUpper(a.Currency))
		}
		return mcp.TextResult("Слежение за " + strings.ToUpper(a.Currency) + " снято.")
	})

	s.Register(mcp.Tool{
		Name:        "list_watches",
		Title:       "Что отслеживается",
		Description: "Список валют под слежением: период, сколько замеров накоплено, когда последний.",
		Annotations: &mcp.Annotations{ReadOnlyHint: &t},
	}, func(ctx context.Context, raw json.RawMessage) mcp.CallResult {
		list := tr.List()
		if len(list) == 0 {
			return mcp.TextResult("Ни за одной валютой не следим.")
		}
		var lines []string
		for _, w := range list {
			line := fmt.Sprintf("%s: каждые %s, замеров %d", w.Code, w.Every, len(w.Samples))
			if n := len(w.Samples); n > 0 {
				line += fmt.Sprintf(", последний %s — %s RUB", w.Samples[n-1].At.Format("02.01 15:04:05"), num(w.Samples[n-1].Value))
			}
			if w.Errors > 0 {
				line += fmt.Sprintf(", ошибок подряд %d (%s)", w.Errors, w.LastError)
			}
			lines = append(lines, line)
		}
		return mcp.TextResult(strings.Join(lines, "\n"))
	})

	s.Register(mcp.Tool{
		Name:  "rate_digest",
		Title: "Сводка по курсу",
		Description: "Агрегированная сводка по накопленным замерам валюты за окно: первый и последний курс, " +
			"минимум и максимум со временем, среднее, изменение в процентах, пропуски в замерах.",
		InputSchema: mcp.Schema(
			mcp.Prop{Name: "currency", Type: "string", Required: true, Description: "код валюты под слежением"},
			mcp.Prop{Name: "window", Type: "string", Description: "за какой срок: 15m, 1h, 24h; по умолчанию 24h"},
		),
		Annotations: &mcp.Annotations{ReadOnlyHint: &t},
	}, func(ctx context.Context, raw json.RawMessage) mcp.CallResult {
		a, bad := mcp.Args[struct {
			Currency string `json:"currency"`
			Window   string `json:"window"`
		}](raw)
		if bad != nil {
			return *bad
		}
		window := 24 * time.Hour
		if strings.TrimSpace(a.Window) != "" {
			w, err := time.ParseDuration(strings.TrimSpace(a.Window))
			if err != nil || w <= 0 {
				return mcp.ErrorResult("окно %q не понять: пиши как 15m, 1h, 24h", a.Window)
			}
			window = w
		}
		d, err := tr.Digest(a.Currency, window)
		if err != nil {
			return mcp.ErrorResult("%v", err)
		}
		text := fmt.Sprintf("%s за %s (%s–%s): замеров %d; было %s, стало %s RUB (%+.3f%%); "+
			"минимум %s в %s, максимум %s в %s; среднее %s.",
			d.Code, window, d.From.Format("15:04:05"), d.To.Format("15:04:05"), d.Count,
			num(d.First), num(d.Last), d.ChangePct,
			num(d.Min), d.MinAt.Format("15:04:05"), num(d.Max), d.MaxAt.Format("15:04:05"), num(d.Avg))
		if d.Gaps > 0 {
			text += fmt.Sprintf(" Пропусков в замерах: %d — сервер не работал или рынок не отвечал.", d.Gaps)
		}
		return structured(text, d)
	})
	return s
}
