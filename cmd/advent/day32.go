package main

// День 32 — автоматическое ревью кода: `advent review`.
//
// Пайплайн из лекции: триггер → вход → агент → выход. Триггер — новый
// PR или новый коммит в нём; VPS и облачного раннера нет, поэтому
// наблюдает за репозиторием эта машина (`-watch`): опрашивает GitHub
// через gh, на каждую новую версию PR запускает ревью и публикует его
// комментарием. Эмбеддинги, реранк и индекс — здесь же, на локальной
// видеокарте, — то, чего нет в раннере GitHub Actions.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/review"
	"github.com/safronov-a1exander/advent/internal/servers/gitrepo"
)

func cmdReview(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	c := bindCommon(fs)
	pr := fs.Int("pr", 0, "номер PR на GitHub: diff и описание берутся через gh")
	base := fs.String("base", "", "без -pr: ревью текущей ветки после ответвления от base (например day-31)")
	comment := fs.Bool("comment", false, "опубликовать ревью комментарием в PR (повторно ту же версию не публикует)")
	watch := fs.Duration("watch", 0, "следить за открытыми PR: опрашивать GitHub с этим периодом и ревьюить каждую новую версию")
	model := fs.String("model", "", "модель ревью (пусто — default_model провайдера)")
	fallback := fs.String("fallback", "deepseek-v4-pro", "запасная модель, если основная не дала ответа; пусто — без неё")
	kb := fs.String("kb", "review", "база знаний из rag.bases: правила ревью, документация и код")
	tries := fs.Int("tries", 2, "попыток на модель")
	stats := fs.Bool("stats", false, "только метрики по журналу прогонов")
	maxReviews := fs.Int("max-reviews", 0, "с -watch: остановиться после стольких опубликованных ревью (0 — следить, пока не остановят)")
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
	metricsPath := filepath.Join(cfg.RunsDir, "review", "metrics.jsonl")
	if *stats {
		ms, err := review.LoadMetrics(metricsPath)
		if err != nil {
			return err
		}
		fmt.Println(review.Summarize(ms))
		return nil
	}

	client, prov, err := cfg.Client(c.provider)
	if err != nil {
		return err
	}
	p := &review.Pipeline{
		Primary: review.Model{Client: client, ID: firstNonEmpty(*model, prov.DefaultMod)},
		Tries:   *tries,
		Backoff: 2 * time.Second,
		Timeout: 3 * time.Minute,
		Log:     logLine,
	}
	if *fallback != "" {
		p.Fallback = review.Model{Client: client, ID: *fallback}
	}
	if k, err := knowledgeFor(cfg, "", prov.Name, *kb); err == nil {
		p.KB = k
	} else {
		logLine("RAG недоступен: %v — ревью без документации", err)
	}
	repo := &gitrepo.Repo{Root: c.dir, Timeout: 2 * time.Minute}
	run := func(in review.Input) (*review.Result, error) {
		// Копия пайплайна на прогон: проверки читают файлы именно этой
		// версии, и наблюдатель, ревьюя PR за PR, не должен подменять их
		// в общем объекте (замечание собственного ревью, день 32).
		pp := *p
		pp.Checks = review.DefaultChecks(func(ctx context.Context, path string) ([]byte, error) {
			out, err := repo.Git(ctx, "show", in.SHA+":"+path)
			return []byte(out), err
		})
		res, err := pp.Run(ctx, in)
		if err != nil {
			return nil, err
		}
		name := fmt.Sprintf("local-%s-%s.md", safeRef(in.Head), shortSHA(in.SHA))
		if in.PR > 0 {
			name = fmt.Sprintf("pr-%d-%s.md", in.PR, shortSHA(in.SHA))
		}
		out := filepath.Join(cfg.ReportsDir, "review", name)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(out, []byte(res.Markdown()), 0o644); err != nil {
			return nil, err
		}
		logLine("выход: отчёт %s", filepath.ToSlash(out))
		if err := review.AppendMetric(metricsPath, review.MetricOf(res)); err != nil {
			logLine("метрики не записались: %v", err)
		}
		if ms, err := review.LoadMetrics(metricsPath); err == nil {
			for _, l := range strings.Split(review.Summarize(ms).String(), "\n") {
				logLine("метрики · %s", l)
			}
		}
		return res, nil
	}

	switch {
	case *watch > 0:
		return watchPRs(ctx, repo, *watch, *maxReviews, run)
	case *pr > 0:
		in, err := prInput(ctx, repo, *pr)
		if err != nil {
			return err
		}
		res, err := run(in)
		if err != nil {
			return err
		}
		if *comment {
			return publish(ctx, in, res)
		}
		return nil
	case *base != "":
		in, err := localInput(ctx, repo, *base)
		if err != nil {
			return err
		}
		_, err = run(in)
		return err
	}
	return errors.New("укажи -pr N, -base <ветка> или -watch 30s")
}

func logLine(format string, a ...any) {
	fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func safeRef(s string) string { return strings.NewReplacer("/", "-", "\\", "-").Replace(s) }

// gh — вызов GitHub CLI. Авторизация и репозиторий — его забота:
// ключей в стенде не появляется.
func gh(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh %s: %s", args[0], firstNonEmpty(strings.TrimSpace(errb.String()), err.Error()))
	}
	return out.Bytes(), nil
}

type ghPR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Base   string `json:"baseRefName"`
	Head   string `json:"headRefName"`
	SHA    string `json:"headRefOid"`
	URL    string `json:"url"`
}

