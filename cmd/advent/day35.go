package main

// День 35 — реальная задача: `advent ship`, сдача дня челленджа.
//
// Каждый день этого репозитория кончается одинаково: проверить ветку,
// формат и тесты, дописать заметку и строку README, записать видео,
// запушить, открыть PR «Day NN — тема», отправить ссылки в таблицу сдачи.
// Руками это делалось по-разному и с потерями: таблица README отстала
// на девять дней, видео дня 31 записалось битым, PR каждый раз
// описывался с нуля. `advent ship` делает это пайплайном: проверки
// кодом, субагенты веером, публикация — только с подтверждения человека.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/mcp"
	"github.com/safronov-a1exander/advent/internal/review"
	"github.com/safronov-a1exander/advent/internal/servers/gitrepo"
	"github.com/safronov-a1exander/advent/internal/ship"
	"github.com/safronov-a1exander/advent/internal/store"
)

func cmdShip(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ship", flag.ExitOnError)
	c := bindCommon(fs)
	dayN := fs.Int("day", 0, "номер дня (по умолчанию — из имени текущей ветки day-NN)")
	yes := fs.Bool("yes", false, "выполнить публикацию (push и PR) без вопроса — человек разрешил заранее")
	skipTests := fs.Bool("skip-tests", false, "не гонять go test (для повторного прогона, когда тесты только что прошли)")
	hold := fs.Duration("hold", 0, "подержать итог на экране — для записи видео")
	if err := fs.Parse(args); err != nil {
		return err
	}
	defer func() {
		if *hold > 0 {
			time.Sleep(*hold)
		}
	}()
	cfg, err := config.Load(c.dir)
	if err != nil {
		return err
	}
	repo := &gitrepo.Repo{Root: c.dir, Timeout: 5 * time.Minute}
	day, err := shipDay(ctx, repo, *dayN)
	if err != nil {
		return err
	}
	logLine("сдача дня %d · ветка %s (от %s) · «%s»", day.N, day.Branch, day.Prev, day.Title)

	// 1. Цепочка проверок кодом: дёшево, детерминированно, по порядку.
	checks := shipChecks(ctx, repo, cfg, day, *skipTests)
	for _, ch := range checks {
		logLine("%s %s — %s", ch.Status.Mark(), ch.Name, ch.Detail)
	}
	if bl := ship.Blocking(checks); len(bl) > 0 {
		logLine("сдавать нельзя: %d проверок не прошли, и починить их может только человек", len(bl))
		return writeShip(cfg, day, checks, nil, nil, false)
	}

	// 2. Субагенты веером: у каждого свой контекст и своя задача.
	client, prov, err := cfg.Client(c.provider)
	if err != nil {
		return err
	}
	model := prov.DefaultMod
	subs := []ship.Subagent{
		{Name: "ревьюер", Run: func(ctx context.Context) ship.Result {
			return reviewerAgent(ctx, cfg, client, prov.Name, model, repo, day)
		}},
		{Name: "автор PR", Run: func(ctx context.Context) ship.Result { return describerAgent(ctx, client, model, repo, day) }},
		{Name: "редактор README", When: ship.NeedsFix("редактор README"),
			Run: func(ctx context.Context) ship.Result { return readmeAgent(ctx, cfg, client, prov.Name, model, day) }},
	}
	results := ship.FanOut(ctx, subs, checks, logLine)

	// 3. Что публиковать — решает код по проверкам, выполнять — человек.
	var desc *ship.Result
	for i := range results {
		if results[i].Name == "автор PR" && results[i].Err == nil {
			desc = &results[i]
		}
		if results[i].Name == "ревьюер" && results[i].Detail["verdict"] == review.RequestChanges {
			logLine("⚠ ревьюер просит изменения — посмотри отчёт, прежде чем подтверждать")
		}
		if results[i].Name == "редактор README" && results[i].Err == nil {
			logLine("⚠ README поправлен в черновике — посмотри diff и примени: advent files -apply, затем коммит")
		}
	}
	actions, bodyPath, err := shipActions(ctx, repo, cfg, day, checks, desc)
	if err != nil {
		return err
	}
	if len(actions) == 0 {
		logLine("публиковать нечего: ветка запушена, PR открыт")
		return writeShip(cfg, day, checks, results, nil, true)
	}
	logLine("план публикации:")
	for _, a := range actions {
		logLine("  %s", a)
	}
	ok := *yes
	if !ok {
		ok = confirm("выполнить? [y/N] ")
	}
	if !ok {
		logLine("не выполнено — нужно подтверждение человека: advent ship -yes")
		return writeShip(cfg, day, checks, results, actions, false)
	}
	for _, a := range actions {
		if err := runAction(ctx, a, bodyPath); err != nil {
			logLine("✗ %s: %v — остальное не выполняю", a, err)
			return writeShip(cfg, day, checks, results, actions, false)
		}
		logLine("✓ %s", a)
	}
	return writeShip(cfg, day, checks, results, actions, true)
}

