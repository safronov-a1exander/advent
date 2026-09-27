// Package goals — MCP-сервер «Цели» (день 20): накопления на что-то
// конкретное — сумма, срок и план, как успеть.
//
// Сервер нарочно знает только рубли. Цель «60 000 лир к марту» он принять
// не может: пересчёт — работа сервера «Курсы», а сколько человек реально
// откладывает — сервера «Бюджет». Так устроены настоящие системы: у каждой
// своя область, и собрать ответ из трёх может только агент. Это и есть
// задание двадцатого дня.
package goals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/safronov-a1exander/advent/internal/mcp"
)

// Version — версия сервера в ответе на initialize.
const Version = "day-20"

// Goal — одна цель.
type Goal struct {
	Name     string    `json:"name"`
	Amount   float64   `json:"amount_rub"`
	Saved    float64   `json:"saved_rub"`
	Deadline string    `json:"deadline"` // ГГГГ-ММ-ДД
	Note     string    `json:"note,omitempty"`
	Created  time.Time `json:"created"`
}

type store struct {
	path string
	now  func() time.Time
	mu   sync.Mutex
	list map[string]Goal
}

// New — сервер целей с хранилищем в JSON-файле path.
func New(path string, now func() time.Time) (*mcp.Server, error) {
	if now == nil {
		now = time.Now
	}
	st := &store{path: path, now: now, list: map[string]Goal{}}
	if b, err := os.ReadFile(path); err == nil {
		var goals []Goal
		if err := json.Unmarshal(b, &goals); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, g := range goals {
			st.list[key(g.Name)] = g
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	s := mcp.NewServer("goals", Version,
		"Цели накоплений пользователя. Суммы только в рублях: сумму в валюте сначала пересчитай другим инструментом.")
	t, f := true, false
	s.Register(mcp.Tool{
		Name: "add_goal", Title: "Завести цель",
		Description: "Завести цель накоплений или обновить существующую с тем же названием. Сумма — только в рублях.",
		InputSchema: mcp.Schema(
			mcp.Prop{Name: "name", Type: "string", Required: true, Description: "название цели, например «Стамбул»"},
			mcp.Prop{Name: "amount_rub", Type: "number", Required: true, Description: "сколько нужно накопить, в рублях"},
			mcp.Prop{Name: "deadline", Type: "string", Required: true, Description: "к какой дате, ГГГГ-ММ-ДД"},
			mcp.Prop{Name: "saved_rub", Type: "number", Description: "сколько уже отложено, в рублях; по умолчанию 0"},
			mcp.Prop{Name: "note", Type: "string", Description: "пояснение, например исходная сумма в валюте"},
		),
		Annotations: &mcp.Annotations{ReadOnlyHint: &f, DestructiveHint: &f, IdempotentHint: &t},
	}, st.add)
	s.Register(mcp.Tool{
		Name: "list_goals", Title: "Цели", Description: "Все цели накоплений: сумма, отложено, срок.",
		Annotations: &mcp.Annotations{ReadOnlyHint: &t},
	}, st.listGoals)
	s.Register(mcp.Tool{
		Name: "plan_goal", Title: "План накоплений",
		Description: "Успевает ли пользователь к сроку цели, если откладывать по monthly_rub в месяц: " +
			"сколько месяцев до срока, когда накопится при таком темпе, сколько нужно в месяц, чтобы успеть.",
		InputSchema: mcp.Schema(
			mcp.Prop{Name: "name", Type: "string", Required: true, Description: "название цели"},
			mcp.Prop{Name: "monthly_rub", Type: "number", Required: true, Description: "сколько откладывать в месяц, в рублях"},
		),
		Annotations: &mcp.Annotations{ReadOnlyHint: &t},
	}, st.plan)
	return s, nil
}

func key(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func (st *store) save() error {
	goals := make([]Goal, 0, len(st.list))
	for _, g := range st.list {
		goals = append(goals, g)
	}
	sort.Slice(goals, func(i, j int) bool { return goals[i].Name < goals[j].Name })
	b, err := json.MarshalIndent(goals, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(st.path), 0o755); err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}

func (st *store) add(_ context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct {
		Name     string   `json:"name"`
		Amount   *float64 `json:"amount_rub"`
		Deadline string   `json:"deadline"`
		Saved    float64  `json:"saved_rub"`
		Note     string   `json:"note"`
	}](raw)
	if bad != nil {
		return *bad
	}
	switch {
	case strings.TrimSpace(a.Name) == "":
		return mcp.ErrorResult("у цели нет названия")
	case a.Amount == nil || *a.Amount <= 0:
		return mcp.ErrorResult("сумма цели amount_rub должна быть больше нуля и в рублях")
	case a.Saved < 0:
		return mcp.ErrorResult("отложено не может быть меньше нуля")
	}
	d, err := time.Parse("2006-01-02", strings.TrimSpace(a.Deadline))
	if err != nil {
		return mcp.ErrorResult("срок %q не в формате ГГГГ-ММ-ДД", a.Deadline)
	}
	if !d.After(st.now()) {
		return mcp.ErrorResult("срок %s уже прошёл", d.Format("02.01.2006"))
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	_, existed := st.list[key(a.Name)]
	g := Goal{Name: strings.TrimSpace(a.Name), Amount: *a.Amount, Saved: a.Saved,
		Deadline: d.Format("2006-01-02"), Note: a.Note, Created: st.now()}
	st.list[key(a.Name)] = g
	if err := st.save(); err != nil {
		return mcp.ErrorResult("не сохранил цель: %v", err)
	}
	verb := "Цель заведена"
	if existed {
		verb = "Цель обновлена"
	}
	return mcp.TextResult(fmt.Sprintf("%s: «%s» — %s RUB к %s, отложено %s RUB. До срока полных месяцев: %d.",
		verb, g.Name, rub(g.Amount), d.Format("02.01.2006"), rub(g.Saved), monthsUntil(st.now(), d)))
}

func (st *store) listGoals(_ context.Context, _ json.RawMessage) mcp.CallResult {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.list) == 0 {
		return mcp.TextResult("Целей пока нет.")
	}
	var names []string
	for k := range st.list {
		names = append(names, k)
	}
	sort.Strings(names)
	var lines []string
	for _, k := range names {
		g := st.list[k]
		line := fmt.Sprintf("«%s»: %s RUB к %s, отложено %s RUB", g.Name, rub(g.Amount), g.Deadline, rub(g.Saved))
		if g.Note != "" {
			line += " (" + g.Note + ")"
		}
		lines = append(lines, line)
	}
	return mcp.TextResult(strings.Join(lines, "\n"))
}

func (st *store) plan(_ context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct {
		Name    string   `json:"name"`
		Monthly *float64 `json:"monthly_rub"`
	}](raw)
	if bad != nil {
		return *bad
	}
	st.mu.Lock()
	g, ok := st.list[key(a.Name)]
	st.mu.Unlock()
	if !ok {
		return mcp.ErrorResult("цели «%s» нет: сначала add_goal", a.Name)
	}
	if a.Monthly == nil || *a.Monthly <= 0 {
		return mcp.ErrorResult("сколько откладывать в месяц, monthly_rub, должно быть больше нуля")
	}
	d, _ := time.Parse("2006-01-02", g.Deadline)
	left := math.Max(g.Amount-g.Saved, 0)
	months := monthsUntil(st.now(), d)
	need := int(math.Ceil(left / *a.Monthly))
	reach := st.now().AddDate(0, need, 0)
	verdict := fmt.Sprintf("успеваешь: накопится к %s", reach.Format("01.2006"))
	if need > months {
		verdict = fmt.Sprintf("НЕ успеваешь: при таком темпе накопится только к %s", reach.Format("01.2006"))
	}
	required := "—"
	if months > 0 {
		required = rub(math.Ceil(left/float64(months))) + " RUB"
	}
	return mcp.TextResult(fmt.Sprintf("«%s»: осталось накопить %s RUB. При %s RUB в месяц нужно месяцев: %d, до срока %s — %d; %s. Чтобы успеть к сроку, нужно откладывать %s в месяц.",
		g.Name, rub(left), rub(*a.Monthly), need, d.Format("02.01.2006"), months, verdict, required))
}

// monthsUntil — сколько полных месяцев от from до to: столько раз
// успеет прийти ежемесячный перевод в копилку.
func monthsUntil(from, to time.Time) int {
	m := (to.Year()-from.Year())*12 + int(to.Month()) - int(from.Month())
	if to.Day() < from.Day() {
		m--
	}
	if m < 0 {
		return 0
	}
	return m
}

func rub(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	return strings.TrimSuffix(strings.TrimRight(strings.TrimRight(s, "0"), "."), ".")
}
