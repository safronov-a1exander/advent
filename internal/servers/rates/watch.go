package rates

// Слежение за курсом по расписанию (день 18).
//
// Курс ЦБ публикуется раз в день, и собирать его каждую минуту бессмысленно.
// Для слежения нужен курс рынка — он меняется постоянно. Его отдаёт
// публичный API Coinbase: таблица курсов к доллару для фиатных валют
// и криптовалют, из неё рубль за единицу считается кросс-курсом.
//
// Расписание живёт в самом сервере, а не в агенте: у агента нет «пульса»,
// он просыпается, только когда его спрашивают. Сервер работает отдельным
// процессом, копит замеры в JSON-файле и переживает перезапуск — задания
// и история лежат на диске, а пропущенные замеры не выдумываются задним
// числом: прошлого курса рынка у нас нет, и в сводке это видно дырой.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Market — источник текущего курса рынка: рублей за единицу валюты.
type Market interface {
	Quote(ctx context.Context, code string) (float64, error)
}

// Coinbase — курсы с api.coinbase.com. Таблица одна на все валюты,
// поэтому за один замер хватает одного запроса.
type Coinbase struct {
	Base string
	HTTP *http.Client
}

func NewCoinbase() *Coinbase {
	return &Coinbase{Base: "https://api.coinbase.com", HTTP: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Coinbase) Quote(ctx context.Context, code string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/v2/exchange-rates?currency=USD", nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("рынок не ответил: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return 0, fmt.Errorf("рынок: http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var body struct {
		Data struct {
			Rates map[string]string `json:"rates"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("ответ рынка не разобрать: %w", err)
	}
	perUSD := func(k string) (float64, error) {
		v, ok := body.Data.Rates[k]
		if !ok {
			return 0, fmt.Errorf("валюты %s нет на рынке", k)
		}
		return strconv.ParseFloat(v, 64)
	}
	rub, err := perUSD("RUB")
	if err != nil {
		return 0, err
	}
	if code == "USD" {
		return rub, nil
	}
	x, err := perUSD(code)
	if err != nil {
		return 0, err
	}
	if x == 0 {
		return 0, fmt.Errorf("нулевой курс %s", code)
	}
	return rub / x, nil
}

// Walk — подставной рынок для репетиций и тестов: случайное блуждание
// вокруг правдоподобной цены. Настоящие цифры в сводке появляются только
// с настоящим рынком.
type Walk struct {
	mu   sync.Mutex
	last map[string]float64
}

func (w *Walk) Quote(_ context.Context, code string) (float64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.last == nil {
		w.last = map[string]float64{"USD": 84, "EUR": 96, "TRY": 1.73, "CNY": 11.8, "BTC": 9_100_000}
	}
	v, ok := w.last[code]
	if !ok {
		return 0, fmt.Errorf("валюты %s нет на рынке", code)
	}
	v *= 1 + (rand.Float64()-0.5)*0.004
	w.last[code] = v
	return v, nil
}

// Sample — один замер.
type Sample struct {
	At    time.Time `json:"at"`
	Value float64   `json:"rub"`
}

// Watch — задание слежения за одной валютой.
type Watch struct {
	Code    string        `json:"code"`
	Every   time.Duration `json:"every_ns"`
	Created time.Time     `json:"created"`
	Next    time.Time     `json:"next"`
	// Errors — сколько замеров подряд не удалось; LastError — почему.
	Errors    int      `json:"errors,omitempty"`
	LastError string   `json:"last_error,omitempty"`
	Samples   []Sample `json:"samples"`
}

// MaxSamples — сколько замеров держать на валюту. При замере раз в минуту
// это неделя; старые уходят, чтобы файл не рос вечно.
const MaxSamples = 10_000

// MinEvery — чаще не спрашиваем: чужой API — не наш, а курс за десять
// секунд всё равно почти не меняется.
const MinEvery = 10 * time.Second

// Tracker — задания, замеры и планировщик, который их выполняет.
type Tracker struct {
	path   string
	market Market
	now    func() time.Time

	mu      sync.Mutex
	watches map[string]*Watch
	// Log — что происходит в планировщике; сервер отдаёт его в stderr.
	Log func(format string, a ...any)
}

// NewTracker поднимает задания из файла path (если он есть).
func NewTracker(path string, m Market) (*Tracker, error) {
	t := &Tracker{path: path, market: m, now: time.Now, watches: map[string]*Watch{},
		Log: func(string, ...any) {}}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return t, nil
	case err != nil:
		return nil, err
	}
	var list []*Watch
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, w := range list {
		t.watches[w.Code] = w
	}
	return t, nil
}

// save пишет задания атомарно: сначала во временный файл, потом
// переименование. Упади процесс посреди записи — останется прежний файл,
// а не половина JSON.
func (t *Tracker) save() error {
	list := make([]*Watch, 0, len(t.watches))
	for _, w := range t.watches {
		list = append(list, w)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Code < list[j].Code })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, t.path)
}

// Add заводит слежение или меняет период существующего. Первый замер —
// сразу: иначе до первой сводки пришлось бы ждать целый период.
func (t *Tracker) Add(ctx context.Context, code string, every time.Duration) (*Watch, bool, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if every < MinEvery {
		return nil, false, fmt.Errorf("период %s слишком частый: не чаще раза в %s", every, MinEvery)
	}
	if _, err := t.market.Quote(ctx, code); err != nil {
		return nil, false, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	w, existed := t.watches[code]
	if !existed {
		w = &Watch{Code: code, Created: t.now()}
		t.watches[code] = w
	}
	w.Every = every
	w.Next = t.now()
	return w, existed, t.save()
}

// Remove снимает слежение вместе с историей.
func (t *Tracker) Remove(code string) (bool, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.watches[code]; !ok {
		return false, nil
	}
	delete(t.watches, code)
	return true, t.save()
}

// List — копии заданий по алфавиту.
func (t *Tracker) List() []Watch {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Watch, 0, len(t.watches))
	for _, w := range t.watches {
		c := *w
		c.Samples = append([]Sample(nil), w.Samples...)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// Tick выполняет задания, чей срок подошёл. Сеть — вне замка: медленный
// ответ рынка не должен держать вызовы инструментов.
func (t *Tracker) Tick(ctx context.Context) int {
	now := t.now()
	t.mu.Lock()
	var due []string
	for code, w := range t.watches {
		if !w.Next.After(now) {
			due = append(due, code)
		}
	}
	t.mu.Unlock()
	sort.Strings(due)

	done := 0
	for _, code := range due {
		v, err := t.market.Quote(ctx, code)
		t.mu.Lock()
		w, ok := t.watches[code]
		if !ok { // сняли, пока ходили на рынок
			t.mu.Unlock()
			continue
		}
		// Следующий срок — от текущего момента, а не от прошлого срока:
		// после простоя сервер не должен пытаться «догнать» пропущенные
		// замеры пачкой — они всё равно были бы одним и тем же курсом.
		w.Next = now.Add(w.Every)
		if err != nil {
			w.Errors++
			w.LastError = err.Error()
			t.Log("замер %s не удался (%d подряд): %v", code, w.Errors, err)
		} else {
			w.Errors, w.LastError = 0, ""
			w.Samples = append(w.Samples, Sample{At: now, Value: v})
			if len(w.Samples) > MaxSamples {
				w.Samples = w.Samples[len(w.Samples)-MaxSamples:]
			}
			done++
			t.Log("замер %s = %.4f RUB (всего %d)", code, v, len(w.Samples))
		}
		if err := t.save(); err != nil {
			t.Log("не сохранил задания: %v", err)
		}
		t.mu.Unlock()
	}
	return done
}

// Run — планировщик: проверяет сроки каждые step, пока жив ctx.
func (t *Tracker) Run(ctx context.Context, step time.Duration) {
	t.Tick(ctx)
	tk := time.NewTicker(step)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			t.Tick(ctx)
		}
	}
}

// Digest — агрегат замеров за окно.
type Digest struct {
	Code      string    `json:"currency"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Count     int       `json:"samples"`
	First     float64   `json:"first"`
	Last      float64   `json:"last"`
	Min       float64   `json:"min"`
	MinAt     time.Time `json:"min_at"`
	Max       float64   `json:"max"`
	MaxAt     time.Time `json:"max_at"`
	Avg       float64   `json:"avg"`
	ChangePct float64   `json:"change_pct"`
	// Gaps — сколько раз между замерами прошло больше двух периодов:
	// сервер не работал или рынок не отвечал.
	Gaps  int           `json:"gaps"`
	Every time.Duration `json:"every_ns"`
}

// Digest считает сводку по замерам за последние window.
func (t *Tracker) Digest(code string, window time.Duration) (Digest, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	t.mu.Lock()
	w, ok := t.watches[code]
	var samples []Sample
	var every time.Duration
	if ok {
		every = w.Every
		since := t.now().Add(-window)
		for _, s := range w.Samples {
			if !s.At.Before(since) {
				samples = append(samples, s)
			}
		}
	}
	t.mu.Unlock()
	if !ok {
		return Digest{}, fmt.Errorf("за %s не следим: сначала watch_rate", code)
	}
	if len(samples) == 0 {
		return Digest{}, fmt.Errorf("по %s нет замеров за последние %s", code, window)
	}
	d := Digest{Code: code, Count: len(samples), Every: every,
		From: samples[0].At, To: samples[len(samples)-1].At,
		First: samples[0].Value, Last: samples[len(samples)-1].Value,
		Min: math.Inf(1), Max: math.Inf(-1)}
	var sum float64
	for i, s := range samples {
		sum += s.Value
		if s.Value < d.Min {
			d.Min, d.MinAt = s.Value, s.At
		}
		if s.Value > d.Max {
			d.Max, d.MaxAt = s.Value, s.At
		}
		if i > 0 && every > 0 && s.At.Sub(samples[i-1].At) > 2*every {
			d.Gaps++
		}
	}
	d.Avg = sum / float64(len(samples))
	if d.First != 0 {
		d.ChangePct = (d.Last/d.First - 1) * 100
		if math.Abs(d.ChangePct) < 0.0005 {
			d.ChangePct = 0 // иначе в сводке «-0.000%» от ошибки округления
		}
	}
	return d, nil
}
