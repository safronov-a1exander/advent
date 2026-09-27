package rates

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/mcp"
)

// Version — версия сервера в ответе на initialize.
const Version = "day-17"

const instructions = "Курсы валют ЦБ РФ. Используй, когда нужна сумма в другой валюте " +
	"или курс на дату: сам курс не угадывай, он меняется каждый день."

// New — сервер «Курсы» с инструментами поверх src.
func New(src Source) *mcp.Server {
	s := mcp.NewServer("rates", Version, instructions)

	t := true
	readOnly := &mcp.Annotations{ReadOnlyHint: &t}

	s.Register(mcp.Tool{
		Name:  "exchange_rate",
		Title: "Курс валюты",
		Description: "Официальный курс ЦБ РФ: сколько рублей стоит одна единица валюты. " +
			"Без даты — последний опубликованный курс. На выходной день — курс последнего рабочего дня перед ним.",
		InputSchema: mcp.Schema(
			mcp.Prop{Name: "currency", Type: "string", Required: true, Description: "код валюты ISO 4217: USD, EUR, TRY, CNY…"},
			mcp.Prop{Name: "date", Type: "string", Description: "дата ГГГГ-ММ-ДД; пусто — последний курс"},
		),
		Annotations: readOnly,
	}, func(ctx context.Context, raw json.RawMessage) mcp.CallResult {
		a, bad := mcp.Args[struct {
			Currency string `json:"currency"`
			Date     string `json:"date"`
		}](raw)
		if bad != nil {
			return *bad
		}
		d, res := daily(ctx, src, a.Date)
		if res != nil {
			return *res
		}
		r, ok := d.Lookup(a.Currency)
		if !ok {
			return mcp.ErrorResult("валюты %q нет в таблице ЦБ на %s", a.Currency, d.Date.Format("02.01.2006"))
		}
		change := ""
		if r.Previous > 0 && r.Code != "RUB" {
			change = fmt.Sprintf(" (днём раньше %s, %+.2f%%)", num(r.Previous/r.Nominal), (r.Value/r.Previous-1)*100)
		}
		text := fmt.Sprintf("1 %s = %s RUB по курсу ЦБ на %s%s", r.Code, num(r.PerUnit()), d.Date.Format("02.01.2006"), change)
		return structured(text, map[string]any{
			"currency": r.Code, "name": r.Name, "rub_per_unit": round4(r.PerUnit()),
			"date": d.Date.Format("2006-01-02"),
		})
	})

	s.Register(mcp.Tool{
		Name:  "convert",
		Title: "Пересчёт суммы",
		Description: "Пересчитать сумму из одной валюты в другую по курсу ЦБ РФ (кросс-курс через рубль). " +
			"Используй для трат в валюте и планов поездок.",
		InputSchema: mcp.Schema(
			mcp.Prop{Name: "amount", Type: "number", Required: true, Description: "сумма в исходной валюте"},
			mcp.Prop{Name: "from", Type: "string", Required: true, Description: "код исходной валюты: USD, EUR, TRY, RUB…"},
			mcp.Prop{Name: "to", Type: "string", Description: "код валюты результата; по умолчанию RUB"},
			mcp.Prop{Name: "date", Type: "string", Description: "дата курса ГГГГ-ММ-ДД; пусто — последний курс"},
		),
		Annotations: readOnly,
	}, func(ctx context.Context, raw json.RawMessage) mcp.CallResult {
		a, bad := mcp.Args[struct {
			Amount *float64 `json:"amount"`
			From   string   `json:"from"`
			To     string   `json:"to"`
			Date   string   `json:"date"`
		}](raw)
		if bad != nil {
			return *bad
		}
		if a.Amount == nil {
			return mcp.ErrorResult("не задана сумма amount")
		}
		if a.To == "" {
			a.To = "RUB"
		}
		d, res := daily(ctx, src, a.Date)
		if res != nil {
			return *res
		}
		from, ok := d.Lookup(a.From)
		if !ok {
			return mcp.ErrorResult("валюты %q нет в таблице ЦБ", a.From)
		}
		to, ok := d.Lookup(a.To)
		if !ok {
			return mcp.ErrorResult("валюты %q нет в таблице ЦБ", a.To)
		}
		out := *a.Amount * from.PerUnit() / to.PerUnit()
		text := fmt.Sprintf("%s %s = %s %s по курсу ЦБ на %s (1 %s = %s RUB)",
			num(*a.Amount), from.Code, num(out), to.Code, d.Date.Format("02.01.2006"),
			from.Code, num(from.PerUnit()))
		if from.Code == "RUB" {
			text = fmt.Sprintf("%s RUB = %s %s по курсу ЦБ на %s (1 %s = %s RUB)",
				num(*a.Amount), num(out), to.Code, d.Date.Format("02.01.2006"), to.Code, num(to.PerUnit()))
		}
		return structured(text, map[string]any{
			"amount": *a.Amount, "from": from.Code, "to": to.Code, "result": round2(out),
			"date": d.Date.Format("2006-01-02"),
		})
	})
	return s
}

// daily достаёт таблицу курсов на дату из аргумента. Ошибка — уже
// результат для модели: неверная дата — это то, что она может исправить.
func daily(ctx context.Context, src Source, date string) (*Daily, *mcp.CallResult) {
	var at time.Time
	if strings.TrimSpace(date) != "" {
		t, err := time.Parse("2006-01-02", strings.TrimSpace(date))
		if err != nil {
			r := mcp.ErrorResult("дата %q не в формате ГГГГ-ММ-ДД", date)
			return nil, &r
		}
		if t.After(time.Now()) {
			r := mcp.ErrorResult("дата %s в будущем: курса на неё ещё нет", t.Format("02.01.2006"))
			return nil, &r
		}
		at = t
	}
	d, err := src.Daily(ctx, at)
	if err != nil {
		r := mcp.ErrorResult("%v", err)
		return nil, &r
	}
	return d, nil
}

// structured — ответ в двух видах: текст для модели и структура для кода.
func structured(text string, v any) mcp.CallResult {
	r := mcp.TextResult(text)
	r.StructuredContent, _ = json.Marshal(v)
	return r
}

// num — число без лишних нулей и с точкой: «84.2714», «1250», «0.35».
func num(v float64) string {
	s := fmt.Sprintf("%.4f", v)
	if math.Abs(v) >= 100 {
		s = fmt.Sprintf("%.2f", v)
	}
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