// prInput — вход пайплайна из PR: описание, diff и коммит головы, чтобы
// проверки кодом читали файлы именно этой версии.
func prInput(ctx context.Context, repo *gitrepo.Repo, n int) (review.Input, error) {
	logLine("триггер: PR #%d", n)
	raw, err := gh(ctx, "pr", "view", strconv.Itoa(n), "--json", "number,title,body,baseRefName,headRefName,headRefOid,url")
	if err != nil {
		return review.Input{}, err
	}
	var p ghPR
	if err := json.Unmarshal(raw, &p); err != nil {
		return review.Input{}, err
	}
	diff, err := gh(ctx, "pr", "diff", strconv.Itoa(n))
	if err != nil {
		return review.Input{}, err
	}
	if _, err := repo.Git(ctx, "cat-file", "-e", p.SHA+"^{commit}"); err != nil {
		logLine("коммита %s нет локально — забираю pull/%d/head", shortSHA(p.SHA), n)
		if _, err := repo.Git(ctx, "fetch", "--quiet", "origin", fmt.Sprintf("pull/%d/head", n)); err != nil {
			return review.Input{}, err
		}
	}
	logLine("«%s» · %s → %s · %s", p.Title, p.Head, p.Base, shortSHA(p.SHA))
	return review.Input{PR: p.Number, URL: p.URL, Title: p.Title, Body: p.Body, Base: p.Base, Head: p.Head,
		SHA: p.SHA, Diff: string(diff)}, nil
}

// localInput — то же для ветки без PR: работа ветки после base.
func localInput(ctx context.Context, repo *gitrepo.Repo, base string) (review.Input, error) {
	head, err := repo.Git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return review.Input{}, err
	}
	sha, err := repo.Git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return review.Input{}, err
	}
	diff, err := repo.Diff(ctx, base, "", false)
	if err != nil {
		return review.Input{}, err
	}
	subj, _ := repo.Git(ctx, "log", "-1", "--format=%s")
	in := review.Input{Title: strings.TrimSpace(subj), Base: base, Head: strings.TrimSpace(head),
		SHA: strings.TrimSpace(sha), Diff: diff}
	logLine("триггер: ветка %s после %s · %s", in.Head, base, shortSHA(in.SHA))
	return in, nil
}

// publish — комментарий в PR. Если ревью этой версии уже опубликовано
// (метка с SHA в теле), второй раз не пишем: перезапуск пайплайна
// не должен засыпать PR одинаковыми комментариями.
func publish(ctx context.Context, in review.Input, res *review.Result) error {
	raw, err := gh(ctx, "pr", "view", strconv.Itoa(in.PR), "--json", "comments")
	if err != nil {
		return err
	}
	if bytes.Contains(raw, []byte(review.Marker(in.SHA))) {
		logLine("ревью версии %s в PR #%d уже есть — не дублирую", shortSHA(in.SHA), in.PR)
		return nil
	}
	f, err := os.CreateTemp("", "advent-review-*.md")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, werr := f.WriteString(res.Markdown())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("комментарий не собрался: %w", werr)
	}
	out, err := gh(ctx, "pr", "comment", strconv.Itoa(in.PR), "--body-file", f.Name())
	if err != nil {
		return err
	}
	logLine("выход: комментарий в PR #%d — %s", in.PR, strings.TrimSpace(string(out)))
	return nil
}

// watchPRs — локальный триггер вместо облачного раннера: открытые PR
// опрашиваются по расписанию, новая версия (новый SHA головы) — ревью
// и комментарий. Уже отревьюенные версии узнаются по метке в комментариях,
// поэтому перезапуск наблюдателя ничего не повторяет.
//
// Версия считается сделанной только после публикации: если ревью или
// комментарий упали, на следующем опросе она пробуется снова, а не
// пропускается молча до перезапуска (замечание собственного ревью).
func watchPRs(ctx context.Context, repo *gitrepo.Repo, every time.Duration, max int, run func(review.Input) (*review.Result, error)) error {
	logLine("наблюдаю за открытыми PR каждые %s · Ctrl+C — стоп", every)
	done := map[string]bool{}
	published := 0
	for {
		raw, err := gh(ctx, "pr", "list", "--state", "open", "--json", "number,headRefOid,title")
		if err != nil {
			logLine("GitHub недоступен: %v — следующая попытка через %s", err, every)
		} else {
			var prs []ghPR
			if err := json.Unmarshal(raw, &prs); err != nil {
				logLine("ответ gh pr list не разобрать: %v", err)
			}
			for _, p := range prs {
				key := fmt.Sprintf("%d@%s", p.Number, p.SHA)
				if done[key] {
					continue
				}
				com, err := gh(ctx, "pr", "view", strconv.Itoa(p.Number), "--json", "comments")
				if err == nil && bytes.Contains(com, []byte(review.Marker(p.SHA))) {
					done[key] = true // эту версию уже ревьюили
					continue
				}
				in, err := prInput(ctx, repo, p.Number)
				if err != nil {
					logLine("PR #%d: %v", p.Number, err)
					continue
				}
				res, err := run(in)
				if err != nil {
					logLine("PR #%d: %v", p.Number, err)
					continue
				}
				if err := publish(ctx, in, res); err != nil {
					logLine("PR #%d: комментарий не опубликован: %v — попробую на следующем опросе", p.Number, err)
					continue
				}
				done[key] = true
				published++
				if max > 0 && published >= max {
					logLine("опубликовано ревью: %d — наблюдение закончено", published)
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(every):
		}
	}
}
