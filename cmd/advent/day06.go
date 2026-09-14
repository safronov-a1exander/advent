package main

// День 6 — агент как отдельная сущность.
//   advent swarm -fleet agents/fleet.yaml
//
// Проверочный вопрос дня — «можно ли моментально поднять много агентов
// с разными конфигами». Команда отвечает на него буквально: читает флот
// из YAML, порождает агентов в одном процессе, задаёт всем один вопрос
// и печатает, как ответила каждая группа. Размер флота — replicas в файле.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/store"
)

func cmdSwarm(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("swarm", flag.ExitOnError)
	c := bindCommon(fs)
	fleetPath := fs.String("fleet", "agents/fleet.yaml", "файл флота")
	question := fs.String("question", "", "вопрос всем агентам (по умолчанию из файла флота)")
	parallel := fs.Int("parallel", 0, "сколько запросов одновременно (0 — из файла флота)")
	hold := fs.Duration("hold", 0, "подержать итог на экране перед выходом — для записи видео")
	if err := fs.Parse(args); err != nil {
		return err
	}

	fleet, err := agent.LoadFleet(*fleetPath)
	if err != nil {
		return err
	}
	if *question != "" {
		fleet.Question = *question
	}
	if strings.TrimSpace(fleet.Question) == "" {
		return fmt.Errorf("нечего спрашивать: задай question в %s или флаг -question", *fleetPath)
	}
	if *parallel > 0 {
		fleet.Parallel = *parallel
	}

	cfg, err := config.Load(c.dir)
	if err != nil {
		return err
	}
	client, prov, err := cfg.Client(c.provider)
	if err != nil {
		return err
	}
	configs, err := fleet.Configs(prov.Models)
	if err != nil {
		return err
	}
	journal, err := store.NewWriter(cfg.RunsDir, store.NewRunID("swarm"))
	if err != nil {
		return err
	}
	defer journal.Close()

	fmt.Printf("флот    %s · провайдер %s\n", *fleetPath, prov.Name)
	fmt.Printf("вопрос  %s\n\n", fleet.Question)

	// --- порождение ---
	pool := agent.NewPool(client, prov.Name, journal)
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	agents := make([]*agent.Agent, 0, len(configs))
	for _, ac := range configs {
		agents = append(agents, pool.Spawn(ac))
	}
	spawnTook := time.Since(start)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	perAgent := int64(after.TotalAlloc-before.TotalAlloc) / int64(max(len(agents), 1))

	took := spawnTook.Round(time.Microsecond).String()
	if spawnTook < time.Millisecond {
		// таймер Windows грубее микросекунды: флот порождается
		// быстрее, чем он успевает тикнуть
		took = "меньше миллисекунды"
	}
	fmt.Printf("порождено %d агентов за %s в одном процессе · ~%d байт на агента\n",
		pool.Len(), took, perAgent)
	fmt.Printf("первый %s · последний %s\n\n", agents[0].ID(), agents[len(agents)-1].ID())

	// --- опрос ---
	par := fleet.Parallel
	if par <= 0 || par > len(agents) {
		par = len(agents)
	}
	fmt.Printf("опрос: одновременно не больше %d запросов\n", par)
	var done atomic.Int32
	total := len(agents)
	askStart := time.Now()
	results := pool.AskAll(ctx, agents, fleet.Question, par, func(agent.Result) {
		n := done.Add(1)
		// \r перерисовывает строку прогресса на месте
		fmt.Printf("\r  готово %3d / %d", n, total)
	})
	wall := time.Since(askStart)
	fmt.Print("\n\n")

	printFleetTable(fleet, results)

	// --- итог ---
	var latencySum time.Duration
	isolated := true
	for _, r := range results {
		if r.Err == nil {
			latencySum += r.Reply.Final.Latency
		}
		if want := 2; r.Err == nil && len(r.Agent.History()) != want {
			isolated = false
		}
	}
	spent := pool.Spent()
	speedup := 0.0
	if wall > 0 {
		speedup = latencySum.Seconds() / wall.Seconds()
	}
	fmt.Println()
	fmt.Printf("итого   агентов %d · вызовов %d · ошибок %d · in %d · out %d · $%.6f\n",
		pool.Len(), spent.Calls, spent.Errors, spent.Prompt, spent.Completion, spent.CostUSD)
	fmt.Printf("время   стена %s · сумма задержек %s · выигрыш от параллельности ×%.1f\n",
		wall.Round(10*time.Millisecond), latencySum.Round(10*time.Millisecond), speedup)
	if isolated {
		fmt.Println("история у каждого агента своя: ровно 2 сообщения (вопрос + его ответ) ✓")
	} else {
		fmt.Println("ВНИМАНИЕ: у части агентов в истории не 2 сообщения — изоляция нарушена")
	}
	fmt.Printf("журнал  %s\n", journal.Path())

	if *hold > 0 {
		time.Sleep(*hold)
	}
	return nil
}

// printFleetTable — сводка по группам флота. Агентов в группе может быть
// сколько угодно: построчный вывод утонул бы в одинаковых строках.
func printFleetTable(fleet *agent.Fleet, results []agent.Result) {
	type group struct {
		name, model, params string
		n, ok, errs, right  int
		latency             time.Duration
		in, out             int
		cost                float64
		sample, firstErr    string
	}
	groups := map[string]*group{}
	var order []string
	for _, r := range results {
		cfg := r.Agent.Config()
		g, seen := groups[cfg.Name]
		if !seen {
			// Флот всегда отвечает без стриминга — в сводке это только шум.
			shown := cfg
			shown.Stream = true
			g = &group{name: cfg.Name, model: cfg.Model, params: shown.Summary()}
			groups[cfg.Name] = g
			order = append(order, cfg.Name)
		}
		g.n++
		if r.Err != nil {
			g.errs++
			if g.firstErr == "" {
				g.firstErr = r.Err.Error()
			}
			continue
		}
		resp := r.Reply.Final
		g.ok++
		g.latency += resp.Latency
		g.in += resp.Usage.PromptTokens
		g.out += resp.Usage.CompletionTokens
		g.cost += resp.CostUSD
		if fleet.Expect != "" && strings.Contains(resp.Content, fleet.Expect) {
			g.right++
		}
		if g.sample == "" {
			g.sample = strings.Join(strings.Fields(resp.Content), " ")
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	head := "ГРУППА\tАГЕНТОВ\tМОДЕЛЬ\tПАРАМЕТРЫ\tОК\tОШИБОК"
	if fleet.Expect != "" {
		head += "\tВЕРНО"
	}
	fmt.Fprintln(w, head+"\tСР.МС\tIN\tOUT\t$")
	for _, name := range order {
		g := groups[name]
		avg := "—"
		if g.ok > 0 {
			avg = fmt.Sprint((g.latency / time.Duration(g.ok)).Milliseconds())
		}
		row := fmt.Sprintf("%s\t%d\t%s\t%s\t%d\t%d", g.name, g.n, g.model, orDashStr(g.params), g.ok, g.errs)
		if fleet.Expect != "" {
			row += fmt.Sprintf("\t%d/%d", g.right, g.ok)
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%.6f\n", row, avg, g.in, g.out, g.cost)
	}
	w.Flush()

	fmt.Println()
	for _, name := range order {
		g := groups[name]
		switch {
		case g.sample != "":
			fmt.Printf("  %-12s → %s\n", g.name, cut(g.sample, 100))
		case g.firstErr != "":
			fmt.Printf("  %-12s ✗ %s\n", g.name, cut(g.firstErr, 100))
		}
	}
}

func orDashStr(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
