package main

// День 16 — подключение к MCP.
//   advent mcp                        все серверы из config.yaml: соединиться и показать инструменты
//   advent mcp -server deepwiki       один сервер по имени
//   advent mcp -url https://…/mcp     любой сервер по адресу, без записи в конфиг
//   advent mcp -wire                  плюс всё, что ходит по проводу (JSON-RPC)
//
// Модели здесь ещё нет: только соединение и список инструментов. Но каждый
// байт схемы, который печатается ниже, потом уедет в каждый запрос к модели,
// поэтому размер схемы показан сразу.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/mcp"
)

func cmdMCP(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	c := bindCommon(fs)
	server := fs.String("server", "", "имя сервера из mcp_servers в config.yaml (пусто — все)")
	url := fs.String("url", "", "адрес удалённого сервера (Streamable HTTP) — вместо -server")
	wire := fs.Bool("wire", false, "печатать JSON-RPC сообщения в обе стороны")
	full := fs.Bool("full", false, "печатать описания инструментов целиком")
	hold := fs.Duration("hold", 0, "подержать итог на экране перед выходом — для записи видео")
	timeout := fs.Duration("timeout", 30*time.Second, "сколько ждать один сервер")
	if err := fs.Parse(args); err != nil {
		return err
	}
	defer func() {
		if *hold > 0 {
			time.Sleep(*hold)
		}
	}()

	var specs []mcp.Spec
	switch {
	case *url != "":
		specs = []mcp.Spec{{Name: hostOf(*url), URL: *url}}
	default:
		cfg, err := config.Load(c.dir)
		if err != nil {
			return err
		}
		if *server != "" {
			s, err := cfg.MCPServer(*server)
			if err != nil {
				return err
			}
			specs = []mcp.Spec{s}
		} else {
			specs = cfg.MCPServers
		}
	}
	if len(specs) == 0 {
		return fmt.Errorf("в config.yaml нет mcp_servers — укажи -url")
	}

	failed := 0
	var total mcpTotals
	for i, s := range specs {
		if i > 0 {
			fmt.Println()
		}
		t, err := showServer(ctx, s, *wire, *full, *timeout)
		if err != nil {
			failed++
			fmt.Printf("   ✗ %v\n", err)
			continue
		}
		total.servers++
		total.tools += t.tools
		total.schema += t.schema
	}
	if len(specs) > 1 {
		fmt.Printf("\nитого: серверов %d из %d, инструментов %d, схема %s ≈ %d токенов на каждый запрос к модели\n",
			total.servers, len(specs), total.tools, bytesHuman(total.schema), schemaTokens(total.schema))
	}
	if failed == len(specs) {
		return fmt.Errorf("ни один сервер не ответил")
	}
	return nil
}

type mcpTotals struct {
	servers, tools, schema int
}

func showServer(ctx context.Context, s mcp.Spec, wire, full bool, timeout time.Duration) (mcpTotals, error) {
	fmt.Printf("== %s · %s\n", s.Name, s.Where())
	if s.Description != "" {
		fmt.Printf("   %s\n", s.Description)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var opts []mcp.Option
	if wire {
		opts = append(opts, mcp.WithTrace(printWire))
	}
	cl, err := mcp.Open(ctx, s, opts...)
	if err != nil {
		return mcpTotals{}, err
	}
	defer cl.Close()

	info := cl.Info()
	session := "нет"
	if h, ok := transportSession(cl); ok && h != "" {
		session = cut(h, 16)
	}
	fmt.Printf("   соединение  ok за %d мс · протокол %s · сервер %s %s · сессия %s\n",
		cl.Elapsed.Milliseconds(), info.ProtocolVersion, info.Server.Name, info.Server.Version, session)
	var caps []string
	for k := range info.Capabilities {
		caps = append(caps, k)
	}
	slices.Sort(caps)
	fmt.Printf("   возможности %s\n", strings.Join(caps, ", "))
	if info.Instructions != "" {
		fmt.Printf("   инструкция  %s (%d симв.)\n", cut(oneLine(info.Instructions), 90), utf8.RuneCountInString(info.Instructions))
	}

	start := time.Now()
	tools, err := cl.ListTools(ctx)
	if err != nil {
		return mcpTotals{}, err
	}
	schema := schemaSize(tools)
	fmt.Printf("   tools/list  %d мс · инструментов %d · схема %s ≈ %d токенов\n",
		time.Since(start).Milliseconds(), len(tools), bytesHuman(schema), schemaTokens(schema))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, t := range tools {
		var ps []string
		for _, p := range t.Params() {
			name := p.Name
			if p.Required {
				name += "*"
			}
			if p.Type != "" {
				name += ": " + p.Type
			}
			ps = append(ps, name)
		}
		desc := oneLine(t.Description)
		if !full {
			desc = cut(desc, 70)
		}
		fmt.Fprintf(w, "   • %s(%s)\t%s\n", t.Name, strings.Join(ps, ", "), desc)
	}
	w.Flush()
	return mcpTotals{tools: len(tools), schema: schema}, nil
}

// transportSession — id сессии Streamable HTTP, если сервер его выдал.
func transportSession(cl *mcp.Client) (string, bool) {
	s, ok := cl.Transport().(interface{ Session() string })
	if !ok {
		return "", false
	}
	return s.Session(), true
}

// printWire — одно сообщение протокола в одну строку, длинные обрезаны.
func printWire(dir mcp.Direction, raw []byte) {
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		buf.Reset()
		buf.Write(raw)
	}
	fmt.Printf("   %s %s\n", dir, cut(buf.String(), 150))
}

// schemaSize — сколько байт займут описания инструментов, когда их
// отдадут модели: имя, описание и схема аргументов каждого.
func schemaSize(tools []mcp.Tool) int {
	n := 0
	for _, t := range tools {
		b, _ := json.Marshal(struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		}{t.Name, t.Description, t.InputSchema})
		n += len(b)
	}
	return n
}

// schemaTokens — грубая оценка: схемы почти целиком латиница и JSON,
// а на них выходит около четырёх байт на токен. Точную цифру покажет
// usage провайдера, когда схема уйдёт модели.
func schemaTokens(bytes int) int { return (bytes + 3) / 4 }

func bytesHuman(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d Б", n)
	}
	return fmt.Sprintf("%.1f КБ", float64(n)/1024)
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexAny(u, "/:"); i > 0 {
		u = u[:i]
	}
	return u
}
