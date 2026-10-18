// Package review — AI-ревью изменений (день 32): пайплайн из лекции
// недели — триггер → вход → агент → выход.
//
//	PR (или ветка)  ──► diff и изменённые файлы
//	                ──► правила ревью и связанный код из базы знаний (RAG)
//	                ──► проверки кодом: gofmt, секреты, тесты
//	                ──► модель: баги, архитектура, рекомендации — JSON
//	                ──► сверка ответа кодом ──► markdown ──► комментарий в PR
//
// Нейросеть здесь — ненадёжное звено, и пайплайн устроен вокруг этого:
// таймаут, повтор с паузой, повтор с просьбой исправить JSON, запасная
// модель. Если не вышло ничего, ревью всё равно выходит: только с
// проверками кодом и явной пометкой «нужен человек». Пайплайн не должен
// ни падать молча, ни выдавать пустое «всё хорошо».
package review

import (
	"strconv"
	"strings"
)

// File — один изменённый файл из diff.
type File struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // added | modified | deleted | renamed
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	// Lines — номера строк новой версии, попавшие в изменения (добавленные
	// и контекст хунков): на них ревью может ссылаться.
	Lines []Range `json:"-"`
	// AddedText — добавленные строки подряд: по ним ищется связанный код.
	AddedText string `json:"-"`
}

// Range — отрезок строк [From, To] новой версии файла.
type Range struct{ From, To int }

// Touches — попадает ли строка line в изменения файла.
func (f File) Touches(line int) bool {
	for _, r := range f.Lines {
		if line >= r.From && line <= r.To {
			return true
		}
	}
	return false
}

// ParseDiff разбирает unified diff (`git diff`, `gh pr diff`) на файлы.
func ParseDiff(diff string) []File {
	var files []File
	var cur *File
	var added strings.Builder
	flush := func() {
		if cur != nil {
			cur.AddedText = added.String()
			files = append(files, *cur)
		}
		cur = nil
		added.Reset()
	}
	for _, line := range strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			cur = &File{Status: "modified"}
			// «diff --git a/x b/x» — путь берём из b/, уточняем по +++ ниже
			if i := strings.LastIndex(line, " b/"); i >= 0 {
				cur.Path = line[i+3:]
			}
		case cur == nil:
		case strings.HasPrefix(line, "new file mode"):
			cur.Status = "added"
		case strings.HasPrefix(line, "deleted file mode"):
			cur.Status = "deleted"
		case strings.HasPrefix(line, "rename to "):
			cur.Status, cur.Path = "renamed", strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "+++ "):
			if p := strings.TrimPrefix(line, "+++ "); p != "/dev/null" {
				cur.Path = strings.TrimPrefix(p, "b/")
			}
		case strings.HasPrefix(line, "--- "):
		case strings.HasPrefix(line, "@@"):
			from, n := hunkNew(line)
			if n > 0 {
				cur.Lines = append(cur.Lines, Range{from, from + n - 1})
			}
		case strings.HasPrefix(line, "+"):
			cur.Added++
			added.WriteString(line[1:])
			added.WriteByte('\n')
		case strings.HasPrefix(line, "-"):
			cur.Deleted++
		}
	}
	flush()
	return files
}

// hunkNew — начало и длина новой стороны из «@@ -a,b +c,d @@».
func hunkNew(h string) (from, n int) {
	i := strings.Index(h, " +")
	if i < 0 {
		return 0, 0
	}
	rest := h[i+2:]
	if j := strings.IndexByte(rest, ' '); j >= 0 {
		rest = rest[:j]
	}
	a, b, ok := strings.Cut(rest, ",")
	from, _ = strconv.Atoi(a)
	n = 1
	if ok {
		n, _ = strconv.Atoi(b)
	}
	return from, n
}

// Find — файл по пути; ok=false — такого файла в diff нет.
func Find(files []File, path string) (File, bool) {
	path = strings.TrimPrefix(strings.TrimSpace(strings.ReplaceAll(path, "\\", "/")), "./")
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return File{}, false
}
