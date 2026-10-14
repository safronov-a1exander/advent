package main

import (
	"strings"
	"testing"

	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/mcp"
)

func TestIsOwnNetwork(t *testing.T) {
	for addr, want := range map[string]bool{
		"http://127.0.0.1:8080/v1":     true,
		"http://localhost:8080/v1":     true,
		"http://192.168.1.20:8090/v1":  true,
		"http://10.0.0.5/v1":           true,
		"https://api.deepseek.com/v1":  false,
		"https://mcp.context7.com/mcp": false,
		"http://8.8.8.8:8080/v1":       false,
	} {
		if got := isOwnNetwork(addr); got != want {
			t.Errorf("%s: %v, ждали %v", addr, got, want)
		}
	}
}

func TestOfflineCheck(t *testing.T) {
	cfg := &config.Config{MCPServers: []mcp.Spec{
		{Name: "budget", Command: "@self"},
		{Name: "context7", URL: "https://mcp.context7.com/mcp"},
	}}
	local := &config.Provider{BaseURL: "http://127.0.0.1:8080/v1"}

	where, err := offlineCheck(cfg, local, []string{"budget"}, false, "")
	if err != nil || !strings.Contains(where, "127.0.0.1:8080") || !strings.Contains(where, "budget") {
		t.Fatalf("локально: %q, %v", where, err)
	}
	if _, err := offlineCheck(cfg, local, []string{"budget", "context7"}, false, ""); err == nil || !strings.Contains(err.Error(), "context7") {
		t.Fatalf("облачный MCP пропущен: %v", err)
	}
	cloud := &config.Provider{BaseURL: "https://api.deepseek.com/v1"}
	if _, err := offlineCheck(cfg, cloud, nil, false, ""); err == nil {
		t.Fatal("облачная модель пропущена")
	}
}
