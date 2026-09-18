package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Хранилище профилей (день 12).
//
// Профиль — конфиг, а не данные, поэтому лежит он не там, где память:
// каталог `profiles/`, YAML, файл на профиль. Лекция предлагала MD-файлы,
// и по сути это то же самое — текст, который человек правит руками и
// кладёт в репозиторий. YAML взят потому, что у профиля есть структура
// (стиль отдельно, ограничения отдельно, дороги отдельно), и в этом
// проекте так описаны и сценарии, и флот агентов.
//
// Читается профиль при каждом обращении, а не один раз при старте:
// правка файла должна действовать со следующего запроса, иначе
// «поправил профиль — перезапусти приложение» превращает персонализацию
// в то, чем никто не пользуется.

// Store — где лежат профили.
type Store interface {
	Load(id string) (*Profile, error)
	List() ([]string, error)
	Path(id string) string
}

// FileStore — каталог с YAML-файлами профилей.
type FileStore struct {
	dir string
	mu  sync.Mutex
	// cache — разобранные профили по mtime файла: читать YAML на каждый
	// запрос незачем, а замечать правку надо.
	cache map[string]cached
}

type cached struct {
	mtime int64
	size  int64
	prof  *Profile
}

// NewFileStore — хранилище в каталоге dir.
func NewFileStore(dir string) *FileStore {
	return &FileStore{dir: dir, cache: map[string]cached{}}
}

// Dir — каталог профилей.
func (s *FileStore) Dir() string { return s.dir }

// Path — файл профиля.
func (s *FileStore) Path(id string) string {
	return filepath.Join(s.dir, safeName(id)+".yaml")
}

// safeName — id, пригодный для имени файла.
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
	return strings.Trim(b.String(), "-")
}

// ErrNotFound — профиля с таким id нет.
var ErrNotFound = errors.New("профиль не найден")

// Load читает профиль. Пустой id — не ошибка, а «без профиля»: агент
// работает как на одиннадцатом дне.
func (s *FileStore) Load(id string) (*Profile, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	path := s.Path(id)
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if c, ok := s.cache[id]; ok && c.mtime == st.ModTime().UnixNano() && c.size == st.Size() {
		s.mu.Unlock()
		return c.prof, nil
	}
	s.mu.Unlock()

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	p.ID = id
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	s.mu.Lock()
	s.cache[id] = cached{mtime: st.ModTime().UnixNano(), size: st.Size(), prof: &p}
	s.mu.Unlock()
	return &p, nil
}

// List — какие профили есть.
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
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}
		out = append(out, strings.TrimSuffix(strings.TrimSuffix(name, ".yml"), ".yaml"))
	}
	sort.Strings(out)
	return out, nil
}

// Save пишет профиль в файл. Нужен команде `advent profile -init`:
// первый профиль удобнее получить готовым и поправить, чем сочинять
// структуру YAML по документации.
func (s *FileStore) Save(p *Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	path := s.Path(p.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	delete(s.cache, p.ID)
	return nil
}
