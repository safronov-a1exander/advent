package main

// День 13 — состояние задачи.
//
//	advent task                        какие задачи есть и где каждая стоит
//	advent task -show бот              стадия, план, что сделано, журнал
//	advent task -show бот -stage execution -note "план одобрен"
//	advent task -show бот -step "схема готова"
//
// День 15: -stage ходит через ту же карту переходов, что и агент. Прыжок
// через стадию отсюда не проходит, отказ печатается и попадает в журнал
// задачи — иначе запрет обходился бы одной командой.
//
// Команда нужна ровно затем же, зачем команда памяти на одиннадцатом дне:
// «пауза на любом этапе и продолжение без повторных объяснений» — свойство,
// которым должно быть можно пользоваться. Посмотреть, на чём остановились,
// не запуская чат, — самый частый случай.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/task"
)

func cmdTask(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("task", flag.ExitOnError)
	c := bindCommon(fs)
	dir := fs.String("tasks-dir", "", "каталог состояний задач (по умолчанию tasks_dir из config.yaml)")
	show := fs.String("show", "", "показать задачу по id")
	stage := fs.String("stage", "", "перевести в стадию: planning | execution | validation | done")
	note := fs.String("note", "", "к -stage: почему перешли (попадёт в журнал)")
	step := fs.String("step", "", "отметить текущий шаг сделанным с этим описанием")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(c.dir)
	if err != nil {
		return err
	}
	root := *dir
	if root == "" {
		root = cfg.TasksDir
	}
	store := task.NewFileStore(root)

	if *show == "" {
		return listTasks(store, root)
	}
	t, err := store.Load(*show)
	if err != nil {
		return err
	}
	if t == nil {
		return fmt.Errorf("задачи %q нет: %s", *show, store.Path(*show))
	}

	switch {
	case *stage != "" && *step != "":
		return fmt.Errorf("-stage и -step вместе не работают")
	case *stage != "":
		to := task.State(strings.ToLower(strings.TrimSpace(*stage)))
		// Проверка та же, что у агента: снаружи можно не больше, чем изнутри.
		// Иначе «нельзя перепрыгнуть этап» обходилось бы одной командой.
		err := t.Move(to, *note, task.DefaultTransitions)
		// Сохраняем в обоих случаях: отказ тоже записан в журнал задачи.
		if saveErr := store.Save(t); saveErr != nil {
			return saveErr
		}
		if err != nil {
			return err
		}
		fmt.Printf("стадия: %s\n\n", t.State)
	case *step != "":
		t.Complete(*step)
		if err := store.Save(t); err != nil {
			return err
		}
		fmt.Printf("шаг закрыт\n\n")
	}

	printTask(t, store.Path(*show))
	return nil
}

func printTask(t *task.Task, path string) {
	fmt.Printf("== %s ==\n\n%s\n", t.Title, t.Resume())
	// День 15: куда отсюда можно. Первое, что нужно знать, вернувшись
	// к задаче, — не «где я», а «что я могу сделать дальше».
	if allowed := task.DefaultTransitions.AllowedFrom(t.State); len(allowed) > 0 {
		names := make([]string, len(allowed))
		for i, st := range allowed {
			names[i] = string(st)
		}
		fmt.Printf("переходы: %s → %s\n", t.State, strings.Join(names, ", "))
	} else {
		fmt.Println("переходы: из этой стадии ходов нет — задача закрыта")
	}
	if n := t.Rejected(); n > 0 {
		fmt.Printf("отказов в переходах: %d\n", n)
	}
	if len(t.Plan) > 0 {
		fmt.Println("\nплан:")
		for i, s := range t.Plan {
			mark := " "
			if i+1 < t.Step {
				mark = "✓"
			} else if i+1 == t.Step {
				mark = "→"
			}
			fmt.Printf("  %s %d. %s\n", mark, i+1, s)
		}
	}
	if len(t.Done) > 0 {
		fmt.Println("\nсделано:")
		for _, s := range t.Done {
			fmt.Println("  -", s)
		}
	}
	if len(t.Log) > 0 {
		fmt.Println("\nжурнал стадий:")
		for _, e := range t.Log {
			line := "  " + e.At.Format("2006-01-02 15:04") + "  "
			switch {
			case e.From != "" && e.To != "":
				line += string(e.From) + " → " + string(e.To)
			case e.To != "":
				line += string(e.To)
			}
			if e.Note != "" {
				if !strings.HasSuffix(line, "  ") {
					line += " — "
				}
				line += e.Note
			}
			fmt.Println(line)
		}
	}
	fmt.Println("\nфайл:", path)
}

func listTasks(store *task.FileStore, root string) error {
	fmt.Println("каталог задач:", root)
	ids, err := store.List()
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		fmt.Println("\nзадач нет: запусти chat с -task-state auto -task \"имя задачи\"")
		return nil
	}
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tСТАДИЯ\tШАГ\tСЕЙЧАС")
	for _, id := range ids {
		t, err := store.Load(id)
		if err != nil {
			fmt.Fprintf(w, "%s\t— не прочиталась: %v\t\t\n", id, err)
			continue
		}
		steps := "—"
		if n := t.Total(); n > 0 {
			steps = fmt.Sprintf("%d/%d", t.Step, n)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", id, t.State, steps, t.Current)
	}
	w.Flush()
	fmt.Println("\nподробнее: advent task -show <id>")
	return nil
}
