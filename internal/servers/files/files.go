// Package files — MCP-сервер «Файлы проекта» (день 34): ассистент сам
// читает, ищет и правит файлы, а не отвечает текстом о них.
//
// Правки не трогают рабочие файлы. Запись ложится в теневую копию
// (каталог stage): модель читает свои изменения, как будто они уже
// сделаны, а человек в конце видит diff и решает, применять ли. Это
// «опасные операции — только с подтверждением» из лекции недели,
// сделанное кодом: у модели нет инструмента, который изменил бы проект
// сам по себе, и нет инструмента удаления вовсе.
package files

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/safronov-a1exander/advent/internal/mcp"
)

// Version — версия сервера в ответе на initialize.
const Version = "day-34"

// MaxRead — сколько символов отдаёт одно чтение: модели нужен файл,
// а не мегабайт лога.
const MaxRead = 20000

// Project — корень проекта и каталог теневой копии.
type Project struct {
	Root  string
	Stage string
}

// closed — что ассистенту нельзя ни читать, ни писать: секреты и служебное.
var closed = []string{".git", ".env", "config.local.yaml", "keys.txt", "runs", "index", "recordings", "sessions", "bin"}

// Clean — путь внутри проекта или ошибка. Абсолютные пути, «..» и
// закрытые каталоги отклоняются.
func (p *Project) Clean(rel string) (string, error) {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" || rel == "." {
		return ".", nil
	}
	if path.IsAbs(rel) || filepath.IsAbs(rel) || strings.Contains(rel, ":") {
		return "", fmt.Errorf("путь %q: нужен путь внутри проекта, относительно его корня", rel)
	}
	c := path.Clean(rel)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("путь %q выходит за пределы проекта", rel)
	}
	first := strings.SplitN(c, "/", 2)[0]
	for _, x := range closed {
		if first == x || c == x {
			return "", fmt.Errorf("путь %q закрыт для ассистента", rel)
		}
	}
	return c, nil
}

// read — файл с учётом теневой копии: изменённый ассистентом файл
// читается в новой версии.
func (p *Project) read(rel string) ([]byte, bool, error) {
	if b, err := os.ReadFile(filepath.Join(p.Stage, rel)); err == nil {
		return b, true, nil
	}
	b, err := os.ReadFile(filepath.Join(p.Root, rel))
	return b, false, err
}