var dayBranch = regexp.MustCompile(`^day-(\d\d)$`)

func shipDay(ctx context.Context, repo *gitrepo.Repo, n int) (ship.Day, error) {
	if n == 0 {
		cur, err := repo.Git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return ship.Day{}, err
		}
		m := dayBranch.FindStringSubmatch(strings.TrimSpace(cur))
		if m == nil {
			return ship.Day{}, fmt.Errorf("текущая ветка %q — не day-NN; укажи -day", strings.TrimSpace(cur))
		}
		n, _ = strconv.Atoi(m[1])
	}
	d := ship.Day{N: n, Branch: fmt.Sprintf("day-%02d", n), Prev: fmt.Sprintf("day-%02d", n-1),
		Note: fmt.Sprintf("docs/days/day%02d.md", n)}
	if b, err := os.ReadFile(filepath.Join(repo.Root, d.Note)); err == nil {
		first, _, _ := strings.Cut(string(b), "\n")
		if _, t, ok := strings.Cut(first, "—"); ok {
			d.Title = strings.TrimSpace(t)
		}
	}
	if url, err := repo.Git(ctx, "remote", "get-url", "origin"); err == nil {
		d.Remote = strings.TrimSuffix(strings.TrimSpace(url), ".git")
	}
	return d, nil
}

