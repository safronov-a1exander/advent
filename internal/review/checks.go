package review

import (
	"bytes"
	"context"
	"fmt"
	"go/format"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Check — детерминированная проверка: то, что можно сказать о PR без
// модели и что модель не должна иметь возможности «не заметить».
type Check struct {
	Name    string   `json:"name"`
	OK      bool     `json:"ok"`
	Skipped bool     `json:"skipped,omitempty"`
	Details []string `json:"details,omitempty"`
}

// Status — коротко для лога и отчёта.
func (c Check) Status() string {
	switch {
	case c.Skipped:
		return "пропущена — " + strings.Join(c.Details, "; ")
	case c.OK:
		return "ok"
	}
	return fmt.Sprintf("%d замечаний", len(c.Details))
}

// Reader — содержимое файла в ревьюируемой версии (на SHA головы PR).
type Reader func(ctx context.Context, path string) ([]byte, error)

// DefaultChecks — проверки кодом для этого проекта.
func DefaultChecks(read Reader) func(ctx context.Context, in Input) []Check {
	return func(ctx context.Context, in Input) []Check {
		return []Check{
			gofmtCheck(ctx, read, in.Files),
			secretsCheck(in.Files),
			testsCheck(in.Files),
			dayNoteCheck(in.Files),
		}
	}
}

func gofmtCheck(ctx context.Context, read Reader, files []File) Check {
	c := Check{Name: "gofmt", OK: true}
	if read == nil {
		return Check{Name: "gofmt", Skipped: true, Details: []string{"нет доступа к файлам версии"}}
	}
	for _, f := range files {
		if f.Status == "deleted" || !strings.HasSuffix(f.Path, ".go") {
			continue
		}
		src, err := read(ctx, f.Path)
		if err != nil {
			c.Details = append(c.Details, f.Path+": не прочитать — "+err.Error())
			c.OK = false
			continue
		}
		out, err := format.Source(src)
		switch {
		case err != nil:
			c.Details = append(c.Details, f.Path+": не разбирается как Go — "+firstLine(err.Error()))
			c.OK = false
		case !bytes.Equal(out, src):
			c.Details = append(c.Details, f.Path+": не отформатирован (gofmt -w)")
			c.OK = false
		}
	}
	return c
}

// envRef — строка, где значение берётся из окружения: «api_key_env: X»
// или «${X}». Пропускать по одному «_env» в строке было слишком широко —
// так прятался бы любой ключ рядом со словом env (замечание собственного
// ревью).
var envRef = regexp.MustCompile(`(?i)^\s*[a-z_]*_env\s*:\s*[A-Z0-9_]+\s*$|\$\{[A-Z0-9_]+\}`)

var secretRE = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_\-]{20,}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{30,}`),
	regexp.MustCompile(`(?i)(api_key|apikey|secret|token|password)\s*[:=]\s*["']?[A-Za-z0-9_\-]{16,}`),
}

// secretsCheck — ключи в добавленных строках. Ссылки на переменные
// окружения (api_key_env: X) — не ключи.
func secretsCheck(files []File) Check {
	c := Check{Name: "секреты", OK: true}
	for _, f := range files {
		for i, line := range strings.Split(f.AddedText, "\n") {
			if envRef.MatchString(line) {
				continue
			}
			for _, re := range secretRE {
				if m := re.FindString(line); m != "" {
					c.Details = append(c.Details, fmt.Sprintf("%s: похоже на ключ в добавленной строке %d: %s…", f.Path, i+1, clipRunes(m, 12)))
					c.OK = false
					break
				}
			}
		}
	}
	return c
}

// testsCheck — пакеты, где поменялся код, но не тесты. Это не ошибка
// сама по себе, поэтому проверка не «падает», а перечисляет.
func testsCheck(files []File) Check {
	code, tests := map[string]bool{}, map[string]bool{}
	for _, f := range files {
		if !strings.HasSuffix(f.Path, ".go") || f.Status == "deleted" {
			continue
		}
		dir := path.Dir(f.Path)
		if strings.HasSuffix(f.Path, "_test.go") {
			tests[dir] = true
		} else {
			code[dir] = true
		}
	}
	c := Check{Name: "тесты", OK: true}
	var dirs []string
	for d := range code {
		if !tests[d] && d != "cmd/advent" {
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		c.Details = append(c.Details, d+": код изменён, тесты в этом PR не тронуты")
		c.OK = false
	}
	return c
}

var dayFile = regexp.MustCompile(`^cmd/advent/day(\d\d)\.go$`)

// dayNoteCheck — правило проекта: у новой команды дня есть заметка дня.
func dayNoteCheck(files []File) Check {
	c := Check{Name: "заметка дня", OK: true}
	for _, f := range files {
		m := dayFile.FindStringSubmatch(f.Path)
		if m == nil || f.Status != "added" {
			continue
		}
		note := "docs/days/day" + m[1] + ".md"
		if _, ok := Find(files, note); !ok {
			c.Details = append(c.Details, f.Path+" добавлен, а "+note+" в PR нет")
			c.OK = false
		}
	}
	return c
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}
