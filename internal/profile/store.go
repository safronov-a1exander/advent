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
// каталог `profiles/`, markdown, файл на профиль. Тело файла — проза
// о собеседнике, необязательный frontmatter — дороги.
//
// Первая версия была YAML со схемой из семи полей под ту же прозу.
// Схема ничего не проверяла, читалась в одном месте и там же склеивалась
// обратно в строки, зато требовала кавычек вокруг каждой фразы
// с двоеточием. Лекция, кстати, с самого начала говорила про MD-файлы —
// и `CLAUDE.md` с `.cursor/rules/*.mdc` устроены именно так.
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
	return filepath.Join(s.dir, safeName(id)+".md")
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

	body, front, err := readMarkdown(path)
	if err != nil {
		return nil, err
	}
	var p Profile
	if len(front) > 0 {
		if err := yaml.Unmarshal(front, &p); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	p.ID = id
	p.About = body
	if p.Name == "" {
		p.Name = id
	}
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
		if e.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		out = append(out, strings.TrimSuffix(name, ".md"))
	}
	sort.Strings(out)
	return out, nil
}

// readMarkdown делит файл на frontmatter и тело. Формат тот же, что у всех:
// три дефиса, YAML, три дефиса, дальше текст. Файла без frontmatter это
// не касается — он весь целиком тело, и это нормальный профиль.
func readMarkdown(path string) (body string, front []byte, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return strings.TrimSpace(text), nil, nil
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", nil, fmt.Errorf("%s: frontmatter открыт, но не закрыт строкой ---", path)
	}
	tail := rest[end+len("\n---"):]
	if i := strings.Index(tail, "\n"); i >= 0 {
		tail = tail[i+1:]
	} else {
		tail = ""
	}
	return strings.TrimSpace(tail), []byte(rest[:end]), nil
}
