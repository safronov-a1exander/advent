package main

// День 34 — ассистент, который работает с файлами проекта: `advent files`.
//
// Задача ставится целью («таблица в README отстаёт от заметок — обнови»),
// а не командой «открой файл X»: какие файлы читать, где искать и что
// править, ассистент решает сам через MCP-сервер files. Правки ложатся
// в черновик (runs/files/stage), в конце печатается diff и сохраняется
// в reports/files/. В проект черновик попадает только второй командой —
// `advent files -apply`, которую запускает человек, посмотрев на diff.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/mcp"
	"github.com/safronov-a1exander/advent/internal/servers/files"
	"github.com/safronov-a1exander/advent/internal/store"
)

const filesSystem = `Ты — ассистент разработчика проекта advent (Go). Ты работаешь с файлами проекта через инструменты: ищешь, читаешь, сравниваешь и правишь.
Задачу тебе ставят целью. Сам реши, какие файлы нужны: сначала найди их (list_files, search), потом прочитай нужное (read_file) и только потом меняй.
Правила:
- правь точечно: edit_file с дословным фрагментом из read_file; write_file — для новых файлов или когда меняется весь файл;
- ничего не выдумывай: факты бери из файлов проекта и называй, откуда взял;
- если цель — проверка, файлы не меняй, а перечисли найденные расхождения с путями и строками;
- есть ли что-то в файле, проверяй инструментом search по каждому элементу, а не по памяти о прочитанном: в отчёт — только то, что подтвердил поиск;
- все правки попадают в черновик, человек посмотрит diff и решит, применять ли;
- в конце коротко: что сделал, какие файлы прочитал и изменил, что осталось на человека. По-русски.`

// StageDir — черновик правок ассистента.
const StageDir = "runs/files/stage"

func cmdFiles(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("files", flag.ExitOnError)
	c := bindCommon(fs)
	model := fs.String("model", "", "модель (пусто — default_model провайдера)")
	apply := fs.Bool("apply", false, "применить черновик к проекту — после того как посмотрел diff")
	showDiff := fs.Bool("diff", false, "показать diff черновика")
	keep := fs.Bool("keep", false, "не очищать черновик перед новой целью — продолжить правки")
	hold := fs.Duration("hold", 0, "подержать итог на экране — для записи видео")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return err
	}
	defer func() {
		if *hold > 0 {
			time.Sleep(*hold)
		}
	}()
	root, _ := filepath.Abs(c.dir)
	proj := &files.Project{Root: root, Stage: filepath.Join(root, filepath.FromSlash(StageDir))}

	switch {
	case *apply:
		return applyStage(proj)
	case *showDiff:
		d, err := stageDiff(proj)
		if err != nil {
			return err
		}
		fmt.Print(d)
		return nil
	}
	goal := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if goal == "" {
		return errors.New(`нужна цель: advent files "таблица в README отстаёт от заметок — обнови"`)
	}
	if !*keep {
		if err := os.RemoveAll(proj.Stage); err != nil {
			return err
		}
	}

	cfg, err := config.Load(c.dir)
	if err != nil {
		return err
	}
	client, prov, err := cfg.Client(c.provider)
	if err != nil {
		return err
	}
	w, err := store.NewWriter(cfg.RunsDir, store.NewRunID("files"))
	if err != nil {
		return err
	}
	defer w.Close()
	hub := mcp.NewHub(cfg.MCPServers)
	defer hub.Close()
	pool := agent.NewPool(client, prov.Name, w)
	pool.SetToolbox(hub)
	ag := pool.SpawnTemp(agent.Config{
		Name: "файлы", Model: firstNonEmpty(*model, prov.DefaultMod), System: filesSystem,
		Thinking: "disabled", MCP: []string{"files"}, MaxToolRounds: 24,
	})

	logLine("цель: %s", goal)
	started := time.Now()
	reply, err := ag.Ask(ctx, goal, func(e agent.Event) {
		switch e.Kind {
		case agent.EventToolCall:
			logLine("→ %s %s", e.Label, clip(oneLine(e.Content), 110))
		case agent.EventToolResult:
			mark := "←"
			if e.Failed {
				mark = "✗"
			}
			logLine("  %s %s", mark, clip(oneLine(e.Content), 120))
		}
	})
	if err != nil {
		return err
	}
	// Расход — по счётчику пула: в Reply.Steps цикл инструментов кладёт
	// только последний вызов, и цена по нему занижена в разы.
	sp := pool.Spent()
	fmt.Printf("\n%s\n\n", strings.TrimSpace(reply.Final.Content))
	logLine("вызовов модели %d · инструментов %d · in %d / out %d · $%.4f · %s",
		sp.Calls, len(reply.Tools), sp.Prompt, sp.Completion, sp.CostUSD, time.Since(started).Round(time.Second))

	staged, err := proj.Staged()
	if err != nil {
		return err
	}
	if len(staged) == 0 {
		logLine("файлы не изменены — черновик пуст")
		return nil
	}
	d, err := stageDiff(proj)
	if err != nil {
		return err
	}
	fmt.Print("\n" + d)
	out := filepath.Join(cfg.ReportsDir, "files", time.Now().Format("20060102-150405")+".diff")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err == nil {
		os.WriteFile(out, []byte(d), 0o644)
	}
	logLine("черновик: %d файлов · diff сохранён в %s", len(staged), filepath.ToSlash(out))
	logLine("в проект ещё ничего не записано · применить: advent files -apply")
	return nil
}

// stageDiff — unified diff черновика против проекта. git diff --no-index
// умеет сравнивать два файла вне индекса; пути в заголовках переписываются
// на a/<путь> и b/<путь>, как у обычного diff проекта.
func stageDiff(p *files.Project) (string, error) {
	staged, err := p.Staged()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, rel := range staged {
		orig := filepath.Join(p.Root, filepath.FromSlash(rel))
		if _, err := os.Stat(orig); err != nil {
			orig = os.DevNull
		}
		cmd := exec.Command("git", "-c", "core.quotepath=off", "diff", "--no-index", "--no-color", "--", orig, filepath.Join(p.Stage, filepath.FromSlash(rel)))
		out, err := cmd.Output()
		var ee *exec.ExitError
		if err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1) { // 1 — есть различия
			return "", fmt.Errorf("diff %s: %w", rel, err)
		}
		b.WriteString(relabel(string(out), rel))
	}
	return b.String(), nil
}

// relabel — заголовки diff с абсолютными путями → a/<rel>, b/<rel>.
func relabel(d, rel string) string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(d, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			l = "diff --git a/" + rel + " b/" + rel
		case strings.HasPrefix(l, "--- ") && !strings.HasSuffix(l, "/dev/null"):
			l = "--- a/" + rel
		case strings.HasPrefix(l, "+++ "):
			l = "+++ b/" + rel
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// applyStage переносит черновик в проект и очищает его.
func applyStage(p *files.Project) error {
	staged, err := p.Staged()
	if err != nil {
		return err
	}
	if len(staged) == 0 {
		logLine("черновик пуст — применять нечего")
		return nil
	}
	for _, rel := range staged {
		if _, err := p.Clean(rel); err != nil {
			return err
		}
		src := filepath.Join(p.Stage, filepath.FromSlash(rel))
		dst := filepath.Join(p.Root, filepath.FromSlash(rel))
		if err := copyFile(src, dst); err != nil {
			return err
		}
		logLine("применено: %s", rel)
	}
	if err := os.RemoveAll(p.Stage); err != nil {
		return err
	}
	logLine("черновик применён и очищен · файлов %d · дальше — git diff и коммит, как обычно", len(staged))
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
