package main

// День 14 — инварианты.
//
//	advent invariants                       какие наборы есть
//	advent invariants -show барбершоп       правила и чем каждое проверяется
//	advent invariants -show барбершоп -check "Возьмём Python и MongoDB"
//	advent invariants -show барбершоп -stage planning
//
// Флаг -check прогоняет текст через быстрые фильтры, не обращаясь к модели.
// Это способ увидеть, что фильтр ловит, — и заодно напоминание, сколько он
// пропускает: подстрока не отличает «предлагаю» от «отказываюсь», а правила
// без фильтра не проверяет вовсе.

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
	check := fs.String("check", "", "прогнать этот текст через быстрые фильтры набора")
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
	fmt.Fprintln(w, "ПРАВИЛО\tБЫСТРЫЙ ФИЛЬТР\tСТАДИИ\tДЕЙСТВУЕТ")
	for _, inv := range set.Invariants {
		stages := "все"
		if len(inv.Stages) > 0 {
			stages = strings.Join(inv.Stages, ", ")
		}
		active := "да"
		if !inv.Active(*stage) {
			active = "нет"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", inv.Name, inv.Filters(), stages, active)
	}
	w.Flush()
	fmt.Println("\nФильтр — только предварительный отсев. Смысловую проверку каждого")
	fmt.Println("правила делает отдельная модель, и включается она режимом judge.")

	fmt.Println("\nФормулировки — они же уходят в промпт и в объяснение отказа:")
	for _, inv := range set.Invariants {
		fmt.Printf("  - %s\n", oneLine(inv.Rule))
	}

	if strings.TrimSpace(*check) != "" {
		fmt.Printf("\n== быстрые фильтры (без обращения к модели) ==\n\n")
		vs := invariant.Filter(*check, set.List(), *stage)
		if len(vs) == 0 {
			// «Ничего не нашли» легко прочитать как «всё чисто», а это не так:
			// фильтр отвечает только на вопрос «встретилось ли слово».
			fmt.Println("фильтры ничего не нашли — но это не значит, что нарушений нет")
			var unchecked []string
			for _, inv := range set.List() {
				if inv.Active(*stage) && !inv.HasFilter() {
					unchecked = append(unchecked, inv.Name)
				}
			}
			if len(unchecked) > 0 {
				fmt.Println("\nу этих правил фильтра нет вовсе — их смотрит только смысловая проверка:")
				for _, n := range unchecked {
					fmt.Printf("  - %s\n", n)
				}
			}
			fmt.Println("\nкаталог:", store.Path(*show))
			return nil
		}
		for _, v := range vs {
			fmt.Printf("✗ %s\n", v)
		}
		fmt.Println("\nчто получит модель на переспрос:")
		fmt.Println()
		for _, line := range strings.Split(invariant.Explain(vs), "\n") {
			fmt.Println("  " + line)
		}
	}
	fmt.Println("\nкаталог:", store.Path(*show))
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
	fmt.Fprintln(w, "ID\tИМЯ\tПРАВИЛ\tС ФИЛЬТРОМ")
	for _, id := range ids {
		set, err := store.Load(id)
		if err != nil {
			fmt.Fprintf(w, "%s\t— не прочитался: %v\t\t\n", id, err)
			continue
		}
		filtered := 0
		for _, inv := range set.Invariants {
			if inv.HasFilter() {
				filtered++
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\n", id, set.Name, len(set.Invariants), filtered)
	}
	w.Flush()
	fmt.Println("\nподробнее: advent invariants -show <id> [-check \"текст ответа\"]")
	return nil
}
