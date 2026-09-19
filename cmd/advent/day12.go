package main

// День 12 — персонализация ассистента.
//
//	advent profile                      какие профили есть
//	advent profile -show сеньор         что уйдёт в промпт и какие есть дороги
//	advent profile -init новый          заготовка, которую останется поправить
//
// Профиль — конфиг, а не данные, и живёт он в репозитории рядом с кодом.
// Поэтому команда его не правит (для этого есть редактор), а показывает
// ровно то, что получит модель. Какой дорогой пойдёт конкретный запрос,
// эта команда не отвечает: дорогу выбирает модель, и увидеть выбор можно
// только в чате или в `advent dialog`, где он печатается перед ответом.

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
		return initProfile(store, root, *initID)
	case *show != "":
		return showProfile(store, *show)
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
		if len(roads) == 0 {
			roads = []string{"—"}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", id, p.Name, strings.Join(roads, ", "))
	}
	w.Flush()
	fmt.Println("\nподробнее: advent profile -show <id>")
	return nil
}

func showProfile(store *profile.FileStore, id string) error {
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

	if len(p.Pipelines) == 0 {
		fmt.Println("\nДорог нет: все запросы идут одинаково.")
		fmt.Println("\nфайл:", store.Path(id))
		return nil
	}

	fmt.Println("\nДороги:")
	for i, pl := range p.Pipelines {
		mark := ""
		if i == 0 {
			mark = " (по умолчанию)"
		}
		fmt.Printf("\n  %s%s\n", pl.Name, mark)
		if w := strings.TrimSpace(pl.When); w != "" {
			fmt.Printf("    когда: %s\n", w)
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
	if p.NeedsChoice() {
		fmt.Println("\nДорог больше одной, поэтому под каждый запрос её выбирает")
		fmt.Println("короткий вызов модели — по смыслу запроса, а не по словам.")
	}
	fmt.Println("\nфайл:", store.Path(id))
	return nil
}

// initProfile кладёт заготовку. Сочинять формат по документации — ровно тот
// барьер, из-за которого персонализацией не пользуются.
const profileTemplate = `---
name: короткое имя для списков
pipelines:
  - name: по умолчанию
    stages: [первая стадия, вторая стадия]
  - name: особый случай
    when: когда эта дорога уместна — обычной прозой, это читает модель
    stages: [своя стадия]
    # tier: strong        # свой класс модели на эту дорогу
    # strategy: пошагово  # своя стратегия рассуждения
---

Здесь прозой: кто этот человек, как с ним разговаривать, чего делать
нельзя и зачем он вообще пришёл. Этот текст уходит в системный промпт
каждого запроса как есть — пишите его так, как объяснили бы живому
помощнику в первый день работы.

Например: тимлид, десять лет в бэкенде, пишет на Go. Отвечать сухо
и коротко, три-пять предложений. Базовое не объяснять. Не пересказывать
документацию и не начинать с «отличный вопрос». Если у варианта есть
цена — называть её в цифрах или в рисках.
`

func initProfile(store *profile.FileStore, root, id string) error {
	path := store.Path(id)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("профиль %q уже есть: %s", id, path)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(profileTemplate), 0o644); err != nil {
		return err
	}
	fmt.Println("заготовка:", path)
	fmt.Println("поправь её и запусти: advent chat -profile", id)
	return nil
}
