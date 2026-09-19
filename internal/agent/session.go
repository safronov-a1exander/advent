package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/memory"
)

// Сохранение разговоров между запусками (день 7).
//
// Агент живёт в памяти процесса: закрыл приложение — разговор пропал.
// Хранилище пишет каждого агента в отдельный файл после каждого изменения,
// а при старте пул поднимает всех обратно. Для пользователя это как в любом
// чат-клиенте: закрыл, открыл — разговоры на месте и продолжаются с того же
// места, потому что модели уходит та же история, что и до перезапуска.
//
// Формат — JSON, по файлу на агента. Разговор можно открыть глазами,
// поправить руками или приложить к отчёту; SQLite на одного пользователя
// с десятками разговоров ничего не добавил бы, кроме зависимости.

// snapshotVersion — версия формата файла. Меняется, только если старые
// файлы перестанут читаться как есть.
const snapshotVersion = 1

// Snapshot — всё, что нужно, чтобы поднять агента после перезапуска.
type Snapshot struct {
	Version  int           `json:"version"`
	ID       string        `json:"id"`
	Provider string        `json:"provider,omitempty"`
	Created  time.Time     `json:"created"`
	Updated  time.Time     `json:"updated"`
	Rev      uint64        `json:"rev"`
	Config   Config        `json:"config"`
	History  []llm.Message `json:"history"`
	Stats    Stats         `json:"stats"`
	// Turns и Calibration появились на восьмом дне. Поля добавочные:
	// старые файлы читаются как есть, просто без учёта по ходам.
	Turns       []Turn  `json:"turns,omitempty"`
	Calibration float64 `json:"calibration,omitempty"`
	// Summary — сводка старой части разговора (день 9). История при этом
	// лежит в History целиком: сводка — про контекст, а не про память.
	Summary *summaryState `json:"summary,omitempty"`
	// Facts — блок фактов sticky facts (день 10).
	Facts []Fact `json:"facts,omitempty"`
	// ChatMemory — краткосрочный слой памяти (день 11). Рабочий и
	// долговременный слои сюда не попадают: они принадлежат задаче и
	// пользователю и лежат в своих файлах, иначе два разговора об одной
	// задаче хранили бы две расходящиеся копии одного и того же.
	ChatMemory []memory.Entry `json:"chat_memory,omitempty"`

	// Ветки (день 10): History, Turns, Summary и Facts выше — активная ветка,
	// остальные лежат в Parked.
	Branch      string            `json:"branch,omitempty"`
	Parked      map[string]thread `json:"parked,omitempty"`
	Checkpoints []checkpoint      `json:"checkpoints,omitempty"`
	BranchOrder []string          `json:"branch_order,omitempty"`
	BranchFrom  map[string]string `json:"branch_from,omitempty"`
}

// Store — куда пул сохраняет агентов.
type Store interface {
	Save(Snapshot) error
	// LoadAll возвращает всё, что удалось прочитать. Битые файлы не мешают
	// поднять остальные: они перечислены в ошибке.
	LoadAll() ([]Snapshot, error)
	// Archive убирает разговор из активных, не уничтожая его.
	Archive(id string) error
	// Path — где лежит разговор; для подсказок в интерфейсе.
	Path(id string) string
}

// snapshot снимает копию состояния агента под замком.
func (a *Agent) snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Snapshot{
		Version:  snapshotVersion,
		ID:       a.id,
		Provider: a.provider,
		Created:  a.created,
		Updated:  a.updated,
		Rev:      a.rev,
		Config:   a.cfg.Clone(),
		History:  append([]llm.Message(nil), a.history...),
		Stats:    a.stats,

		Turns:       append([]Turn(nil), a.turns...),
		Calibration: a.calib,
		Summary:     summaryPtr(a.summary),
		Facts:       append([]Fact(nil), a.facts...),
		ChatMemory:  a.mem.Layer(memory.ScopeChat).Entries(),

		Branch:      a.branch,
		Parked:      cloneThreads(a.parked),
		Checkpoints: cloneCheckpoints(a.checkpoints),
		BranchOrder: append([]string(nil), a.branchOrder...),
		BranchFrom:  cloneStrings(a.branchFrom),
	}
}

func cloneThreads(m map[string]thread) map[string]thread {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]thread, len(m))
	for k, v := range m {
		out[k] = v.clone()
	}
	return out
}

func cloneCheckpoints(cs []checkpoint) []checkpoint {
	out := make([]checkpoint, len(cs))
	for i, c := range cs {
		out[i] = c
		out[i].State = c.State.clone()
	}
	return out
}

func cloneStrings(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func summaryPtr(s summaryState) *summaryState {
	if s.Text == "" {
		return nil
	}
	return &s
}

// FileStore — по JSON-файлу на агента в одном каталоге.
type FileStore struct {
	dir string

	mu sync.Mutex
	// written — последняя записанная ревизия по id. Сохранения идут из
	// разных горутин (ответ агента, правка настроек в интерфейсе), и более
	// старый снимок может прийти позже нового — его надо просто пропустить.
	written map[string]uint64
}

// NewFileStore — хранилище в каталоге dir; каталог создаётся при первой записи.
func NewFileStore(dir string) *FileStore {
	return &FileStore{dir: dir, written: map[string]uint64{}}
}

// Dir — каталог хранилища.
func (s *FileStore) Dir() string { return s.dir }

// Path — файл разговора.
func (s *FileStore) Path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// Save пишет снимок атомарно: сначала во временный файл, потом переименование.
// Если процесс упадёт посреди записи, на диске останется прежняя версия
// разговора, а не обрезанный JSON.
func (s *FileStore) Save(snap Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.written[snap.ID]; ok && snap.Rev <= last {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	path := s.Path(snap.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	s.written[snap.ID] = snap.Rev
	return nil
}

// LoadAll читает все разговоры каталога в порядке создания.
func (s *FileStore) LoadAll() ([]Snapshot, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var (
		out  []Snapshot
		errs []error
	)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		snap, err := readSnapshot(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, snap)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })

	s.mu.Lock()
	for _, snap := range out {
		s.written[snap.ID] = snap.Rev
	}
	s.mu.Unlock()
	return out, errors.Join(errs...)
}

func readSnapshot(path string) (Snapshot, error) {
	var snap Snapshot
	b, err := os.ReadFile(path)
	if err != nil {
		return snap, err
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		return snap, fmt.Errorf("%s: %w", path, err)
	}
	switch {
	case snap.Version > snapshotVersion:
		return snap, fmt.Errorf("%s: формат версии %d новее, чем умеет эта сборка (%d)", path, snap.Version, snapshotVersion)
	case snap.ID == "":
		return snap, fmt.Errorf("%s: нет id", path)
	case snap.ID+".json" != filepath.Base(path):
		// id внутри и имя файла разошлись — вероятно, файл скопировали руками.
		// Доверяем имени файла: по нему разговор и будут искать.
		snap.ID = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	return snap, nil
}

// Archive переносит разговор в подкаталог archive. Закрыть разговор в
// интерфейсе — не значит уничтожить: файл остаётся, его можно вернуть руками.
func (s *FileStore) Archive(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.Path(id)
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		delete(s.written, id)
		return nil // агент ни разу не сохранялся — архивировать нечего
	}
	dst := filepath.Join(s.dir, "archive", fmt.Sprintf("%s-%s.json", id, time.Now().Format("20060102-150405")))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	delete(s.written, id)
	return nil
}
