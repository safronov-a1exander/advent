package main

// День 17 — свой MCP-сервер и первый вызов инструмента агентом.
//   advent mcp-server rates                         сервер «Курсы» на stdio (его запускает клиент)
//   advent mcp -server rates                        соединиться со своим сервером, как с любым другим
//   advent mcp -server rates -call convert -args '{"amount":10,"from":"USD"}'
//   advent chat -mcp rates                          агент сам решает, когда звать инструмент

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/mcp"
	"github.com/safronov-a1exander/advent/internal/servers/rates"
)

// cmdMCPServer — свои серверы стенда. Живут в том же бинаре, поэтому
// в config.yaml достаточно написать command: "@self".
func cmdMCPServer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mcp-server", flag.ExitOnError)
	verbose := fs.Bool("log", false, "журнал вызовов в stderr")
	httpAddr := fs.String("http", "", "слушать Streamable HTTP на адресе вместо stdio, например 127.0.0.1:8765 (день 18)")
	data := fs.String("data", "runs/mcp/rates-watch.json", "файл заданий слежения и замеров (день 18)")
	market := fs.String("market", "coinbase", "откуда брать рыночный курс для слежения: coinbase | walk (подставной, для репетиций)")
	// Имя сервера — первый аргумент, флаги можно писать и после него.
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	var srv *mcp.Server
	var tracker *rates.Tracker
	switch name {
	case "rates":
		var m rates.Market = rates.NewCoinbase()
		switch *market {
		case "coinbase":
		case "walk":
			m = &rates.Walk{}
		default:
			return fmt.Errorf("-market: coinbase или walk, а не %q", *market)
		}
		tr, err := rates.NewTracker(*data, m)
		if err != nil {
			return err
		}
		tracker = tr
		srv = rates.WithTracker(rates.New(rates.NewCBR()), tr)
	default:
		return fmt.Errorf("неизвестный сервер %q; есть: rates", name)
	}
	// stdout занят протоколом: любая строка туда ломает клиенту разбор.
	// Всё остальное — только в stderr.
	logger := log.New(os.Stderr, name+": ", log.LstdFlags)
	if *verbose {
		srv.Log = logger
	}
	if tracker != nil {
		if *verbose || *httpAddr != "" {
			tracker.Log = logger.Printf
		}
		// Планировщик живёт, пока жив процесс. На stdio это время жизни
		// клиента; чтобы следить 24/7, сервер запускают по HTTP отдельно.
		go tracker.Run(ctx, 5*time.Second)
	}
	if *httpAddr == "" {
		return srv.ServeStdio(ctx, os.Stdin, os.Stdout)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", srv.HTTPHandler())
	hs := &http.Server{Addr: *httpAddr, Handler: mux}
	go func() {
		<-ctx.Done()
		hs.Close()
	}()
	logger.Printf("слушаю http://%s/mcp · задания и замеры в %s", *httpAddr, *data)
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// callTool — один вызов инструмента руками, без модели: проверить сервер
// до того, как им начнёт пользоваться агент.
func callTool(ctx context.Context, cl *mcp.Client, tool, args string) error {
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	if !json.Valid([]byte(args)) {
		// PowerShell 5.1 срезает кавычки у аргументов внешних программ,
		// и JSON доезжает до нас как {amount:10,from:USD}. Поэтому кроме
		// JSON понимаем и простую запись: amount=10,from=USD.
		kv, err := parseKV(args)
		if err != nil {
			return fmt.Errorf("-args: ни JSON, ни ключ=значение: %s", args)
		}
		args = kv
	}
	res, err := cl.CallTool(ctx, tool, json.RawMessage(args))
	if err != nil {
		return err
	}
	mark := "✓"
	if res.IsError {
		mark = "✗ ошибка инструмента"
	}
	fmt.Printf("   tools/call  %s %s → %s\n", tool, args, mark)
	for _, l := range strings.Split(res.Text(), "\n") {
		fmt.Printf("     %s\n", l)
	}
	if len(res.StructuredContent) > 0 {
		fmt.Printf("     structuredContent: %s\n", res.StructuredContent)
	}
	return nil
}

// parseKV — «amount=10,from=USD» или «amount=10 from=USD» в JSON-объект.
// Числа и true/false становятся числами и булевыми, остальное — строками.
func parseKV(s string) (string, error) {
	s = strings.Trim(strings.TrimSpace(s), "{}")
	obj := map[string]any{}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			k, v, ok = strings.Cut(part, ":")
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" {
			return "", fmt.Errorf("не пара ключ=значение: %q", part)
		}
		var x any
		if json.Unmarshal([]byte(v), &x) == nil {
			obj[k] = x
		} else {
			obj[k] = v
		}
	}
	b, err := json.Marshal(obj)
	return string(b), err
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
