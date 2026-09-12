package main

// День 7 — разговоры переживают перезапуск.
//   advent chat                   открывает последний сохранённый разговор
//   advent chat -new              новый разговор, старые остаются в списке
//   advent chat -session <id>     открыть конкретный
//   advent sessions               что сохранено
//   advent sessions -show <id>    переписка разговора целиком

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/config"
)

type sessionFlags struct {
	path   string
	resume string
	fresh  bool
	noSave bool
}

func bindSessions(fs *flag.FlagSet) *sessionFlags {
	s := &sessionFlags{}
	fs.StringVar(&s.path, "sessions", "", "каталог сохранённых разговоров (по умолчанию sessions_dir из config.yaml)")
	fs.StringVar(&s.resume, "session", "", "открыть разговор с этим id")
	fs.BoolVar(&s.fresh, "new", false, "начать новый разговор, даже если есть сохранённые")
	fs.BoolVar(&s.noSave, "no-save", false, "не сохранять разговоры и не поднимать старые")
	return s
}

func (s *sessionFlags) dir(cfg *config.Config) string {
	if s.path != "" {
		return s.path
	}
	return cfg.SessionsDir
}

func cmdSessions(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ExitOnError)
	c := bindCommon(fs)
	sf := bindSessions(fs)
	show := fs.String("show", "", "показать переписку разговора с этим id")
	hold := fs.Duration("hold", 0, "подержать вывод на экране перед выходом — для записи видео")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(c.dir)
	if err != nil {
		return err
	}
	store := agent.NewFileStore(sf.dir(cfg))
	snaps, loadErr := store.LoadAll()
	defer func() {
		if *hold > 0 {
			time.Sleep(*hold)
		}
	}()

	if *show != "" {
		for _, s := range snaps {
			if s.ID == *show {
				printSession(store, s)
				return nil
			}
		}
		return fmt.Errorf("разговора %s в %s нет", *show, store.Dir())
	}

	fmt.Printf("сохранённые разговоры · %s\n\n", store.Dir())
	if len(snaps) == 0 {
		fmt.Println("пока пусто: разговоры появятся после первого вопроса в advent chat")
	} else {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tОБНОВЛЁН\tПРОВАЙДЕР\tМОДЕЛЬ\tСООБЩЕНИЙ\tТОКЕНОВ IN/OUT\tПЕРВЫЙ ВОПРОС")
		for _, s := range snaps {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d / %d\t%s\n",
				s.ID, s.Updated.Format("02.01 15:04"), orDashStr(s.Provider), s.Config.Model, len(s.History),
				s.Stats.Prompt, s.Stats.Completion, cut(firstQuestion(s), 50))
		}
		w.Flush()
	}
	if loadErr != nil {
		fmt.Printf("\nне прочитались: %v\n", loadErr)
	}
	return nil
}

// printSession — переписка целиком и откуда она взялась: видно, что после
// перезапуска агенту уйдёт ровно это.
func printSession(store *agent.FileStore, s agent.Snapshot) {
	fmt.Printf("разговор %s · %s\n", s.ID, store.Path(s.ID))
	fmt.Printf("создан %s · обновлён %s · модель %s\n",
		s.Created.Format("02.01 15:04:05"), s.Updated.Format("02.01 15:04:05"), s.Config.Model)
	if sys := strings.TrimSpace(s.Config.System); sys != "" {
		fmt.Printf("system: %s\n", cut(strings.Join(strings.Fields(sys), " "), 100))
	}
	fmt.Println()
	for i, m := range s.History {
		fmt.Printf("%2d %-9s %s\n", i+1, m.Role, cut(strings.Join(strings.Fields(m.Content), " "), 110))
	}
	fmt.Printf("\nходов %d · вызовов %d · in %d · out %d · $%.6f\n",
		s.Stats.Turns, s.Stats.Calls, s.Stats.Prompt, s.Stats.Completion, s.Stats.CostUSD)
}

func firstQuestion(s agent.Snapshot) string {
	for _, m := range s.History {
		if m.Role == "user" {
			return strings.Join(strings.Fields(m.Content), " ")
		}
	}
	return "—"
}
