package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Локальный конфиг заменяет одноимённый MCP-сервер целиком и добавляет
// новые — так свой адрес или заголовок с ключом не попадает в общий файл.
func TestLocalOverridesMCPServers(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("config.yaml", `
mcp_servers:
  - name: a
    url: https://a.example/mcp
  - name: b
    url: https://b.example/mcp
`)
	write("config.local.yaml", `
mcp_servers:
  - name: b
    url: http://127.0.0.1:9000/mcp
  - name: c
    url: https://c.example/mcp
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPServers) != 3 {
		t.Fatalf("серверы: %+v", cfg.MCPServers)
	}
	b, err := cfg.MCPServer("b")
	if err != nil || b.URL != "http://127.0.0.1:9000/mcp" {
		t.Fatalf("b не заменился: %+v %v", b, err)
	}
	if _, err := cfg.MCPServer("нет"); err == nil {
		t.Fatal("неизвестный сервер должен давать ошибку")
	}
}
