package main

// День 18 — агент, который сам присылает сводку по расписанию.
//   advent mcp-server rates -http 127.0.0.1:8765     сервер слежения: живёт отдельно, замеряет курс сам
//   advent watch -every 5m                           агент раз в пять минут собирает сводку через MCP
//   advent watch -once                               один раз и выйти — для cron / Планировщика задач
//
// У агента нет «пульса»: он просыпается, только когда его спрашивают, и
// просьба «присылай сводку каждый час» в промпте ничего не запустит.
// Поэтому расписание — снаружи агента, в двух местах: сервер сам копит
// замеры, а этот цикл сам будит агента. Ведущий курса так и сказал:
// «лучше крон повесить просто».

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/mcp"
	"github.com/safronov-a1exander/advent/internal/store"
)

const defaultWatchPrompt = "Собери сводку по всем валютам, за которыми следишь: что отслеживается, " +
	"как изменился курс за последний час, минимум и максимум. Коротко, по валюте на строку, " +
	"в конце — одна фраза, стоит ли что-то сделать с тратами."

const watchSystem = "Ты ассистент по личным финансам и раз в какое-то время сам присылаешь сводку. " +
	"Цифры бери только из инструментов, не придумывай. Отвечай по-русски, коротко."

func cmdWatch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	c := bindCommon(fs)
	every := fs.Duration("every", 5*time.Minute, "как часто будить агента")
	once := fs.Bool("once", false, "одна сводка и выход — для cron или Планировщика задач Windows")
	count := fs.Int("count", 0, "сколько сводок сделать и выйти (0 — работать, пока не остановят)")
	prompt := fs.String("prompt", defaultWatchPrompt, "что спрашивать у агента каждый раз")
	servers := fs.String("mcp", "rates-live", "MCP-серверы агента через запятую")
	model := fs.String("model", "", "модель (по умолчанию из config.yaml)")
	thinking := fs.String("thinking", "disabled", "режим рассуждений: enabled | disabled")
	out := fs.String("out", "runs/digests.md", "куда дописывать сводки")
	hold := fs.Duration("hold", 8*time.Second, "подержать последнюю сводку на экране перед выходом (для записи видео)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *once {
		*count = 1
	}
	cfg, err := config.Load(c.dir)
	if err != nil {
		return err
	}
	client, prov, err := cfg.Client(c.provider)
	if err != nil {
		return err
	}
	names := splitList(*servers)
	for _, s := range names {
		if _, err := cfg.MCPServer(s); err != nil {
			return fmt.Errorf("-mcp: %w", err)
		}
	}
	journal, err := store.NewWriter(cfg.RunsDir, store.NewRunID("watch"))
	if err != nil {
		return err
	}
	defer journal.Close()

	hub := mcp.NewHub(cfg.MCPServers)
	defer hub.Close()
	pool := agent.NewPool(client, prov.Name, journal)
	pool.SetToolbox(hub)
	acfg := agent.Config{Name: "сводка", Model: *model, System: watchSystem, Thinking: *thinking,
		Temperature: llm.F(0), MCP: names}
	if acfg.Model == "" {
		acfg.Model = prov.DefaultMod
	}

	fmt.Printf("агент-сводчик · %s / %s · MCP %s · каждые %s", prov.Name, acfg.Model, strings.Join(names, ", "), *every)
	if *count > 0 {
		fmt.Printf(" · сводок %d", *count)
	}
	fmt.Printf("\nсводки дописываются в %s · журнал %s\n", *out, journal.Path())

	for n := 1; ; n++ {
		digest(ctx, pool, acfg, *prompt, *out, n)
		if *count > 0 && n >= *count {
			if *count > 1 {
				time.Sleep(*hold)
			}
			return nil
		}
		next := time.Now().Add(*every)
		fmt.Printf("\n   … следующая сводка в %s\n", next.Format("15:04:05"))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Until(next)):
		}
	}
}

// digest — одна сводка. Каждый раз новый агент с чистой историей: сводке
// не нужны прошлые сводки, а тащить их за собой — значит платить за них
// в каждом следующем запросе. День 8 показал, чем это кончается.
func digest(ctx context.Context, pool *agent.Pool, cfg agent.Config, prompt, out string, n int) {
	at := time.Now()
	fmt.Printf("\n══ сводка %d · %s ══\n", n, at.Format("02.01.2006 15:04:05"))
	a := pool.SpawnTemp(cfg)
	defer pool.Remove(a.ID())

	reply, err := a.Ask(ctx, prompt, func(e agent.Event) {
		switch e.Kind {
		case agent.EventToolCall:
			fmt.Printf("   ▸ вызов %s %s\n", e.Label, cut(oneLine(e.Content), 100))
		case agent.EventToolResult:
			mark := "↳"
			if e.Failed {
				mark = "✗"
			}
			fmt.Printf("     %s %s\n", mark, cut(oneLine(e.Content), 140))
		}
	})
	if err != nil {
		fmt.Printf("   ✗ сводка не собралась: %v\n", err)
		appendDigest(out, at, "не собралась: "+err.Error(), "")
		return
	}
	text := strings.TrimSpace(reply.Final.Content)
	fmt.Println()
	for _, l := range strings.Split(text, "\n") {
		fmt.Println("   " + l)
	}
	_, cost := reply.Usage()
	var t agent.Turn
	if turns := a.Turns(); len(turns) > 0 {
		t = turns[len(turns)-1]
	}
	stat := fmt.Sprintf("вызовов API %d · инструментов %d (%s) · вход %d · выход %d · $%.6f",
		t.Calls, t.ToolCalls, agent.ToolTrace(reply.Tools), t.Prompt, t.Completion, cost)
	fmt.Printf("\n   %s\n", stat)
	appendDigest(out, at, text, stat)
}

// appendDigest дописывает сводку в markdown-файл: это и есть «присылает» —
// файл, который можно открыть, отправить или скормить дальше.
func appendDigest(path string, at time.Time, text, stat string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "## %s\n\n%s\n\n", at.Format("02.01.2006 15:04:05"), text)
	if stat != "" {
		fmt.Fprintf(f, "_%s_\n\n", stat)
	}
}
