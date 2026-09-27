// Package rates — MCP-сервер «Курсы» вокруг API курсов ЦБ РФ (день 17).
//
// Задание дня — свой сервер вокруг любого API. Для ассистента по личным
// тратам самый полезный внешний API — курсы валют: в выписке есть списания
// в долларах, в поездке траты в лирах, а модель курса не знает и знать
// не может — у неё дата обучения, а курс меняется каждый день.
//
// Главное в сервере — маппинг, о котором весь слайд лекции: инструмент,
// который видит модель, превращается в конкретный HTTP-запрос к сервису,
// а ответ сервиса — в CallToolResult.
package rates

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Rate — курс одной валюты к рублю за Nominal единиц.
type Rate struct {
	Code     string  `json:"CharCode"`
	Nominal  float64 `json:"Nominal"`
	Name     string  `json:"Name"`
	Value    float64 `json:"Value"`
	Previous float64 `json:"Previous"`
}

// PerUnit — рублей за одну единицу валюты.
func (r Rate) PerUnit() float64 { return r.Value / r.Nominal }

// Daily — курсы ЦБ на одну дату.
type Daily struct {
	Date   time.Time       `json:"Date"`
	Valute map[string]Rate `json:"Valute"`
}

// Lookup — курс валюты. Рубль — единица отсчёта, его в таблице нет.
func (d *Daily) Lookup(code string) (Rate, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "RUB" || code == "RUR" {
		return Rate{Code: "RUB", Nominal: 1, Name: "Российский рубль", Value: 1, Previous: 1}, true
	}
	r, ok := d.Valute[code]
	return r, ok
}

// Source — откуда берутся курсы. Настоящий ЦБ в работе, подставной в тестах.
type Source interface {
	// Daily — курсы на дату; нулевая дата — последние опубликованные.
	Daily(ctx context.Context, date time.Time) (*Daily, error)
}

// CBR — курсы ЦБ РФ в JSON с зеркала cbr-xml-daily.ru. Сам cbr.ru отдаёт
// XML в windows-1251; зеркало отдаёт те же цифры в UTF-8 и держит архив
// по датам.
type CBR struct {
	Base string
	HTTP *http.Client

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	d  *Daily
	at time.Time
}

// NewCBR — источник с кэшем по датам: курс за прошедший день не меняется,
// и спрашивать его дважды незачем.
func NewCBR() *CBR {
	return &CBR{Base: "https://www.cbr-xml-daily.ru", HTTP: &http.Client{Timeout: 15 * time.Second}, cache: map[string]cached{}}
}

// ErrNoDate — на эту дату курс не публиковался (выходной, праздник, будущее).
type ErrNoDate struct{ Date time.Time }

func (e ErrNoDate) Error() string {
	return fmt.Sprintf("на %s ЦБ курс не публиковал", e.Date.Format("02.01.2006"))
}

func (c *CBR) Daily(ctx context.Context, date time.Time) (*Daily, error) {
	if date.IsZero() {
		// Последний курс кэшируем ненадолго: ЦБ публикует новый раз в день.
		return c.fetch(ctx, "latest", c.Base+"/daily_json.js", 10*time.Minute)
	}
	// Курс на выходной — это курс последнего рабочего дня перед ним:
	// ЦБ в выходные не публикует, а платить в субботу всё равно по какому-то
	// курсу надо. Шагаем назад не больше недели.
	for back := 0; back < 7; back++ {
		d := date.AddDate(0, 0, -back)
		url := fmt.Sprintf("%s/archive/%s/daily_json.js", c.Base, d.Format("2006/01/02"))
		daily, err := c.fetch(ctx, d.Format("2006-01-02"), url, 0)
		if _, missing := err.(ErrNoDate); missing {
			continue
		}
		return daily, err
	}
	return nil, ErrNoDate{Date: date}
}

func (c *CBR) fetch(ctx context.Context, key, url string, ttl time.Duration) (*Daily, error) {
	c.mu.Lock()
	if e, ok := c.cache[key]; ok && (ttl == 0 || time.Since(e.at) < ttl) {
		c.mu.Unlock()
		return e.d, nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ЦБ не ответил: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t, _ := time.Parse("2006-01-02", key)
		return nil, ErrNoDate{Date: t}
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("ЦБ: http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var d Daily
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("ответ ЦБ не разобрать: %w", err)
	}
	c.mu.Lock()
	c.cache[key] = cached{d: &d, at: time.Now()}
	c.mu.Unlock()
	return &d, nil
}
