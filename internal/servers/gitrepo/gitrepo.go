// Package gitrepo — MCP-сервер «Git» (день 31): ассистент разработчика
// видит репозиторий, в котором работает, — ветку, изменённые файлы,
// список файлов, diff и историю.
//
// Документация отвечает на «как устроено», а git — на «где мы сейчас»:
// на какой ветке, что поменялось с прошлого шага, какие файлы есть.
// Первое лежит в базе знаний, второе меняется каждую минуту и в индекс
// не годится — его нужно спрашивать у репозитория в момент вопроса.
//
// Все инструменты только читают. Коммит, push и смена ветки — опасные
// операции из лекции недели, и ассистенту, который отвечает на вопросы,
// они не нужны вовсе.
package gitrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/mcp"
)

// Version — версия сервера в ответе на initialize.
const Version = "day-31"

// MaxOutput — сколько символов вывода git отдаётся модели. Diff целой
// недели — сотни килобайт; модели нужна его часть и честная пометка,
// что остальное обрезано.
const MaxOutput = 24000

// Repo — репозиторий, с которым работает сервер.
type Repo struct {
	Root string
	// Timeout — сколько ждать одну команду git.
	Timeout time.Duration
}

// New — сервер над репозиторием в каталоге root.
func New(root string) *mcp.Server {
	r := &Repo{Root: root, Timeout: 20 * time.Second}
	s := mcp.NewServer("git", Version,
		"Git-репозиторий проекта: текущая ветка, изменения, файлы, diff, история. Только чтение.")
	t := true
	ro := &mcp.Annotations{ReadOnlyHint: &t}
	s.Register(mcp.Tool{
		Name: "branch", Title: "Текущая ветка",
		Description: "Текущая git-ветка проекта, последний коммит и сколько файлов изменено, но не закоммичено.",
		Annotations: ro,
	}, r.branch)
	s.Register(mcp.Tool{
		Name: "status", Title: "Изменённые файлы",
		Description: "Файлы, изменённые в рабочей копии и ещё не закоммиченные: путь и что с ним (изменён, новый, удалён).",
		Annotations: ro,
	}, r.status)
	s.Register(mcp.Tool{
		Name: "files", Title: "Файлы проекта",
		Description: "Список файлов под контролем git. Можно сузить каталогом или шаблоном: «internal/servers», «*.md», «cmd/**/*.go».",
		InputSchema: mcp.Schema(
			mcp.Prop{Name: "path", Type: "string", Description: "каталог или шаблон пути; пусто — все файлы"},
			mcp.Prop{Name: "limit", Type: "integer", Description: "сколько путей вернуть, по умолчанию 200"},
		),
		Annotations: ro,
	}, r.files)
	s.Register(mcp.Tool{
		Name: "diff", Title: "Diff",
		Description: "Что изменилось. Без base — незакоммиченные правки рабочей копии. С base — всё, что сделано " +
			"на текущей ветке после ответвления от base (например base=main или base=day-30). " +
			"stat=true — только список файлов и число строк, без самих изменений.",
		InputSchema: mcp.Schema(
			mcp.Prop{Name: "base", Type: "string", Description: "ветка или коммит, от которого считать изменения"},
			mcp.Prop{Name: "path", Type: "string", Description: "только этот файл или каталог"},
			mcp.Prop{Name: "stat", Type: "boolean", Description: "только сводка по файлам"},
		),
		Annotations: ro,
	}, r.diff)
	s.Register(mcp.Tool{
		Name: "log", Title: "История",
		Description: "Последние коммиты: хэш, дата, заголовок. С base — только коммиты текущей ветки после base.",
		InputSchema: mcp.Schema(
			mcp.Prop{Name: "base", Type: "string", Description: "ветка или коммит, после которого показывать"},
			mcp.Prop{Name: "limit", Type: "integer", Description: "сколько коммитов, по умолчанию 15"},
		),
		Annotations: ro,
	}, r.log)
	return s
}

