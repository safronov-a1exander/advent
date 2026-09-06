package main

// День 2 — контроль формата ответа.
//   advent run -scenario scenarios/day02-format.yaml   прогон в консоль + отчёт
//   advent lab -scenario scenarios/day02-format.yaml   то же в TUI (для видео)

import (
	"context"
	"flag"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/report"
	"github.com/safronov-a1exander/advent/internal/runner"
	"github.com/safronov-a1exander/advent/internal/scenario"
	"github.com/safronov-a1exander/advent/internal/store"
	"github.com/safronov-a1exander/advent/internal/tui"
)

type runFlags struct {
	*commonFlags
	scenarioPath string
	model        string
	repeat       int
	runID        string
	noReport     bool
	full         bool
}

func bindRun(fs *flag.FlagSet) *runFlags {
	r := &runFlags{commonFlags: bindCommon(fs)}
	fs.StringVar(&r.scenarioPath, "scenario", "", "путь к YAML-сценарию")
	fs.StringVar(&r.model, "model", "", "прогнать все варианты на этой модели, игнорируя ту, что в сценарии")
	fs.IntVar(&r.repeat, "repeat", 0, "переопределить число повторов")
	fs.StringVar(&r.runID, "run", "", "имя журнала в runs/")
	fs.BoolVar(&r.noReport, "no-report", false, "не сохранять markdown-отчёт")
	fs.BoolVar(&r.full, "full", false, "печатать полные ответы, а не только таблицу")
	return r
}

func (r *runFlags) build() (*scenario.Scenario, *runner.Runner, *store.Writer, *config.Config, *config.Provider, error) {
	if r.scenarioPath == "" {
		return nil, nil, nil, nil, nil, fmt.Errorf("укажи -scenario scenarios/....yaml")
	}
	sc, err := scenario.Load(r.scenarioPath)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	// Флаг бьёт и модель варианта: иначе «прогони всё на flash» не работало бы
	// для сценариев, где модель задана у каждого варианта отдельно.
	modelOverride := r.model
	if r.repeat > 0 {
		sc.Repeat = r.repeat
	}

	cfg, err := config.Load(r.dir)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	provName := r.provider
	if provName == "" && sc.Provider != "" {
		provName = sc.Provider
	}
	client, prov, err := cfg.Client(provName)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	runID := r.runID
	if runID == "" {
		runID = store.NewRunID(sanitize(sc.Name))
	}
	w, err := store.NewWriter(cfg.RunsDir, runID)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	rn := &runner.Runner{
		Client:        client,
		Provider:      prov.Name,
		Store:         w,
		Fallback:      prov.DefaultMod,
		Models:        prov.PricingMap(),
		ModelOverride: modelOverride,
	}
	return sc, rn, w, cfg, prov, nil
}

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	f := bindRun(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	sc, rn, w, cfg, _, err := f.build()
	if err != nil {
		return err
	}
	defer w.Close()

	fmt.Printf("сценарий : %s\n", sc.Name)
	if sc.Description != "" {
		fmt.Printf("           %s\n", sc.Description)
	}
	fmt.Printf("провайдер: %s\nвариантов: %d × %d повтор(ов)\n\n",
		rn.Provider, len(sc.Variants), sc.Repeat)

	res, err := rn.Run(ctx, sc, func(ev runner.Event) {
		switch ev.Kind {
		case "variant-start":
			fmt.Printf("  → %-40s ", trunc(ev.Label, 40))
		case "variant-done":
			a := ev.Attempt
			if a.Err != nil {
				fmt.Printf("ошибка: %v\n", a.Err)
				return
			}
			mark := "ok"
			if !a.ChecksOK() {
				mark = "проверки провалены"
			}
			fmt.Printf("%6dмс  %4d→%4d ток  $%.6f  %s  %s\n",
				a.Latency.Milliseconds(), a.Usage.PromptTokens,
				a.Usage.CompletionTokens, a.CostUSD, a.Finish, mark)
		}
	})
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Print(report.Table(res))

	if f.full {
		fmt.Println()
		for _, a := range res.Attempts {
			fmt.Printf("\n─── %s ───\n%s\n", a.Label, a.Final)
			for _, c := range a.Checks {
				mark := "✔"
				if !c.OK {
					mark = "✘"
				}
				fmt.Printf("  %s %s %s\n", mark, c.Name, c.Detail)
			}
		}
	}

	fmt.Printf("\nжурнал: %s\n", w.Path())
	if !f.noReport {
		p, err := report.Save(cfg.ReportsDir, res)
		if err != nil {
			return err
		}
		fmt.Printf("отчёт : %s\n", p)
	}
	return nil
}

func cmdLab(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("lab", flag.ExitOnError)
	f := bindRun(fs)
	script := fs.String("script", "", "демо-сценарий для записи видео")
	if err := fs.Parse(args); err != nil {
		return err
	}
	sc, rn, w, cfg, prov, err := f.build()
	if err != nil {
		return err
	}
	defer w.Close()

	reportDir := cfg.ReportsDir
	if f.noReport {
		reportDir = ""
	}

	// Панель в lab работает слоем поверх сценария: пустое поле = «как в YAML».
	set := tui.NewSettings(prov.Models, "", "")
	set.Overlay = true
	if f.model != "" {
		set.Model = f.model
	}
	if f.repeat > 0 {
		set.Repeat = f.repeat
	}

	l := tui.NewLab(sc, rn, set, reportDir)
	p := tea.NewProgram(l, tea.WithContext(ctx))

	if *script != "" {
		acts, err := tui.ParseDemo(*script)
		if err != nil {
			return err
		}
		go tui.RunDemo(p, l, acts)
	}
	if _, err := p.Run(); err != nil {
		return err
	}
	// Путь журнала не печатаем: см. runTUI — строка попадала в конец записи.
	return nil
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func sanitize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "run"
	}
	return b.String()
}