// shipChecks — проверки готовности по порядку. Fixer у проверки — имя
// субагента, который её чинит; без него провал блокирует сдачу.
func shipChecks(ctx context.Context, repo *gitrepo.Repo, cfg *config.Config, d ship.Day, skipTests bool) []ship.Check {
	var cs []ship.Check
	add := func(name string, st ship.Status, detail, fixer string) {
		cs = append(cs, ship.Check{Name: name, Status: st, Detail: detail, Fixer: fixer})
	}
	cur, _ := repo.Git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	switch {
	case strings.TrimSpace(cur) != d.Branch:
		add("ветка", ship.Fail, "сейчас "+strings.TrimSpace(cur)+", а сдаём "+d.Branch, "")
	default:
		if _, err := repo.Git(ctx, "merge-base", "--is-ancestor", d.Prev, "HEAD"); err != nil {
			add("ветка", ship.Fail, d.Branch+" не содержит "+d.Prev+" — шаг должен ответвляться от предыдущего", "")
		} else {
			n, _ := repo.Git(ctx, "rev-list", "--count", d.Prev+"..HEAD")
			add("ветка", ship.OK, fmt.Sprintf("%s от %s, коммитов %s", d.Branch, d.Prev, strings.TrimSpace(n)), "")
		}
	}
	if st, _ := repo.Git(ctx, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		add("рабочее дерево", ship.Fail, fmt.Sprintf("незакоммичено файлов: %d", len(strings.Split(strings.TrimSpace(st), "\n"))), "")
	} else {
		add("рабочее дерево", ship.OK, "всё закоммичено", "")
	}
	if out, err := runCmd(ctx, repo.Root, "gofmt", "-l", "cmd", "internal", "tools"); err != nil || strings.TrimSpace(out) != "" {
		add("gofmt", ship.Fail, firstNonEmpty(strings.TrimSpace(out), errString(err)), "")
	} else {
		add("gofmt", ship.OK, "форматирование в порядке", "")
	}
	if out, err := runCmd(ctx, repo.Root, "go", "vet", "./..."); err != nil {
		add("go vet", ship.Fail, lastLines(out, 3), "")
	} else {
		add("go vet", ship.OK, "без замечаний", "")
	}
	if skipTests {
		add("go test", ship.Warn, "пропущено флагом -skip-tests", "")
	} else if out, err := runCmd(ctx, repo.Root, "go", "test", "./..."); err != nil {
		add("go test", ship.Fail, lastLines(out, 3), "")
	} else {
		n := 0
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, "ok ") {
				n++
			}
		}
		add("go test", ship.OK, fmt.Sprintf("пакетов с тестами: %d, все прошли", n), "")
	}
	note, err := os.ReadFile(filepath.Join(repo.Root, d.Note))
	switch {
	case err != nil:
		add("заметка дня", ship.Fail, d.Note+" нет", "")
	case !strings.Contains(string(note), fmt.Sprintf("Ветка: `%s` (от `%s`)", d.Branch, d.Prev)):
		add("заметка дня", ship.Fail, d.Note+": нет строки «Ветка: `"+d.Branch+"` (от `"+d.Prev+"`)»", "")
	default:
		add("заметка дня", ship.OK, fmt.Sprintf("%s · %d строк", d.Note, strings.Count(string(note), "\n")), "")
	}
	readme, _ := os.ReadFile(filepath.Join(repo.Root, "README.md"))
	row := fmt.Sprintf("| %d | `%s` |", d.N, d.Branch)
	switch {
	case !strings.Contains(string(readme), row):
		add("README", ship.Fail, "в таблице шагов нет строки дня "+strconv.Itoa(d.N), "редактор README")
	case !strings.Contains(string(readme), "→ "+d.Branch):
		add("README", ship.Fail, "в цепочке веток нет "+d.Branch, "редактор README")
	default:
		add("README", ship.OK, "строка в таблице и звено в цепочке веток", "")
	}
	if _, err := os.Stat(filepath.Join(repo.Root, "scripts", fmt.Sprintf("day%02d.demo", d.N))); err != nil {
		add("демо-сценарий", ship.Warn, fmt.Sprintf("scripts/day%02d.demo нет — бывает, если демо — консольные команды", d.N), "")
	} else {
		add("демо-сценарий", ship.OK, fmt.Sprintf("scripts/day%02d.demo", d.N), "")
	}
	add(videoCheck(ctx, cfg, d))
	return pushChecks(ctx, repo, d, cs)
}