// Git — одна команда git в репозитории. Пути с кириллицей git по умолчанию
// экранирует восьмеричными кодами — core.quotepath=off возвращает их как есть.
func (r *Repo) Git(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	full := append([]string{"-C", r.Root, "-c", "core.quotepath=off", "--no-pager"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.ReplaceAll(out.String(), "\r\n", "\n"), nil
}

// ref проверяет имя ветки или коммита из аргументов модели. Строка
// с «-» в начале стала бы флагом git — так через «base» можно было бы
// передать, например, --output и записать файл.
func ref(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if strings.HasPrefix(s, "-") || strings.ContainsAny(s, " \t\n") {
		return "", fmt.Errorf("недопустимое имя ветки или коммита %q", s)
	}
	return s, nil
}

func clip(s string) string {
	if len(s) <= MaxOutput {
		return s
	}
	cut := s[:MaxOutput]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	return cut + fmt.Sprintf("\n… обрезано: показано %d из %d символов — сузь запрос через path или stat=true", len(cut), len(s))
}

// Branch — то, что отвечает инструмент branch, для тех, кто зовёт его
// из кода, а не через модель (подпись /help, день 31).
type Branch struct {
	Name    string `json:"branch"`
	Head    string `json:"head"`
	Subject string `json:"last_commit"`
	Dirty   int    `json:"uncommitted_files"`
}

func (r *Repo) Branch(ctx context.Context) (Branch, error) {
	var b Branch
	name, err := r.Git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return b, err
	}
	b.Name = strings.TrimSpace(name)
	if head, err := r.Git(ctx, "log", "-1", "--format=%h%x09%s"); err == nil {
		b.Head, b.Subject, _ = strings.Cut(strings.TrimSpace(head), "\t")
	}
	if st, err := r.Git(ctx, "status", "--porcelain"); err == nil {
		b.Dirty = len(lines(st))
	}
	return b, nil
}

func (r *Repo) branch(ctx context.Context, _ json.RawMessage) mcp.CallResult {
	b, err := r.Branch(ctx)
	if err != nil {
		return mcp.ErrorResult("%v", err)
	}
	out, _ := json.MarshalIndent(b, "", "  ")
	return mcp.TextResult(string(out))
}

var statusLabel = map[byte]string{
	'M': "изменён", 'A': "добавлен", 'D': "удалён", 'R': "переименован", '?': "новый, не в git",
}

func (r *Repo) status(ctx context.Context, _ json.RawMessage) mcp.CallResult {
	st, err := r.Git(ctx, "status", "--porcelain")
	if err != nil {
		return mcp.ErrorResult("%v", err)
	}
	rows := lines(st)
	if len(rows) == 0 {
		return mcp.TextResult("рабочая копия чистая: всё закоммичено")
	}
	var b strings.Builder
	for _, row := range rows {
		if len(row) < 4 {
			continue
		}
		code := row[0]
		if code == ' ' {
			code = row[1]
		}
		label := statusLabel[code]
		if label == "" {
			label = strings.TrimSpace(row[:2])
		}
		fmt.Fprintf(&b, "%s — %s\n", row[3:], label)
	}
	return mcp.TextResult(b.String())
}

func (r *Repo) files(ctx context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct {
		Path  string `json:"path"`
		Limit int    `json:"limit"`
	}](raw)
	if bad != nil {
		return *bad
	}
	if a.Limit <= 0 {
		a.Limit = 200
	}
	out, err := r.Git(ctx, "ls-files")
	if err != nil {
		return mcp.ErrorResult("%v", err)
	}
	want := strings.Trim(strings.ReplaceAll(strings.TrimSpace(a.Path), "\\", "/"), "/")
	var picked []string
	for _, f := range lines(out) {
		if want == "" || matches(want, f) {
			picked = append(picked, f)
		}
	}
	if len(picked) == 0 {
		return mcp.TextResult(fmt.Sprintf("под «%s» файлов нет", a.Path))
	}
	total := len(picked)
	if total > a.Limit {
		picked = picked[:a.Limit]
	}
	res := strings.Join(picked, "\n")
	if total > len(picked) {
		res += fmt.Sprintf("\n… и ещё %d — сузь path", total-len(picked))
	}
	return mcp.TextResult(fmt.Sprintf("файлов: %d\n%s", total, res))
}

// matches — путь лежит в каталоге want или подходит под шаблон: «*.md»
// ищется по имени файла в любом каталоге, «cmd/**/*.go» — по сегментам.
func matches(want, file string) bool {
	if !strings.ContainsAny(want, "*?[") {
		return file == want || strings.HasPrefix(file, want+"/")
	}
	if !strings.Contains(want, "/") {
		ok, _ := path.Match(want, path.Base(file))
		return ok
	}
	return matchSegs(strings.Split(want, "/"), strings.Split(file, "/"))
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

func (r *Repo) diff(ctx context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct {
		Base string `json:"base"`
		Path string `json:"path"`
		Stat bool   `json:"stat"`
	}](raw)
	if bad != nil {
		return *bad
	}
	out, err := r.Diff(ctx, a.Base, a.Path, a.Stat)
	if err != nil {
		return mcp.ErrorResult("%v", err)
	}
	if strings.TrimSpace(out) == "" {
		if a.Base == "" {
			return mcp.TextResult("незакоммиченных изменений нет; чтобы увидеть работу ветки, передай base")
		}
		return mcp.TextResult("с " + a.Base + " изменений нет")
	}
	return mcp.TextResult(clip(out))
}

// Diff — изменения ветки после ответвления от base (три точки: чужие
// коммиты в base после ответвления в diff не попадают) или, без base,
// незакоммиченные правки. Его же берёт ревью дня 32.
func (r *Repo) Diff(ctx context.Context, base, file string, stat bool) (string, error) {
	base, err := ref(base)
	if err != nil {
		return "", err
	}
	args := []string{"diff", "--no-color", "--no-ext-diff"}
	if stat {
		args = append(args, "--stat=160")
	}
	if base != "" {
		args = append(args, base+"...HEAD")
	} else {
		args = append(args, "HEAD")
	}
	if f := strings.TrimSpace(file); f != "" {
		args = append(args, "--", f)
	}
	return r.Git(ctx, args...)
}

func (r *Repo) log(ctx context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct {
		Base  string `json:"base"`
		Limit int    `json:"limit"`
	}](raw)
	if bad != nil {
		return *bad
	}
	base, err := ref(a.Base)
	if err != nil {
		return mcp.ErrorResult("%v", err)
	}
	if a.Limit <= 0 {
		a.Limit = 15
	}
	args := []string{"log", "--format=%h %ad %s", "--date=short", "-n", strconv.Itoa(a.Limit)}
	if base != "" {
		args = append(args, base+"..HEAD")
	}
	out, err := r.Git(ctx, args...)
	if err != nil {
		return mcp.ErrorResult("%v", err)
	}
	if strings.TrimSpace(out) == "" {
		return mcp.TextResult("коммитов нет")
	}
	return mcp.TextResult(out)
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return out
}
