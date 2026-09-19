package main

// День 14 — инварианты.
//
//	advent invariants                    какие наборы есть
//	advent invariants -show барбершоп    правила набора
//	advent invariants -show барбершоп -stage planning
//
// Команда показывает ровно то, что уйдёт в системный промпт, — и ничего
// не проверяет. Проверить ответ без модели нельзя: правило про смысл,
// а не про вхождение слова, и единственный способ узнать, нарушено ли
// оно, — спросить модель. Для этого есть чат и `advent dialog`.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/invariant"
)

func cmdInvariants(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("invariants", flag.ExitOnError)
	c := bindCommon(fs)
	dir := fs.String("invariants-dir", "", "каталог наборов (по умолчанию invariants_dir из config.yaml)")
	show := fs.String("show", "", "показать набор по id")
	stage := fs.String("stage", "", "какая стадия задачи: правила чужих стадий не действуют")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(c.dir)
	if err != nil {
		return err
	}
	root := *dir
	if root == "" {
		root = cfg.InvariantsDir
	}
	store := invariant.NewFileStore(root)

	if *show == "" {
		return listInvariants(store, root)
	}
	set, err := store.Load(*show)
	if err != nil {
		return err
	}

	fmt.Printf("== %s (%s) ==\n", set.Name, *show)
	if set.About != "" {
		fmt.Println(set.About)
	}
	fmt.Printf("\nповторов при нарушении: %d\n\n", set.RetriesN())

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ПРАВИЛО\tСТАДИИ\tДЕЙСТВУЕТ\tУТОЧНЕНИЕ ДЛЯ ПРОВЕРКИ")
	for _, inv := range set.Invariants {
		stages := "все"
		if len(inv.Stages) > 0 {
			stages = strings.Join(inv.Stages, ", ")
		}
		active := "да"
		if !inv.Active(*stage) {
			active = "нет"
		}
		ask := "—"
		if strings.TrimSpace(inv.Ask) != "" {
			ask = "есть"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", inv.Name, stages, active, ask)
	}
	w.Flush()

	fmt.Println("\nБлок, который уйдёт в системный промпт:")
	fmt.Println()
	block := invariant.Block(set.List(), *stage)
	if block == "" {
		fmt.Println("  (на этой стадии не действует ни одно правило)")
	}
	for _, line := range strings.Split(block, "\n") {
		fmt.Println("  " + line)
	}

	fmt.Println("\nКаталог:", store.Path(*show))
	return nil
}

func listInvariants(store *invariant.FileStore, root string) error {
	fmt.Println("каталог наборов:", root)
	ids, err := store.List()
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		fmt.Println("\nнаборов нет. Набор — это каталог с markdown-файлами правил:")
		fmt.Println("  invariants/<набор>/<правило>.md")
		return nil
	}
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tИМЯ\tПРАВИЛ")
	for _, id := range ids {
		set, err := store.Load(id)
		if err != nil {
			fmt.Fprintf(w, "%s\t— не прочитался: %v\t\n", id, err)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%d\n", id, set.Name, len(set.Invariants))
	}
	w.Flush()
	fmt.Println("\nподробнее: advent invariants -show <id>")
	return nil
}
