package invariant

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

// Хранилище наборов инвариантов (день 14).
//
// «Инварианты должны храниться отдельно от диалога» — требование задания,
// и здесь оно выполняется буквально: набор — это каталог, правило — файл.
//
//	invariants/
//	  барбершоп/
//	    _набор.md      необязательно: имя набора и сколько раз переспрашивать
//	    стек.md
//	    база.md
//	    платежи.md
//
// Формат правила — markdown; тело файла и есть правило:
//
//	Бэкенд только на Go. Python и Node.js предлагать нельзя ни в каком виде.
//
// Необязательный frontmatter добавляет к нему стадии и уточнение для
// проверяющей модели:
//
//	---
//	stages: [planning]
//	ask: Считается ли нарушением код внутри цитаты из чужого сообщения? Нет.
//	---
//
// Markdown, а не YAML, потому что формулировка здесь единственное
// содержимое: её читает модель в промпте и пользователь в объяснении
// отказа. В YAML она была бы строкой в кавычках, которую надо
// экранировать из-за каждого двоеточия, — а править её будут чаще всего
// остального.
//
// Так же устроены и настоящие инструменты: `CLAUDE.md`, `.cursor/rules/*.mdc`.

// Set — набор правил.
type Set struct {
	ID   string
	Name string
	// About — зачем этот набор; в отчёты и подсказки.
	About string
	// Invariants — правила в алфавитном порядке имён файлов. Порядок
	// стабильный, потому что от него зависит текст промпта, а от текста
	// промпта — кэш префикса.
	Invariants []Invariant
	// Retries — сколько раз переспрашивать модель при нарушении.
	// 0 означает значение по умолчанию (1).
	Retries int
}

// setMeta — frontmatter файла `_набор.md`.
type setMeta struct {
	Name    string `yaml:"name"`
	Retries int    `yaml:"retries"`
}

// RetriesN — сколько повторов делать с учётом умолчания.
func (s *Set) RetriesN() int {
	if s == nil {
		return 0
	}
	if s.Retries <= 0 {
		return 1
	}
	return s.Retries
}

// List — правила набора или nil.
func (s *Set) List() []Invariant {
	if s == nil {
		return nil
	}
	return s.Invariants
}

// Summary — однострочная подпись для шапки и отчётов.
func (s *Set) Summary() string {
	if s == nil || len(s.Invariants) == 0 {
		return ""
	}
	name := s.Name
	if name == "" {
		name = s.ID
	}
	return fmt.Sprintf("%s · правил %d", name, len(s.Invariants))
}

// Validate — набор проверяем.
func (s *Set) Validate() error {
	if len(s.Invariants) == 0 {
		return fmt.Errorf("набор %q пустой: нечего проверять", s.ID)
	}
	seen := map[string]bool{}
	for _, inv := range s.Invariants {
		if err := inv.Validate(); err != nil {
			return fmt.Errorf("набор %q: %w", s.ID, err)
		}
		key := strings.ToLower(inv.Name)
		if seen[key] {
			// Имена уходят в объяснение отказа и в ответ проверяющей модели;
			// два правила с одним именем сделали бы отчёт неразличимым.
			return fmt.Errorf("набор %q: два правила с именем %q", s.ID, inv.Name)
		}
		seen[key] = true
	}
	return nil
}

// Store — где лежат наборы.
type Store interface {
	Load(id string) (*Set, error)
	List() ([]string, error)
	Path(id string) string
}

// FileStore — каталог с наборами-подкаталогами.
type FileStore struct {
	dir   string
	mu    sync.Mutex
	cache map[string]cached
}

// cached — разобранный набор и отпечаток его файлов. Правила правят чаще
// всего остального — обычно сразу после того, как модель что-то нарушила, —
// поэтому правка должна действовать со следующего запроса.
type cached struct {
	stamp string
	set   *Set
}

// NewFileStore — хранилище в каталоге dir.
func NewFileStore(dir string) *FileStore {
	return &FileStore{dir: dir, cache: map[string]cached{}}
}

// Dir — каталог наборов.
func (s *FileStore) Dir() string { return s.dir }

// Path — каталог набора.
func (s *FileStore) Path(id string) string {
	return filepath.Join(s.dir, safeName(id))
}

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

// ErrNotFound — набора с таким id нет.
var ErrNotFound = errors.New("набор инвариантов не найден")

// metaFile — имя файла с настройками набора.
const metaFile = "_набор.md"

// Load читает набор; пустой id — «без инвариантов».
func (s *FileStore) Load(id string) (*Set, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	dir := s.Path(id)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, dir)
	}
	if err != nil {
		return nil, err
	}

	// Отпечаток — имена, размеры и времена всех файлов набора. Правило
	// добавляют и удаляют так же часто, как правят, поэтому времени одного
	// файла мало.
	var stamp strings.Builder
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&stamp, "%s:%d:%d;", e.Name(), info.Size(), info.ModTime().UnixNano())
		files = append(files, e.Name())
	}
	sort.Strings(files)

	s.mu.Lock()
	if c, ok := s.cache[id]; ok && c.stamp == stamp.String() {
		s.mu.Unlock()
		return c.set, nil
	}
	s.mu.Unlock()

	set := &Set{ID: id, Name: id}
	for _, name := range files {
		body, front, err := readMarkdown(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if name == metaFile {
			var meta setMeta
			if len(front) > 0 {
				if err := yaml.Unmarshal(front, &meta); err != nil {
					return nil, fmt.Errorf("%s/%s: %w", dir, name, err)
				}
			}
			if meta.Name != "" {
				set.Name = meta.Name
			}
			set.Retries = meta.Retries
			set.About = body
			continue
		}
		var inv Invariant
		if len(front) > 0 {
			if err := yaml.Unmarshal(front, &inv); err != nil {
				return nil, fmt.Errorf("%s/%s: %w", dir, name, err)
			}
		}
		inv.Rule = body
		if inv.Name == "" {
			inv.Name = strings.TrimSuffix(name, ".md")
		}
		set.Invariants = append(set.Invariants, inv)
	}
	if err := set.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}

	s.mu.Lock()
	s.cache[id] = cached{stamp: stamp.String(), set: set}
	s.mu.Unlock()
	return set, nil
}

// readMarkdown делит файл на frontmatter и тело.
//
// Формат тот же, что у всех: три дефиса, YAML, три дефиса, дальше текст.
// Файла без frontmatter это не касается — он весь целиком тело, и это
// самый обычный случай.
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
	head := rest[:end]
	tail := rest[end+len("\n---"):]
	if i := strings.Index(tail, "\n"); i >= 0 {
		tail = tail[i+1:]
	} else {
		tail = ""
	}
	return strings.TrimSpace(tail), []byte(head), nil
}

// List — какие наборы есть.
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
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}
