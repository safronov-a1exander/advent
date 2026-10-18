// Package crm — MCP-сервер «CRM поддержки» (день 33): пользователи
// приватной модели, их ключи, обращения и журнал запросов к шлюзу.
//
// Ассистент поддержки отвечает не «вообще», а этому человеку по этому
// обращению. Документация скажет, что значит 401, а CRM — что ключ этого
// пользователя отозван позавчера и почему. Без второго ответ
// «проверьте ключ» верен и бесполезен.
//
// Все инструменты принимают номер обращения, а не пользователя: по
// обращению сервер сам находит его автора. Найти произвольного
// пользователя по имени или почте нельзя — инструмента для этого нет.
// А сессия поддержки дополнительно ограничена своим обращением кодом
// (см. Scope): чужие данные модель не получит, даже если попросит.
package crm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/safronov-a1exander/advent/internal/mcp"
)

// Version — версия сервера в ответе на initialize.
const Version = "day-33"

// Key — ключ доступа к шлюзу. Самого ключа в CRM нет — только имя,
// под которым он записан в keys.txt, и его судьба.
type Key struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // active | revoked
	Issued  string `json:"issued"`
	Revoked string `json:"revoked,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// User — пользователь сервиса.
type User struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Email   string   `json:"email"`
	Devices []string `json:"devices,omitempty"`
	// RatePerMin — лимит на ключ; 0 — по умолчанию сервиса.
	RatePerMin int    `json:"rate_per_min,omitempty"`
	Keys       []Key  `json:"keys"`
	Note       string `json:"note,omitempty"`
}

// Message — реплика в обращении.
type Message struct {
	From string `json:"from"` // user | support | admin
	At   string `json:"at"`
	Text string `json:"text"`
}

// Ticket — обращение.
type Ticket struct {
	ID       string    `json:"id"`
	User     string    `json:"user"`
	Subject  string    `json:"subject"`
	Status   string    `json:"status"` // open | waiting | resolved | escalated
	Created  string    `json:"created"`
	Device   string    `json:"device,omitempty"`
	Messages []Message `json:"messages"`
}

// Request — строка журнала шлюза: кто, когда, что получил.
type Request struct {
	At   string `json:"at"`
	Key  string `json:"key"`
	Path string `json:"path"`
	Code int    `json:"code"`
	Note string `json:"note,omitempty"`
}

// Data — содержимое CRM.
type Data struct {
	Users    []User    `json:"users"`
	Tickets  []Ticket  `json:"tickets"`
	Requests []Request `json:"requests"`
}

// Store — CRM в JSON-файле. Seed — исходные данные из репозитория, State —
// рабочая копия в runs/: ответы и смена статусов пишутся туда, и демо
// можно повторить с чистого листа, удалив её.
type Store struct {
	State string
	now   func() time.Time
	mu    sync.Mutex
	d     Data
}

// Open читает рабочую копию, а если её нет — исходные данные.
func Open(seed, state string, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	path := state
	if _, err := os.Stat(state); errors.Is(err, os.ErrNotExist) {
		path = seed
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := &Store{State: state, now: now}
	if err := json.Unmarshal(b, &s.d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.State), 0o755); err != nil {
		return err
	}
	tmp := s.State + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.State)
}

func (s *Store) ticket(id string) (*Ticket, *User, error) {
	id = strings.ToUpper(strings.TrimSpace(id))
	for i := range s.d.Tickets {
		t := &s.d.Tickets[i]
		if t.ID != id {
			continue
		}
		for j := range s.d.Users {
			if s.d.Users[j].ID == t.User {
				return t, &s.d.Users[j], nil
			}
		}
		return t, nil, fmt.Errorf("у обращения %s нет пользователя %s в CRM", id, t.User)
	}
	return nil, nil, fmt.Errorf("обращения %s нет", id)
}

// Ticket — обращение с автором: для тех, кто зовёт из кода (карточка
// в системном промпте сессии, страница чата).
func (s *Store) Ticket(id string) (Ticket, User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, u, err := s.ticket(id)
	if err != nil {
		return Ticket{}, User{}, err
	}
	return *t, *u, nil
}

// Tickets — все обращения: список на странице поддержки.
func (s *Store) Tickets() []Ticket {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Ticket(nil), s.d.Tickets...)
}

// New — MCP-сервер поверх хранилища.
func New(s *Store) *mcp.Server {
	srv := mcp.NewServer("crm", Version,
		"CRM поддержки приватной модели: обращение, его автор, ключи и лимиты автора, журнал его запросов к шлюзу. "+
			"Всё — по номеру обращения: другого способа найти пользователя нет.")
	t, f := true, false
	ro := &mcp.Annotations{ReadOnlyHint: &t}
	rw := &mcp.Annotations{ReadOnlyHint: &f, DestructiveHint: &f}
	id := mcp.Prop{Name: "ticket_id", Type: "string", Required: true, Description: "номер обращения, например T-1042"}
	srv.Register(mcp.Tool{
		Name: "get_ticket", Title: "Обращение",
		Description: "Обращение целиком: тема, статус, устройство и переписка — и карточка его автора: имя, устройства, лимит, ключи с их статусом (действует, отозван — когда и почему).",
		InputSchema: mcp.Schema(id), Annotations: ro,
	}, s.getTicket)
	srv.Register(mcp.Tool{
		Name: "user_requests", Title: "Запросы автора к шлюзу",
		Description: "Последние запросы автора обращения к сервису из журнала шлюза: время, ключ, путь, код ответа (200, 401, 413, 429, 503…) и пояснение — плюс сводка по кодам. Показывает, что на самом деле происходило у пользователя.",
		InputSchema: mcp.Schema(id, mcp.Prop{Name: "limit", Type: "integer", Description: "сколько последних запросов, по умолчанию 20"}),
		Annotations: ro,
	}, s.userRequests)
	srv.Register(mcp.Tool{
		Name: "add_note", Title: "Заметка в обращение",
		Description: "Записать в обращение внутреннюю заметку поддержки для администратора: что выяснили и что нужно сделать. Пользователь её не видит. Ответ пользователю сюда не пишется — он идёт в чат.",
		InputSchema: mcp.Schema(id, mcp.Prop{Name: "text", Type: "string", Required: true, Description: "текст заметки"}),
		Annotations: rw,
	}, s.addNote)
	srv.Register(mcp.Tool{
		Name: "set_status", Title: "Статус обращения",
		Description: "Сменить статус обращения: waiting — ждём ответа пользователя, escalated — нужен администратор (выдать или вернуть ключ, поменять лимит, починить сервер), resolved — вопрос решён.",
		InputSchema: mcp.Schema(id, mcp.Prop{Name: "status", Type: "string", Required: true, Enum: []string{"waiting", "escalated", "resolved"}}),
		Annotations: rw,
	}, s.setStatus)
	return srv
}

type ticketCard struct {
	Ticket
	Author struct {
		Name       string   `json:"name"`
		Devices    []string `json:"devices,omitempty"`
		RatePerMin int      `json:"rate_per_min"`
		Keys       []Key    `json:"keys"`
		Note       string   `json:"note,omitempty"`
	} `json:"author"`
}

// DefaultRate — лимит шлюза на ключ, если у пользователя не задан свой.
const DefaultRate = 30

func (s *Store) getTicket(_ context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct {
		ID string `json:"ticket_id"`
	}](raw)
	if bad != nil {
		return *bad
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, u, err := s.ticket(a.ID)
	if err != nil {
		return mcp.ErrorResult("%v", err)
	}
	var c ticketCard
	c.Ticket = *t
	c.Author.Name, c.Author.Devices, c.Author.Keys, c.Author.Note = u.Name, u.Devices, u.Keys, u.Note
	c.Author.RatePerMin = u.RatePerMin
	if c.Author.RatePerMin == 0 {
		c.Author.RatePerMin = DefaultRate
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return mcp.TextResult(string(b))
}

func (s *Store) userRequests(_ context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct {
		ID    string `json:"ticket_id"`
		Limit int    `json:"limit"`
	}](raw)
	if bad != nil {
		return *bad
	}
	if a.Limit <= 0 {
		a.Limit = 20
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, u, err := s.ticket(a.ID)
	if err != nil {
		return mcp.ErrorResult("%v", err)
	}
	keys := map[string]bool{}
	for _, k := range u.Keys {
		keys[k.Name] = true
	}
	var mine []Request
	for _, r := range s.d.Requests {
		if keys[r.Key] {
			mine = append(mine, r)
		}
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].At < mine[j].At })
	if len(mine) > a.Limit {
		mine = mine[len(mine)-a.Limit:]
	}
	if len(mine) == 0 {
		return mcp.TextResult("запросов с ключей автора в журнале шлюза нет")
	}
	codes := map[int]int{}
	var b strings.Builder
	for _, r := range mine {
		codes[r.Code]++
		fmt.Fprintf(&b, "%s  %-16s %s  %d", r.At, r.Key, r.Path, r.Code)
		if r.Note != "" {
			b.WriteString("  " + r.Note)
		}
		b.WriteByte('\n')
	}
	var sum []string
	for c, n := range codes {
		sum = append(sum, fmt.Sprintf("%d×%d", c, n))
	}
	sort.Strings(sum)
	return mcp.TextResult(fmt.Sprintf("запросов: %d · коды: %s\n%s", len(mine), strings.Join(sum, ", "), b.String()))
}

func (s *Store) addNote(_ context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct {
		ID   string `json:"ticket_id"`
		Text string `json:"text"`
	}](raw)
	if bad != nil {
		return *bad
	}
	if strings.TrimSpace(a.Text) == "" {
		return mcp.ErrorResult("пустая заметка")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, _, err := s.ticket(a.ID)
	if err != nil {
		return mcp.ErrorResult("%v", err)
	}
	t.Messages = append(t.Messages, Message{From: "support-note", At: s.now().Format("2006-01-02 15:04"), Text: strings.TrimSpace(a.Text)})
	if err := s.save(); err != nil {
		return mcp.ErrorResult("не сохранилось: %v", err)
	}
	return mcp.TextResult("заметка добавлена в " + t.ID)
}

var statuses = map[string]bool{"waiting": true, "escalated": true, "resolved": true}

func (s *Store) setStatus(_ context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct {
		ID     string `json:"ticket_id"`
		Status string `json:"status"`
	}](raw)
	if bad != nil {
		return *bad
	}
	if !statuses[a.Status] {
		return mcp.ErrorResult("статус %q: можно waiting, escalated или resolved", a.Status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, _, err := s.ticket(a.ID)
	if err != nil {
		return mcp.ErrorResult("%v", err)
	}
	prev := t.Status
	t.Status = a.Status
	if err := s.save(); err != nil {
		return mcp.ErrorResult("не сохранилось: %v", err)
	}
	return mcp.TextResult(fmt.Sprintf("%s: %s → %s", t.ID, prev, a.Status))
}
