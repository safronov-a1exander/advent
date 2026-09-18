package main

// День 12 — персонализация ассистента.
//
//	advent profile                      какие профили есть
//	advent profile -show сеньор         что в профиле и какие у него дороги
//	advent profile -show сеньор -query "сервис упал"   какой дорогой пойдёт запрос
//	advent profile -init новый          заготовка, которую останется поправить
//
// Профиль — конфиг, а не данные, и живёт он в репозитории рядом с кодом.
// Поэтому команда его не правит (для этого есть редактор), а показывает:
// что уйдёт в промпт и куда свернёт конкретный запрос. Второе важнее:
// роутер дорог — единственное место в профиле, где поведение неочевидно
// из файла.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/profile"
)

func cmdProfile(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("profile", flag.ExitOnError)
	c := bindCommon(fs)
	dir := fs.String("profiles-dir", "", "каталог профилей (по умолчанию profiles_dir из config.yaml)")
	show := fs.String("show", "", "показать профиль по id")
	query := fs.String("query", "", "с -show: какой дорогой пойдёт такой запрос")
	initID := fs.String("init", "", "создать заготовку профиля с этим id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(c.dir)
	if err != nil {
		return err
	}
	root := *dir
	if root == "" {
		root = cfg.ProfilesDir
	}
	store := profile.NewFileStore(root)

	switch {
	case *initID != "":
		return initProfile(store, *initID)
	case *show != "":
		return showProfile(store, *show, *query)
	}
	return listProfiles(store, root)
}

func listProfiles(store *profile.FileStore, root string) error {
	fmt.Println("каталог профилей:", root)
	ids, err := store.List()
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		fmt.Println("\nпрофилей нет. Заготовка: advent profile -init мой-профиль")
		return nil
	}
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tИМЯ\tДОРОГИ")
	for _, id := range ids {
		p, err := store.Load(id)
		if err != nil {
			fmt.Fprintf(w, "%s\t— не прочитался: %v\t\n", id, err)
			continue
		}
		roads := make([]string, 0, len(p.Pipelines))
		for _, pl := range p.Pipelines {
			roads = append(roads, pl.Name)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", id, p.Name, strings.Join(roads, ", "))
	}
	w.Flush()
	fmt.Println("\nподробнее: advent profile -show <id> [-query \"текст запроса\"]")
	return nil
}

func showProfile(store *profile.FileStore, id, query string) error {
	p, err := store.Load(id)
	if err != nil {
		return err
	}
	fmt.Printf("== %s (%s) ==\n\n", p.Name, id)
	fmt.Println("Блок, который уйдёт в системный промпт каждого запроса:")
	fmt.Println()
	for _, line := range strings.Split(p.Block(), "\n") {
		fmt.Println("  " + line)
	}

	fmt.Println("\nДороги:")
	for i, pl := range p.Pipelines {
		mark := ""
		if i == 0 {
			mark = " (по умолчанию)"
		}
		fmt.Printf("\n  %s%s\n", pl.Name, mark)
		if len(pl.When) > 0 {
			fmt.Printf("    выбирается по словам: %s\n", strings.Join(pl.When, ", "))
		}
		if len(pl.Stages) > 0 {
			fmt.Printf("    стадии: %s\n", strings.Join(pl.Stages, " → "))
		}
		if pl.Strategy != "" {
			fmt.Printf("    стратегия рассуждения: %s\n", pl.Strategy)
		}
		if pl.Tier != "" {
			fmt.Printf("    класс модели: %s\n", pl.Tier)
		}
	}

	if strings.TrimSpace(query) != "" {
		pl, ok := p.Pick(query)
		fmt.Printf("\nЗапрос %q пойдёт дорогой: ", query)
		if !ok {
			fmt.Println("никакой — у профиля нет дорог")
			return nil
		}
		fmt.Printf("«%s»\n", pl.Name)
		if b := pl.StageBlock(); b != "" {
			fmt.Println("\nи получит вдобавок к профилю:")
			for _, line := range strings.Split(b, "\n") {
				fmt.Println("  " + line)
			}
		}
	}
	fmt.Println("\nфайл:", store.Path(id))
	return nil
}

// initProfile кладёт заготовку. Сочинять структуру YAML по документации —
// ровно тот барьер, из-за которого персонализацией не пользуются.
func initProfile(store *profile.FileStore, id string) error {
	if _, err := store.Load(id); err == nil {
		return fmt.Errorf("профиль %q уже есть: %s", id, store.Path(id))
	}
	p := &profile.Profile{
		ID:    id,
		Name:  id,
		About: "кто этот человек: чем занимается, какой опыт",
		Style: profile.Style{
			Address: "как к нему обращаться",
			Tone:    "формальный / разговорный / сухой",
			Length:  "коротко — три-пять предложений",
			Format:  "список / проза / с примерами кода",
			Level:   "что можно не объяснять",
		},
		Goal:   "зачем он это делает — от этого зависит, что считать хорошим ответом",
		Limits: []string{"чего делать нельзя", "о чём не писать"},
		Pipelines: []profile.Pipeline{
			{
				Name:   "по умолчанию",
				Stages: []string{"первая стадия", "вторая стадия"},
			},
			{
				Name:   "особый случай",
				When:   []string{"слово-триггер"},
				Stages: []string{"своя стадия"},
			},
		},
	}
	if err := store.Save(p); err != nil {
		return err
	}
	fmt.Println("заготовка:", store.Path(id))
	fmt.Println("поправь её и запусти: advent chat -profile", id)
	return nil
}
