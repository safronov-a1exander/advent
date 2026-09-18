package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Хранилище задач (день 13).
//
// Состояние задачи обязано пережить закрытое приложение — иначе «пауза
// на любом этапе и продолжение без повторных объяснений» не работает.
// Формат и приёмы те же, что у разговоров дня 7 и памяти дня 11: JSON,
// файл на задачу, атомарная запись через временный файл.
//
// Задача и рабочий слой памяти (`task scope` дня 11) намеренно делят один
// идентификатор. Это одна и та же задача, просто с двух сторон: состояние —
// где мы в ней находимся, рабочая память — что мы в ней выяснили.

const storeVersion = 1

type file struct {
	Version int   `json:"version"`
	Task    *Task `json:"task"`
}

// Store — где лежат задачи.
type Store interface {
	Load(id string) (*Task, error)
	Save(t *Task) error
	List() ([]string, error)
	Path(id string) string
}

// FileStore — по JSON-файлу на задачу.
type FileStore struct {
	dir string
	mu  sync.Mutex
}

// NewFileStore — хранилище в каталоге dir.
func NewFileStore(dir string) *FileStore { return &FileStore{dir: dir} }

// Dir — каталог хранилища.
func (s *FileStore) Dir() string { return s.dir }

// Path — файл задачи.
func (s *FileStore) Path(id string) string {
	return filepath.Join(s.dir, safeName(id)+".json")
}

// safeName — id задачи, пригодный для имени файла: задачу называет человек.
func safeName(id string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(id)) {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 'а' && r <= 'я' || r == 'ё' || r == '-' || r == '_'
		if ok {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "без-имени"
	}
	return out
}

// Load читает задачу. Отсутствующего файла нет — значит, задача ещё
// не заводилась; это не ошибка, а «начинаем с чистого листа».
func (s *FileStore) Load(id string) (*Task, error) {
	if strings.TrimSpace(id) == "" {
		return nil, nil
	}
	b, err := os.ReadFile(s.Path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", s.Path(id), err)
	}
	if f.Version > storeVersion {
		return nil, fmt.Errorf("%s: формат версии %d новее, чем умеет эта сборка (%d)", s.Path(id), f.Version, storeVersion)
	}
	if f.Task != nil && !f.Task.State.Valid() {
		// Файл правили руками и написали стадию, которой нет. Молча
		// подставить planning значило бы потерять работу; честнее отказать.
		return nil, fmt.Errorf("%s: неизвестная стадия %q", s.Path(id), f.Task.State)
	}
	return f.Task, nil
}

// Save пишет задачу атомарно.
func (s *FileStore) Save(t *Task) error {
	if t == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(file{Version: storeVersion, Task: t}, "", "  ")
	if err != nil {
		return err
	}
	path := s.Path(t.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// List — какие задачи есть.
func (s *FileStore) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(out)
	return out, nil
}
