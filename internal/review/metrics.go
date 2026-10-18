package review

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Metric — одна строка журнала пайплайна: по ним считаются метрики из
// лекции — латентность p50/p95, цена на PR против средней, доля прогонов
// без вмешательства человека.
type Metric struct {
	At       time.Time `json:"at"`
	PR       int       `json:"pr,omitempty"`
	SHA      string    `json:"sha"`
	Model    string    `json:"model,omitempty"`
	Fallback bool      `json:"fallback,omitempty"`
	OK       bool      `json:"ok"` // модель дала разобранный ответ
	Attempts int       `json:"attempts"`
	TookMS   int64     `json:"took_ms"`
	Prompt   int       `json:"prompt_tokens"`
	Output   int       `json:"completion_tokens"`
	CostUSD  float64   `json:"cost_usd"`
	Files    int       `json:"files"`
	Bugs     int       `json:"bugs"`
	Arch     int       `json:"architecture"`
	Recs     int       `json:"recommendations"`
	Dropped  int       `json:"dropped"`
	Verdict  string    `json:"verdict,omitempty"`
}

// MetricOf — строка журнала по итогу прогона.
func MetricOf(r *Result) Metric {
	cost, in, out := r.Cost()
	m := Metric{
		At: time.Now(), PR: r.Input.PR, SHA: r.Input.SHA, Model: r.Model, Fallback: r.Fallback,
		OK: !r.Failed, Attempts: len(r.Attempts), TookMS: r.Took.Milliseconds(),
		Prompt: in, Output: out, CostUSD: cost, Files: len(r.Input.Files), Dropped: len(r.Dropped),
	}
	if a := r.Answer; a != nil {
		m.Bugs, m.Arch, m.Recs, m.Verdict = len(a.Bugs), len(a.Architecture), len(a.Recommendations), a.Verdict
	}
	return m
}

// AppendMetric дописывает строку в журнал path.
func AppendMetric(path string, m Metric) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(m)
	_, err = f.Write(append(b, '\n'))
	return err
}

// LoadMetrics читает журнал; нет файла — пустой журнал.
func LoadMetrics(path string) ([]Metric, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Metric
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m Metric
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out, sc.Err()
}

// Stats — сводка по последним прогонам.
type Stats struct {
	Runs     int
	OK       int
	Fallback int
	P50, P95 time.Duration
	AvgCost  float64
	// Last — последний прогон; LastX — во сколько раз он дороже среднего
	// по предыдущим. Алерт из лекции — при ×2.
	Last  Metric
	LastX float64
}

// Window — по скольким последним прогонам считать базу (лекция: 20).
const Window = 20

// Summarize — метрики по журналу.
func Summarize(ms []Metric) Stats {
	var s Stats
	if len(ms) == 0 {
		return s
	}
	if len(ms) > Window {
		ms = ms[len(ms)-Window:]
	}
	s.Runs = len(ms)
	var took []time.Duration
	var cost float64
	for _, m := range ms {
		if m.OK {
			s.OK++
		}
		if m.Fallback {
			s.Fallback++
		}
		took = append(took, time.Duration(m.TookMS)*time.Millisecond)
		cost += m.CostUSD
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	s.P50, s.P95 = pct(took, 50), pct(took, 95)
	s.AvgCost = cost / float64(len(ms))
	s.Last = ms[len(ms)-1]
	if len(ms) > 1 {
		prev := (cost - s.Last.CostUSD) / float64(len(ms)-1)
		if prev > 0 {
			s.LastX = s.Last.CostUSD / prev
		}
	}
	return s
}

func pct(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := (len(sorted)*p + 99) / 100
	if i < 1 {
		i = 1
	}
	return sorted[i-1]
}

// String — для консоли.
func (s Stats) String() string {
	if s.Runs == 0 {
		return "прогонов ещё не было"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "прогонов %d (последние до %d) · без вмешательства %d из %d (%.0f%%) · на запасной модели %d\n",
		s.Runs, Window, s.OK, s.Runs, 100*float64(s.OK)/float64(s.Runs), s.Fallback)
	fmt.Fprintf(&b, "латентность p50 %s · p95 %s\n", s.P50.Round(100*time.Millisecond), s.P95.Round(100*time.Millisecond))
	fmt.Fprintf(&b, "цена ревью: средняя $%.4f · последний $%.4f", s.AvgCost, s.Last.CostUSD)
	if s.LastX > 0 {
		fmt.Fprintf(&b, " (×%.1f к среднему по предыдущим)", s.LastX)
		if s.LastX >= 2 {
			b.WriteString(" ⚠ АЛЕРТ: ревью подорожало вдвое — смотреть промпт и размер diff")
		}
	}
	return b.String()
}
