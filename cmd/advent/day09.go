package main

// Дни 9–10 — сравнение стратегий контекста на одном диалоге.
//   advent dialog -scenario scenarios/day09-compression.yaml
//   advent dialog -scenario scenarios/day10-strategies.yaml
//
// Каждый вариант проходит один и тот же разговор отдельным агентом.
// В консоль — итоги и рост запроса по ходам, в reports/ — отчёт
// с ответами на вопросы-проверки и сводками.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/dialog"
	"github.com/safronov-a1exander/advent/internal/profile"
	"github.com/safronov-a1exander/advent/internal/store"
)

func cmdDialog(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dialog", flag.ExitOnError)
	c := bindCommon(fs)
	path := fs.String("scenario", "scenarios/day09-compression.yaml", "сценарий диалога")
	hold := fs.Duration("hold", 0, "подержать итог на экране перед выходом — для записи видео")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := dialog.Load(*path)
	if err != nil {
		return err
	}
	cfg, err := config.Load(c.dir)
	if err != nil {
		return err
	}
	client, prov, err := cfg.Client(c.provider)
	if err != nil {
		return err
	}
	runID := store.NewRunID("dialog-" + strings.TrimSuffix(filepath.Base(*path), filepath.Ext(*path)))
	journal, err := store.NewWriter(cfg.RunsDir, runID)
	if err != nil {
		return err
	}
	defer journal.Close()

	fmt.Printf("сценарий  %s · провайдер %s\n", *path, prov.Name)
	fmt.Printf("диалог    %d реплик, вариантов %d — идут параллельно\n\n", len(s.Dialog), len(s.Variants))

	// Пул сравнения: профили читаются из репозитория (день 12), а слоям
	// памяти хранилище нарочно не даётся (день 11) — варианты не должны
	// писать в одни файлы и подсматривать друг у друга. Слои живут
	// в процессе и умирают вместе с прогоном.
	pool := agent.NewPool(client, prov.Name, journal)
	pool.SetProfileStore(profile.NewFileStore(cfg.ProfilesDir))
	pool.SetCatalog(tiersOf(prov.Models))

	started := time.Now()
	var mu sync.Mutex
	done := map[string]int{}
	res, err := dialog.Run(ctx, pool, prov.Models, s,
		func(variant string, step, total int) {
			mu.Lock()
			defer mu.Unlock()
			done[variant] = step
			var parts []string
			for _, v := range s.Variants {
				name := v.Name
				parts = append(parts, fmt.Sprintf("%s %d/%d", name, done[name], total))
			}
			fmt.Printf("\r  %s   ", strings.Join(parts, " · "))
		})
	if err != nil {
		return err
	}
	fmt.Print("\n\n")

	printDialogTotals(res)
	printDialogGrowth(s, res)

	if err := os.MkdirAll(cfg.ReportsDir, 0o755); err != nil {
		return err
	}
	report := filepath.Join(cfg.ReportsDir, runID+".md")
	if err := os.WriteFile(report, []byte(dialog.Markdown(s, prov.Name, res, started)), 0o644); err != nil {
		return err
	}
	fmt.Printf("\nотчёт   %s\nжурнал  %s\n", report, journal.Path())
	if *hold > 0 {
		time.Sleep(*hold)
	}
	return nil
}

func printDialogTotals(res []dialog.Result) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ВАРИАНТ\tКОНТЕКСТ\tВХОД ВСЕГО\tИЗ КЭША\tСЛУЖЕБНЫЕ\tВЫХОД\tСЛУЖ. ВЫЗОВОВ\tПАМЯТЬ\t$")
	for _, r := range res {
		t := r.Totals()
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d/%d\t%.6f\n",
			r.Variant.Name, dialog.VariantLabel(r), t.Input(), t.Cached, t.AuxPrompt, t.Output(),
			t.AuxCalls, t.Passed, t.Checks, t.Cost)
	}
	w.Flush()
	if len(res) < 2 {
		return
	}
	base := res[0].Totals()
	if base.Input() == 0 {
		return
	}
	fmt.Println()
	for _, r := range res[1:] {
		other := r.Totals()
		fmt.Printf("%s против %s: вход %+d%%, из кэша %d против %d, память %d/%d против %d/%d\n",
			r.Variant.Name, res[0].Variant.Name,
			(other.Input()-base.Input())*100/base.Input(), other.Cached, base.Cached,
			other.Passed, other.Checks, base.Passed, base.Checks)
	}
}

// printDialogGrowth — запрос по ходам рядом для всех вариантов.
func printDialogGrowth(s *dialog.Scenario, res []dialog.Result) {
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	head := "ХОД\tРЕПЛИКА"
	for _, r := range res {
		head += "\t" + strings.ToUpper(r.Variant.Name)
	}
	fmt.Fprintln(w, head)
	for i, l := range s.Dialog {
		row := fmt.Sprintf("%d\t%s", i+1, cut(l.Text(), 44))
		for _, r := range res {
			cellText := "—"
			if i < len(r.Steps) {
				st := r.Steps[i]
				cellText = st.Brief(false)
				if st.Checked && !st.Passed && st.Err == "" {
					cellText += " " + strings.Join(st.Missing, ",")
				}
			}
			row += "\t" + cellText
		}
		fmt.Fprintln(w, row)
	}
	w.Flush()
}
