package main

// День 11 — модель памяти агента.
//
//	advent memory                      что вообще лежит в памяти
//	advent memory -user саша           долговременный слой пользователя
//	advent memory -task бот            рабочий слой задачи
//	advent memory -user саша -put "стек = Go"
//	advent memory -task бот -forget срок
//
// Команда нужна не только для отладки. Курс прямо говорил, что рабочий слой
// можно держать хоть в трекере и забирать по апи; у нас он в файлах, и тогда
// его должно быть видно и править снаружи приложения — иначе «хранится
// отдельно» остаётся словами, а не свойством, которым можно пользоваться.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/memory"
)

func cmdMemory(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("memory", flag.ExitOnError)
	c := bindCommon(fs)
	dir := fs.String("memory-dir", "", "каталог слоёв памяти (по умолчанию memory_dir из config.yaml)")
	user := fs.String("user", "", "чей долговременный слой показать или править")
	task := fs.String("task", "", "какой задачи рабочий слой показать или править")
	put := fs.String("put", "", "положить запись: \"ключ = значение\"")
	forget := fs.String("forget", "", "убрать запись по ключу")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(c.dir)
	if err != nil {
		return err
	}
	root := *dir
	if root == "" {
		root = cfg.MemoryDir
	}
	store := memory.NewFileStore(root)

	// Ни юзера, ни задачи — показываем, что вообще есть. Так же, как
	// `advent sessions` без аргументов показывает список разговоров.
	if *user == "" && *task == "" {
		return listMemory(store, root)
	}

	scope, id := memory.ScopeUser, *user
	if *task != "" {
		if *user != "" {
			return fmt.Errorf("-user и -task правят разные слои: укажи что-то одно")
		}
		scope, id = memory.ScopeTask, *task
	}

	m := memory.New(store, *user, *task)
	if err := m.Restore(); err != nil {
		return err
	}

	switch {
	case *put != "" && *forget != "":
		return fmt.Errorf("-put и -forget вместе не работают")
	case *put != "":
		key, value, ok := strings.Cut(*put, "=")
		if !ok {
			return fmt.Errorf("-put: ожидали \"ключ = значение\", получили %q", *put)
		}
		if err := m.Put(scope, memory.Entry{
			Key: strings.TrimSpace(key), Value: strings.TrimSpace(value), Source: memory.SourceManual}); err != nil {
			return err
		}
		fmt.Printf("положено в слой %s (%s) для %q\n", scope, scope.Label(), id)
	case *forget != "":
		if _, ok := m.Layer(scope).Get(*forget); !ok {
			return fmt.Errorf("в слое %s для %q нет ключа %q", scope, id, *forget)
		}
		if err := m.Delete(scope, *forget); err != nil {
			return err
		}
		fmt.Printf("убрано из слоя %s (%s) для %q\n", scope, scope.Label(), id)
	}

	printLayer(m, scope, id, store.Path(scope, id))
	return nil
}

func printLayer(m *memory.Memory, scope memory.Scope, id, path string) {
	entries := m.Layer(scope).Entries()
	fmt.Printf("\n== %s (%s) · %s · записей %d ==\n", scope, scope.Label(), id, len(entries))
	fmt.Println(scope.Hint())
	if len(entries) == 0 {
		fmt.Println("\nпусто")
		return
	}
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "КЛЮЧ\tЗНАЧЕНИЕ\tПОЛОЖИЛ")
	for _, e := range entries {
		fmt.Fprintf(w, "%s\t%s\t%s\n", e.Key, oneLine(e.Value), e.Source)
	}
	w.Flush()
	fmt.Println("\nфайл:", path)
}

func listMemory(store *memory.FileStore, root string) error {
	fmt.Println("каталог памяти:", root)
	empty := true
	for _, scope := range []memory.Scope{memory.ScopeUser, memory.ScopeTask} {
		ids, err := store.List(scope)
		if err != nil {
			return err
		}
		fmt.Printf("\n== %s (%s) ==\n", scope, scope.Label())
		if len(ids) == 0 {
			fmt.Println("пусто")
			continue
		}
		empty = false
		for _, id := range ids {
			entries, err := store.Load(scope, id)
			if err != nil {
				fmt.Printf("  %-30s не прочитался: %v\n", id, err)
				continue
			}
			fmt.Printf("  %-30s записей %d\n", id, len(entries))
		}
	}
	// Краткосрочный слой в этот список не попадает, и это стоит сказать
	// вслух: он лежит в файле разговора и умирает вместе с ним.
	fmt.Println("\nкраткосрочный слой (chat) хранится в файле разговора — advent sessions -show <id>")
	if empty {
		fmt.Println("\nпамяти пока нет: запусти chat с -memory manual или -memory auto")
	} else {
		fmt.Println("подробнее: advent memory -user <id> | advent memory -task <id>")
	}
	return nil
}

// oneLine сводит значение в одну строку для таблицы.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
