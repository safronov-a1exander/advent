package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/safronov-a1exander/advent/internal/config"
)

// День 27: приложение целиком без облака. -offline проверяет до старта
// каждый адрес, куда чат может пойти: модель, MCP-серверы, эмбеддинги
// и реранкер базы знаний. Своя сеть — loopback и частные диапазоны
// (домашний сервер дня 30 тоже «свой»); всё остальное — облако, и чат
// не запускается, а не молча уходит в интернет.

// offlineCheck возвращает строку для ленты — что и где работает —
// или ошибку со списком облачных адресов.
func offlineCheck(cfg *config.Config, prov *config.Provider, mcpServers []string, useRAG bool, embedder string) (string, error) {
	var where, cloud []string
	add := func(what, addr string) {
		if isOwnNetwork(addr) {
			where = append(where, what+" "+addrOf(addr))
		} else {
			cloud = append(cloud, fmt.Sprintf("%s → %s", what, addr))
		}
	}
	add("модель", prov.BaseURL)
	for _, name := range mcpServers {
		s, err := cfg.MCPServer(name)
		if err != nil {
			return "", err
		}
		if s.URL != "" {
			add("mcp "+name, s.URL)
		} else {
			// Подпроцесс на этой машине. Ходит ли он сам в сеть, снаружи
			// не видно: rates, например, берёт курсы с cbr.ru.
			where = append(where, "mcp "+name+" (процесс)")
		}
	}
	if useRAG {
		if embedder == "" {
			embedder = cfg.RAG.Embedder
		}
		for _, e := range cfg.Embedders {
			if e.Name == embedder {
				add("эмбеддинги", e.BaseURL)
			}
		}
		for _, r := range cfg.Rerankers {
			if r.Name == cfg.RAG.Reranker {
				add("реранкер", r.BaseURL)
			}
		}
	}
	if len(cloud) > 0 {
		return "", fmt.Errorf("-offline: в облако ведут %s", strings.Join(cloud, "; "))
	}
	return "офлайн: " + strings.Join(where, " · "), nil
}

// isOwnNetwork — адрес на этой машине или в локальной сети.
func isOwnNetwork(raw string) bool {
	h := hostOf(raw)
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// addrOf — хост с портом, для ленты: «127.0.0.1:8080».
func addrOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}
