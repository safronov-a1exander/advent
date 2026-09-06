// Команда advent — стенд для экспериментов с LLM по REST API.
//
// На ветке main здесь только инфраструктура: конфиг, доступ к API, журнал.
// Пользовательские команды появляются в ветках day-01 … day-NN.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/safronov-a1exander/advent/internal/config"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `advent — стенд для экспериментов с LLM по REST API

Использование:
  advent <команда> [флаги]

Команды:
  ask       одиночный запрос в LLM, ответ в консоль        (день 1)
  chat      интерактивный TUI с потоковым ответом          (день 1)
  demo      прогон сценария в TUI — для записи видео       (день 1)
  models    список моделей провайдера (живой запрос GET /models) и прайс из конфига
  doctor    проверка окружения: конфиг, ключ, доступность API
  version   версия сборки

Общие флаги:
  -provider  имя провайдера из config.yaml (по умолчанию default_provider)
  -dir       корень проекта с config.yaml (по умолчанию .)
`)
}

func run() error {
	if len(os.Args) < 2 {
		usage()
		return fmt.Errorf("не указана команда")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "ask":
		return cmdAsk(ctx, args)
	case "chat":
		return cmdChat(ctx, args)
	case "demo":
		return cmdDemo(ctx, args)
	case "models":
		return cmdModels(ctx, args)
	case "doctor":
		return cmdDoctor(ctx, args)
	case "version":
		fmt.Println("advent", version)
		return nil
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("неизвестная команда %q", cmd)
	}
}

type commonFlags struct {
	provider string
	dir      string
}

func bindCommon(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.provider, "provider", "", "провайдер из config.yaml")
	fs.StringVar(&c.dir, "dir", ".", "корень проекта с config.yaml")
	return c
}

func cmdModels(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("models", flag.ExitOnError)
	c := bindCommon(fs)
	if err := fs.Parse(args); err != nil {
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

	fmt.Printf("провайдер: %s (%s)\n\n", prov.Name, prov.BaseURL)

	fmt.Println("== из config.yaml (используется для расчёта стоимости) ==")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tКЛАСС\tIN $/1M\tCACHED $/1M\tOUT $/1M\tКОНТЕКСТ")
	for _, m := range prov.Models {
		fmt.Fprintf(w, "%s\t%s\t%.4f\t%.4f\t%.4f\t%d\n",
			m.ID, m.Tier, m.InPer1M, m.CachedIn1M, m.OutPer1M, m.MaxContext)
	}
	w.Flush()

	fmt.Println("\n== живой GET /models ==")
	ids, err := client.ListModels(ctx)
	if err != nil {
		fmt.Println("не удалось получить:", err)
		return nil
	}
	for _, id := range ids {
		fmt.Println(" ", id)
	}
	return nil
}

func cmdDoctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	c := bindCommon(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	fmt.Println("1) config.yaml …")
	cfg, err := config.Load(c.dir)
	if err != nil {
		return err
	}
	fmt.Printf("   ok, провайдеров: %d, дефолт: %s\n", len(cfg.Providers), cfg.DefaultProvider)

	fmt.Println("2) API-ключ …")
	client, prov, err := cfg.Client(c.provider)
	if err != nil {
		return err
	}
	fmt.Printf("   ok, провайдер %s, base_url %s\n", prov.Name, prov.BaseURL)

	fmt.Println("3) доступность API …")
	ids, err := client.ListModels(ctx)
	if err != nil {
		return fmt.Errorf("GET /models не прошёл: %w", err)
	}
	fmt.Printf("   ok, моделей доступно: %d\n", len(ids))
	fmt.Println("\nстенд готов")
	return nil
}