// Staged — изменённые файлы теневой копии, пути относительно корня.
func (p *Project) Staged() ([]string, error) {
	var out []string
	err := filepath.WalkDir(p.Stage, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(p.Stage, fp)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// New — сервер над проектом.
func New(p *Project) *mcp.Server {
	s := mcp.NewServer("files", Version,
		"Файлы проекта: список, чтение, поиск по нескольким файлам, создание и правка. "+
			"Правки ложатся в черновик изменений и попадают в проект только после подтверждения человеком.")
	t, f := true, false
	ro := &mcp.Annotations{ReadOnlyHint: &t}
	rw := &mcp.Annotations{ReadOnlyHint: &f, DestructiveHint: &f}
	s.Register(mcp.Tool{
		Name: "list_files", Title: "Файлы",
		Description: "Файлы проекта по каталогу или шаблону: «docs/days», «*.md», «internal/**/*.go». С отметкой, если файл изменён в черновике.",
		InputSchema: mcp.Schema(mcp.Prop{Name: "pattern", Type: "string", Description: "каталог или шаблон; пусто — корень проекта"}),
		Annotations: ro,
	}, p.list)
	s.Register(mcp.Tool{
		Name: "read_file", Title: "Прочитать файл",
		Description: "Содержимое файла с номерами строк — в версии черновика, если файл уже правился. Можно читать кусок: from и to — номера строк.",
		InputSchema: mcp.Schema(
			mcp.Prop{Name: "path", Type: "string", Required: true, Description: "путь от корня проекта"},
			mcp.Prop{Name: "from", Type: "integer", Description: "с какой строки"},
			mcp.Prop{Name: "to", Type: "integer", Description: "по какую строку"},
		),
		Annotations: ro,
	}, p.readFile)
	s.Register(mcp.Tool{
		Name: "search", Title: "Поиск по файлам",
		Description: "Найти строки по регулярному выражению (Go RE2) во многих файлах сразу: путь, номер строки, строка. Например, где вызывается функция или где упоминается команда.",
		InputSchema: mcp.Schema(
			mcp.Prop{Name: "regex", Type: "string", Required: true, Description: "регулярное выражение, например «knowledgeFor\\(» или «^# День»"},
			mcp.Prop{Name: "pattern", Type: "string", Description: "в каких файлах: каталог или шаблон; пусто — во всех текстовых"},
			mcp.Prop{Name: "limit", Type: "integer", Description: "сколько совпадений вернуть, по умолчанию 100"},
		),
		Annotations: ro,
	}, p.search)
	s.Register(mcp.Tool{
		Name: "write_file", Title: "Записать файл",
		Description: "Создать файл или заменить его содержимое целиком. Пишется в черновик изменений: в проект попадёт после подтверждения человеком.",
		InputSchema: mcp.Schema(
			mcp.Prop{Name: "path", Type: "string", Required: true, Description: "путь от корня проекта"},
			mcp.Prop{Name: "content", Type: "string", Required: true, Description: "новое содержимое файла целиком"},
		),
		Annotations: rw,
	}, p.writeFile)
	s.Register(mcp.Tool{
		Name: "edit_file", Title: "Правка в файле",
		Description: "Заменить в файле фрагмент old на new. old должен встречаться в файле ровно один раз — скопируй его из read_file дословно, без номеров строк. Пишется в черновик изменений.",
		InputSchema: mcp.Schema(
			mcp.Prop{Name: "path", Type: "string", Required: true, Description: "путь от корня проекта"},
			mcp.Prop{Name: "old", Type: "string", Required: true, Description: "что заменить — дословно, ровно одно вхождение"},
			mcp.Prop{Name: "new", Type: "string", Required: true, Description: "на что заменить"},
		),
		Annotations: rw,
	}, p.editFile)
	return s
}

// walk — файлы проекта под шаблоном, без закрытых каталогов и бинарей.
func (p *Project) walk(pattern string) ([]string, error) {
	want := strings.Trim(strings.ReplaceAll(strings.TrimSpace(pattern), "\\", "/"), "/")
	if want != "" && !strings.ContainsAny(want, "*?[") {
		if _, err := p.Clean(want); err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	var out []string
	add := func(rel string) {
		if !seen[rel] && (want == "" || match(want, rel)) {
			seen[rel] = true
			out = append(out, rel)
		}
	}
	for _, base := range []string{p.Root, p.Stage} {
		filepath.WalkDir(base, func(fp string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(base, fp)
			rel = filepath.ToSlash(rel)
			if rel == "." {
				return nil
			}
			if d.IsDir() {
				if _, err := p.Clean(rel); err != nil || strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if _, err := p.Clean(rel); err == nil && textFile(rel) {
				add(rel)
			}
			return nil
		})
		if p.Stage == "" || p.Stage == p.Root {
			break
		}
	}
	sort.Strings(out)
	return out, nil
}

var textExt = map[string]bool{".go": true, ".md": true, ".yaml": true, ".yml": true, ".json": true, ".txt": true,
	".ps1": true, ".demo": true, ".html": true, ".mod": true, ".sum": false, ".ini": true}

func textFile(rel string) bool {
	if strings.HasPrefix(path.Base(rel), ".") {
		return path.Base(rel) == ".gitignore"
	}
	return textExt[strings.ToLower(path.Ext(rel))]
}

func match(want, rel string) bool {
	if !strings.ContainsAny(want, "*?[") {
		return rel == want || strings.HasPrefix(rel, want+"/")
	}
	if !strings.Contains(want, "/") {
		ok, _ := path.Match(want, path.Base(rel))
		return ok
	}
	return matchSegs(strings.Split(want, "/"), strings.Split(rel, "/"))
}

func matchSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(segs); i++ {
				if matchSegs(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], segs[0]); !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

func (p *Project) list(_ context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct {
		Pattern string `json:"pattern"`
	}](raw)
	if bad != nil {
		return *bad
	}
	files, err := p.walk(a.Pattern)
	if err != nil {
		return mcp.ErrorResult("%v", err)
	}
	if len(files) == 0 {
		return mcp.TextResult("под «" + a.Pattern + "» файлов нет")
	}
	staged := map[string]bool{}
	if st, err := p.Staged(); err == nil {
		for _, s := range st {
			staged[s] = true
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "файлов: %d\n", len(files))
	for i, f := range files {
		if i >= 300 {
			fmt.Fprintf(&b, "… и ещё %d — сузь шаблон\n", len(files)-i)
			break
		}
		b.WriteString(f)
		if staged[f] {
			b.WriteString("  [изменён в черновике]")
		}
		b.WriteByte('\n')
	}
	return mcp.TextResult(b.String())
}

func (p *Project) readFile(_ context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct {
		Path     string `json:"path"`
		From, To int
	}](raw)
	if bad != nil {
		return *bad
	}
	rel, err := p.Clean(a.Path)
	if err != nil {
		return mcp.ErrorResult("%v", err)
	}
	b, staged, err := p.read(rel)
	if err != nil {
		return mcp.ErrorResult("не прочитать %s: %v", rel, errors.Unwrap(err))
	}
	if !utf8.Valid(b) {
		return mcp.ErrorResult("%s — не текстовый файл", rel)
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	from, to := 1, len(lines)
	if a.From > 0 {
		from = a.From
	}
	if a.To > 0 && a.To < to {
		to = a.To
	}
	var out strings.Builder
	mark := ""
	if staged {
		mark = " (версия из черновика)"
	}
	fmt.Fprintf(&out, "%s%s · строк %d\n", rel, mark, len(lines))
	for i := from; i <= to && i <= len(lines); i++ {
		if out.Len() > MaxRead {
			fmt.Fprintf(&out, "… обрезано на строке %d — читай дальше с from=%d\n", i, i)
			break
		}
		fmt.Fprintf(&out, "%5d│%s\n", i, lines[i-1])
	}
	return mcp.TextResult(out.String())
}

func (p *Project) search(_ context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct {
		Regex   string `json:"regex"`
		Pattern string `json:"pattern"`
		Limit   int    `json:"limit"`
	}](raw)
	if bad != nil {
		return *bad
	}
	re, err := regexp.Compile(a.Regex)
	if err != nil {
		return mcp.ErrorResult("регулярное выражение: %v", err)
	}
	if a.Limit <= 0 {
		a.Limit = 100
	}
	files, err := p.walk(a.Pattern)
	if err != nil {
		return mcp.ErrorResult("%v", err)
	}
	var out strings.Builder
	hits, inFiles := 0, 0
	for _, f := range files {
		b, _, err := p.read(f)
		if err != nil || !utf8.Valid(b) {
			continue
		}
		found := false
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			if !re.MatchString(line) {
				continue
			}
			if !found {
				inFiles++
				found = true
			}
			hits++
			if hits <= a.Limit {
				fmt.Fprintf(&out, "%s:%d: %s\n", f, n, strings.TrimSpace(clipRunes(line, 200)))
			}
		}
	}
	if hits == 0 {
		return mcp.TextResult(fmt.Sprintf("совпадений нет (файлов просмотрено: %d)", len(files)))
	}
	head := fmt.Sprintf("совпадений %d в %d файлах (просмотрено %d)\n", hits, inFiles, len(files))
	if hits > a.Limit {
		head += fmt.Sprintf("показаны первые %d — сузь регулярку или шаблон\n", a.Limit)
	}
	return mcp.TextResult(head + out.String())
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func (p *Project) stage(rel, content string) error {
	dst := filepath.Join(p.Stage, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(content), 0o644)
}

func (p *Project) writeFile(_ context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}](raw)
	if bad != nil {
		return *bad
	}
	rel, err := p.Clean(a.Path)
	if err != nil || rel == "." {
		return mcp.ErrorResult("путь %q: %v", a.Path, err)
	}
	if !textFile(rel) {
		return mcp.ErrorResult("%s: писать можно только текстовые файлы проекта (.md, .go, .yaml…)", rel)
	}
	_, _, existed := p.read(rel)
	if err := p.stage(rel, a.Content); err != nil {
		return mcp.ErrorResult("%v", err)
	}
	verb := "создан"
	if existed == nil {
		verb = "перезаписан"
	}
	return mcp.TextResult(fmt.Sprintf("%s %s в черновике · строк %d", rel, verb, strings.Count(a.Content, "\n")+1))
}

func (p *Project) editFile(_ context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct {
		Path string `json:"path"`
		Old  string `json:"old"`
		New  string `json:"new"`
	}](raw)
	if bad != nil {
		return *bad
	}
	rel, err := p.Clean(a.Path)
	if err != nil {
		return mcp.ErrorResult("%v", err)
	}
	b, _, err := p.read(rel)
	if err != nil {
		return mcp.ErrorResult("не прочитать %s — создай его через write_file", rel)
	}
	src := strings.ReplaceAll(string(b), "\r\n", "\n")
	old := strings.ReplaceAll(a.Old, "\r\n", "\n")
	switch n := strings.Count(src, old); {
	case old == "":
		return mcp.ErrorResult("old пустой — для нового файла есть write_file")
	case n == 0:
		return mcp.ErrorResult("в %s нет такого фрагмента — перечитай файл и скопируй old дословно, без номеров строк", rel)
	case n > 1:
		return mcp.ErrorResult("фрагмент встречается в %s %d раз — возьми в old больше строк вокруг, чтобы он стал единственным", rel, n)
	}
	if err := p.stage(rel, strings.Replace(src, old, a.New, 1)); err != nil {
		return mcp.ErrorResult("%v", err)
	}
	return mcp.TextResult(fmt.Sprintf("%s: заменено в черновике (−%d +%d строк)", rel, strings.Count(old, "\n")+1, strings.Count(a.New, "\n")+1))
}
