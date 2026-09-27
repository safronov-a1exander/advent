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
	"os"
	"strings"

	"github.com/safronov-a1exander/advent/internal/mcp"
	"github.com/safronov-a1exander/advent/internal/servers/rates"
)

// cmdMCPServer — свои серверы стенда. Живут в том же бинаре, поэтому
// в config.yaml достаточно написать command: "@self".
func cmdMCPServer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mcp-server", flag.ExitOnError)
	verbose := fs.Bool("log", false, "журнал вызовов в stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := fs.Arg(0)
	var srv *mcp.Server
	switch name {
	case "rates":
		srv = rates.New(rates.NewCBR())
	default:
		return fmt.Errorf("неизвестный сервер %q; есть: rates", name)
	}
	// stdout занят протоколом: любая строка туда ломает клиенту разбор.
	// Всё остальное — только в stderr.
	if *verbose {
		srv.Log = log.New(os.Stderr, name+": ", log.LstdFlags)
	}
	return srv.ServeStdio(ctx, os.Stdin, os.Stdout)
}

// callTool — один вызов инструмента руками, без модели: проверить сервер
// до того, как им начнёт пользоваться агент.
func callTool(ctx context.Context, cl *mcp.Client, tool, args string) error {
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	if !json.Valid([]byte(args)) {
		return fmt.Errorf("-args не JSON: %s", args)
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

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
