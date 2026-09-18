package memory

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
)

// Хранилище слоёв памяти (день 11).
//
// Слои с разным временем жизни лежат в разных местах, и это единственная
// причина, по которой хранилище вообще есть: chat scope хранить негде и
// незачем, он умирает с разговором, а task и user переживают его — и
// продолжить задачу завтра можно только если они на диске.
//
// Формат — тот же, что у разговоров дня 7: JSON, файл на сущность.
// Курс разрешал что угодно — «можно сделать как sql таблицу, можно файликами
// складывать», — и файлы выигрывают ровно тем же, чем на седьмом дне: память
// агента можно открыть глазами, поправить руками и приложить к отчёту.

// storeVersion — версия формата файла слоя.
const storeVersion = 1

// Store — где лежат хранимые слои.
type Store interface {
	Load(scope Scope, id string) ([]Entry, error)
	Save(scope Scope, id string, entries []Entry) error
	// List — какие идентификаторы есть в слое (какие задачи, какие
	// пользователи). Нужен команде просмотра памяти.
	List(scope Scope) ([]string, error)
	// Path — где лежит слой; для подсказок в интерфейсе.
	Path(scope Scope, id string) string
}

// file — как слой лежит на диске.
type file struct {
	Version int       `json:"version"`
	Scope   Scope     `json:"scope"`
	ID      string    `json:"id"`
	Updated time.Time `json:"updated"`
	Entries []Entry   `json:"entries"`
}

// FileStore — каталог с подкаталогом на слой: users/<id>.json, tasks/<id>.json.
type FileStore struct {
	dir string
	mu  sync.Mutex
}

// NewFileStore — хранилище в каталоге dir; подкаталоги создаются при записи.
func NewFileStore(dir string) *FileStore { return &FileStore{dir: dir} }

// Dir — корень хранилища.
func (s *FileStore) Dir() string { return s.dir }

// subdir — подкаталог слоя. Имена во множественном числе: в каталоге лежит
// по файлу на пользователя и по файлу на задачу.
func subdir(scope Scope) (string, error) {
	switch scope {
	case ScopeUser:
		return "users", nil
	case ScopeTask:
		return "tasks", nil
	case ScopeChat:
		// Краткосрочный слой не хранится отдельно: он часть разговора и
		// уезжает на диск вместе с ним (session.go дня 7).
		return "", fmt.Errorf("слой %s не хранится отдельно: он живёт вместе с разговором", scope)
	}
	return "", fmt.Errorf("неизвестный слой памяти %q", scope)
}

// Path — файл слоя.
func (s *FileStore) Path(scope Scope, id string) string {
	sub, err := subdir(scope)
	if err != nil {
		return ""
	}
	return filepath.Join(s.dir, sub, safeName(id)+".json")
}

// safeName — идентификатор, пригодный для имени файла. Пользователя и задачу
// называет человек («Саша», «бот для барбершопа»), и слэш в таком имени
// не должен уводить запись в чужой каталог.
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

// Load читает слой. Отсутствующий файл — пустая память, а не ошибка.
func (s *FileStore) Load(scope Scope, id string) ([]Entry, error) {
	path := s.Path(scope, id)
	if path == "" {
		return nil, fmt.Errorf("слой %q не хранится в файлах", scope)
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Version > storeVersion {
		return nil, fmt.Errorf("%s: формат версии %d новее, чем умеет эта сборка (%d)", path, f.Version, storeVersion)
	}
	return f.Entries, nil
}

// Save пишет слой атомарно — как разговоры дня 7: сначала во временный файл,
// потом переименование, чтобы падение посреди записи не оставило огрызок.
func (s *FileStore) Save(scope Scope, id string, entries []Entry) error {
	path := s.Path(scope, id)
	if path == "" {
		return fmt.Errorf("слой %q не хранится в файлах", scope)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(file{
		Version: storeVersion,
		Scope:   scope,
		ID:      id,
		Updated: time.Now(),
		Entries: entries,
	}, "", "  ")
	if err != nil {
		return err
	}
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

// List — идентификаторы, для которых в слое что-то лежит.
func (s *FileStore) List(scope Scope) ([]string, error) {
	sub, err := subdir(scope)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.dir, sub))
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
