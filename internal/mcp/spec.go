package mcp

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Spec — как добраться до сервера. Живёт в config.yaml в списке
// mcp_servers: у удалённого сервера адрес, у локального — команда запуска.
type Spec struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// URL — удалённый сервер по Streamable HTTP.
	URL string `yaml:"url"`
	// Headers уходят в каждый запрос к удалённому серверу. ${VAR} в значении
	// подставляется из окружения: ключам в config.yaml не место.
	Headers map[string]string `yaml:"headers"`
}

// Remote — сервер удалённый.
func (s Spec) Remote() bool { return s.URL != "" }

// Where — адрес для печати.
func (s Spec) Where() string { return s.URL }

// Open поднимает транспорт по описанию и проходит рукопожатие.
func Open(ctx context.Context, s Spec, opts ...Option) (*Client, error) {
	if !s.Remote() {
		return nil, fmt.Errorf("mcp-сервер %q: не задан url", s.Name)
	}
	headers := map[string]string{}
	for k, v := range s.Headers {
		headers[k] = os.Expand(v, os.Getenv)
	}
	for k, v := range headers {
		if strings.TrimSpace(v) == "" {
			delete(headers, k) // переменная не задана — заголовок не шлём вовсе
		}
	}
	return Connect(ctx, s.Name, NewHTTP(s.URL, headers), opts...)
}