// videoCheck — последнее видео дня: есть, не пустое, длительность по ffprobe.
// Видео дня 31 однажды «записалось» без кадра: ffmpeg без libx264 упал
// сразу, а прогон шёл впустую.
func videoCheck(ctx context.Context, cfg *config.Config, d ship.Day) (string, ship.Status, string, string) {
	matches, _ := filepath.Glob(filepath.Join("recordings", fmt.Sprintf("day%02d-*.mp4", d.N)))
	if len(matches) == 0 {
		return "видео", ship.Fail, fmt.Sprintf("recordings/day%02d-*.mp4 нет — запись: раздел «Запись демо» в заметке", d.N), ""
	}
	sort.Strings(matches)
	v := matches[len(matches)-1]
	fi, _ := os.Stat(v)
	if fi.Size() < 100<<10 {
		return "видео", ship.Fail, fmt.Sprintf("%s — %d байт, запись битая", filepath.ToSlash(v), fi.Size()), ""
	}
	detail := fmt.Sprintf("%s · %.1f МБ", filepath.ToSlash(v), float64(fi.Size())/1e6)
	if probe := ffprobe(); probe != "" {
		out, err := runCmd(ctx, ".", probe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", v)
		secs, perr := strconv.ParseFloat(strings.TrimSpace(out), 64)
		if err != nil || perr != nil || secs < 10 {
			return "видео", ship.Fail, detail + " — ffprobe не видит в нём видео дольше 10 с", ""
		}
		detail += fmt.Sprintf(" · %.0f с", secs)
	}
	return "видео", ship.OK, detail, ""
}

// ffprobe — из PATH или из каталога OBS, как ищет scripts/win.ps1: на этой
// машине ffmpeg стоит вне PATH, и версия OBS в пути меняется.
func ffprobe() string {
	if p, err := exec.LookPath("ffprobe"); err == nil {
		return p
	}
	ms, _ := filepath.Glob(filepath.Join(os.Getenv("APPDATA"), "ow-electron", "*", "packages", "*", "*", "obs", "bin", "64bit", "ffprobe.exe"))
	sort.Strings(ms)
	if len(ms) > 0 {
		return ms[len(ms)-1]
	}
	return ""
}

func pushChecks(ctx context.Context, repo *gitrepo.Repo, d ship.Day, cs []ship.Check) []ship.Check {
	repo.Git(ctx, "fetch", "--quiet", "origin", d.Branch)
	local, _ := repo.Git(ctx, "rev-parse", "HEAD")
	remote, err := repo.Git(ctx, "rev-parse", "origin/"+d.Branch)
	switch {
	case err != nil:
		cs = append(cs, ship.Check{Name: "push", Status: ship.Warn, Detail: "ветки на GitHub ещё нет"})
	case strings.TrimSpace(local) != strings.TrimSpace(remote):
		cs = append(cs, ship.Check{Name: "push", Status: ship.Warn, Detail: "на GitHub старая версия ветки"})
	default:
		cs = append(cs, ship.Check{Name: "push", Status: ship.OK, Detail: "GitHub совпадает с локальной веткой"})
	}
	if out, err := gh(ctx, "pr", "list", "--head", d.Branch, "--state", "all", "--json", "number,state,url"); err == nil && strings.TrimSpace(string(out)) != "[]" {
		var prs []struct {
			Number int
			State  string
			URL    string
		}
		json.Unmarshal(out, &prs)
		if len(prs) > 0 {
			cs = append(cs, ship.Check{Name: "PR", Status: ship.OK, Detail: fmt.Sprintf("#%d %s", prs[0].Number, strings.ToLower(prs[0].State))})
			return cs
		}
	}
	return append(cs, ship.Check{Name: "PR", Status: ship.Warn, Detail: "PR ещё нет"})
}

// reviewerAgent — субагент-ревьюер: пайплайн дня 32 на работе ветки.
func reviewerAgent(ctx context.Context, cfg *config.Config, client *llm.Client, provName, model string, repo *gitrepo.Repo, d ship.Day) ship.Result {
	in, err := localInput(ctx, repo, d.Prev)
	if err != nil {
		return ship.Result{Err: err}
	}
	p := &review.Pipeline{Primary: review.Model{Client: client, ID: model}, Tries: 2, Backoff: 2 * time.Second,
		Checks: review.DefaultChecks(func(ctx context.Context, path string) ([]byte, error) {
			out, err := repo.Git(ctx, "show", in.SHA+":"+path)
			return []byte(out), err
		})}
	if kb, err := knowledgeFor(cfg, "", provName, "review"); err == nil {
		p.KB = kb
	}
	res, err := p.Run(ctx, in)
	if err != nil {
		return ship.Result{Err: err}
	}
	cost, _, _ := res.Cost()
	r := ship.Result{Cost: cost, Took: res.Took, Detail: map[string]string{}}
	if res.Failed {
		r.Err = errors.New(res.Error)
		return r
	}
	a := res.Answer
	r.Detail["verdict"] = a.Verdict
	high := 0
	for _, f := range a.Bugs {
		if f.Severity == "high" {
			high++
		}
	}
	md := res.Markdown()
	if i := strings.Index(md, "### Проверки кодом"); i > 0 {
		md = md[:i]
	}
	r.Text = fmt.Sprintf("вердикт %s · багов %d (high %d) · архитектура %d · рекомендаций %d\n\n%s",
		a.Verdict, len(a.Bugs), high, len(a.Architecture), len(a.Recommendations), stripHeader(md))
	return r
}

func stripHeader(md string) string {
	if _, rest, ok := strings.Cut(md, "\n\n"); ok {
		return rest
	}
	return md
}

const describerSystem = `Ты готовишь сдачу дня учебного челленджа: описание pull request и тезисы для видео. По заметке дня и истории ветки.
Ответ — JSON:
{"topic": "тема дня для заголовка «Day NN — тема», 3-7 слов, по-русски, со строчной буквы",
 "body": "описание PR: один абзац в 2-4 предложения — что добавляет шаг и что в нём главное; названия команд и файлов в обратных кавычках; без markdown-заголовков и списков",
 "points": ["три тезиса — что сказать в видео: не пересказ кода, а что вскрылось и почему решено так"]}
Только факты из заметки. Пиши так, как пишет автор заметок: коротко, по делу, без восторгов.`

// describerAgent — субагент: описание PR и тезисы к видео по заметке.
func describerAgent(ctx context.Context, client *llm.Client, model string, repo *gitrepo.Repo, d ship.Day) ship.Result {
	note, err := os.ReadFile(filepath.Join(repo.Root, d.Note))
	if err != nil {
		return ship.Result{Err: err}
	}
	log, _ := repo.Git(ctx, "log", "--format=%s", d.Prev+"..HEAD")
	stat, _ := repo.Diff(ctx, d.Prev, "", true)
	prompt := fmt.Sprintf("День %d. Заметка дня:\n\n%s\n\nКоммиты ветки:\n%s\nИзменённые файлы:\n%s",
		d.N, clipRunesMain(string(note), 9000), log, clipRunesMain(stat, 3000))
	t := 0.3
	resp, err := client.Chat(ctx, llm.Request{Model: model, Temperature: &t,
		Messages:       []llm.Message{{Role: llm.RoleSystem, Content: describerSystem}, {Role: llm.RoleUser, Content: prompt}},
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"}, Thinking: &llm.Thinking{Type: "disabled"}})
	if err != nil {
		return ship.Result{Err: err}
	}
	var out struct {
		Topic  string   `json:"topic"`
		Body   string   `json:"body"`
		Points []string `json:"points"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.Content)), &out); err != nil || out.Topic == "" || out.Body == "" {
		return ship.Result{Cost: resp.CostUSD, Err: fmt.Errorf("ответ не разобрался: %v", err)}
	}
	title := fmt.Sprintf("Day %d — %s", d.N, strings.TrimSpace(out.Topic))
	body := strings.TrimSpace(out.Body) + "\n\nЗаметки: `" + d.Note + "`.\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)\n"
	var pts strings.Builder
	for i, p := range out.Points {
		fmt.Fprintf(&pts, "%d. %s\n", i+1, p)
	}
	return ship.Result{Cost: resp.CostUSD,
		Detail: map[string]string{"title": title, "body": body},
		Text:   fmt.Sprintf("**%s**\n\n%s\nТезисы для видео:\n\n%s", title, body, pts.String())}
}

// readmeAgent — субагент-редактор: ассистент дня 34 с целью «добавь
// строку дня». Правка — в черновик; применяет человек.
func readmeAgent(ctx context.Context, cfg *config.Config, client *llm.Client, provName, model string, d ship.Day) ship.Result {
	w, err := store.NewWriter(cfg.RunsDir, store.NewRunID("ship-readme"))
	if err != nil {
		return ship.Result{Err: err}
	}
	defer w.Close()
	hub := mcp.NewHub(cfg.MCPServers)
	defer hub.Close()
	pool := agent.NewPool(client, provName, w)
	pool.SetToolbox(hub)
	ag := pool.SpawnTemp(agent.Config{Name: "редактор README", Model: model, System: filesSystem,
		Thinking: "disabled", MCP: []string{"files"}, MaxToolRounds: 16})
	os.RemoveAll(filepath.FromSlash(StageDir))
	goal := fmt.Sprintf("В README нет дня %d. По заметке %s добавь строку в таблицу шагов в стиле соседних строк и звено %s в цепочку веток. Больше ничего не меняй.", d.N, d.Note, d.Branch)
	reply, err := ag.Ask(ctx, goal, nil)
	sp := pool.Spent()
	if err != nil {
		return ship.Result{Cost: sp.CostUSD, Err: err}
	}
	return ship.Result{Cost: sp.CostUSD, Text: strings.TrimSpace(reply.Final.Content) + "\n\nПравка — в черновике: `advent files -diff`, применить — `advent files -apply`."}
}

// shipActions — что опубликовать: push и PR. Файл с описанием PR пишется
// заранее, чтобы человек видел его до подтверждения.
func shipActions(ctx context.Context, repo *gitrepo.Repo, cfg *config.Config, d ship.Day, checks []ship.Check, desc *ship.Result) ([]string, string, error) {
	var acts []string
	for _, c := range checks {
		if c.Name == "push" && c.Status != ship.OK {
			acts = append(acts, "git push -u origin "+d.Branch)
		}
	}
	needPR := false
	for _, c := range checks {
		if c.Name == "PR" && c.Status != ship.OK {
			needPR = true
		}
	}
	if !needPR {
		return acts, "", nil
	}
	if desc == nil {
		return acts, "", errors.New("PR нужен, но описание не готово — субагент «автор PR» не справился")
	}
	// PR в main, если предыдущий день туда уже влит; иначе — стеком
	// в ветку предыдущего дня, чтобы в diff был только этот шаг.
	base := "main"
	repo.Git(ctx, "fetch", "--quiet", "origin", "main")
	if _, err := repo.Git(ctx, "merge-base", "--is-ancestor", d.Prev, "origin/main"); err != nil {
		base = d.Prev
	}
	bodyPath := filepath.Join(cfg.ReportsDir, "ship", fmt.Sprintf("day%02d-pr.md", d.N))
	if err := os.MkdirAll(filepath.Dir(bodyPath), 0o755); err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(bodyPath, []byte(desc.Detail["body"]), 0o644); err != nil {
		return nil, "", err
	}
	acts = append(acts, fmt.Sprintf("gh pr create --base %s --head %s --title %q --body-file %s", base, d.Branch, desc.Detail["title"], filepath.ToSlash(bodyPath)))
	return acts, bodyPath, nil
}

// runAction выполняет одну строку плана. Разбор — свой и простой: план
// пишет код выше, а не модель, так что в нём только известные команды.
func runAction(ctx context.Context, a, bodyPath string) error {
	switch {
	case strings.HasPrefix(a, "git push "):
		_, err := runCmd(ctx, ".", "git", strings.Fields(a)[1:]...)
		return err
	case strings.HasPrefix(a, "gh pr create "):
		args, err := splitQuoted(strings.TrimPrefix(a, "gh "))
		if err != nil {
			return err
		}
		out, err := gh(ctx, args...)
		if err == nil {
			logLine("  %s", strings.TrimSpace(string(out)))
		}
		return err
	}
	return fmt.Errorf("неизвестное действие %q", a)
}

// splitQuoted — аргументы с кавычками Go (%q) в список.
func splitQuoted(s string) ([]string, error) {
	var out []string
	for s = strings.TrimSpace(s); s != ""; s = strings.TrimSpace(s) {
		if s[0] == '"' {
			v, err := strconv.QuotedPrefix(s)
			if err != nil {
				return nil, err
			}
			u, _ := strconv.Unquote(v)
			out = append(out, u)
			s = s[len(v):]
			continue
		}
		f, rest, _ := strings.Cut(s, " ")
		out = append(out, f)
		s = rest
	}
	return out, nil
}

func writeShip(cfg *config.Config, d ship.Day, checks []ship.Check, results []ship.Result, actions []string, published bool) error {
	path := filepath.Join(cfg.ReportsDir, "ship", fmt.Sprintf("day%02d.md", d.N))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	md := ship.Report(d, checks, results, actions, published)
	if d.Remote != "" {
		md += fmt.Sprintf("\n## В таблицу сдачи\n\nветка: %s/tree/%s\n", d.Remote, d.Branch)
		for _, c := range checks {
			if c.Name == "видео" && c.Status == ship.OK {
				md += "видео: " + strings.SplitN(c.Detail, " ·", 2)[0] + " — загрузить и вставить ссылку\n"
			}
		}
	}
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		return err
	}
	logLine("отчёт: %s", filepath.ToSlash(path))
	return nil
}

func confirm(prompt string) bool {
	fmt.Print(prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}

func runCmd(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.ReplaceAll(string(out), "\r\n", "\n"), err
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, " · ")
}

func clipRunesMain(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "\n…"
	}
	return s
}
