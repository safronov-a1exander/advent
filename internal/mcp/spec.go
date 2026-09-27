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

	// Command и Args — локальный сервер, запускаемый подпроцессом (день 17).
	// Команда "@self" — это сам advent: свои серверы стенда живут в том же
	// бинаре подкомандой mcp-server, и путь к нему не нужно прописывать.
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	// Env — дополнительные переменные окружения подпроцесса, тоже с ${VAR}.
	Env map[string]string `yaml:"env"`
}

// SelfCommand — команда запуска самого advent.
const SelfCommand = "@self"

// Remote — сервер удалённый.
func (s Spec) Remote() bool { return s.URL != "" }

// Where — адрес или команда для печати.
func (s Spec) Where() string {
	if s.Remote() {
		return s.URL
	}
	return "stdio: " + strings.TrimSpace(s.Command+" "+strings.Join(s.Args, " "))
}

// Open поднимает транспорт по описанию и проходит рукопожатие.
func Open(ctx context.Context, s Spec, opts ...Option) (*Client, error) {
	var t Transport
	switch {
	case s.Remote():
		headers := map[string]string{}
		for k, v := range s.Headers {
			if v = os.Expand(v, os.Getenv); strings.TrimSpace(v) != "" {
				headers[k] = v // переменная не задана — заголовок не шлём вовсе
			}
		}
		t = NewHTTP(s.URL, headers)
	case s.Command != "":
		cmd := s.Command
		if cmd == SelfCommand {
			exe, err := os.Executable()
			if err != nil {
				return nil, fmt.Errorf("mcp-сервер %q: не нашёл свой бинарь: %w", s.Name, err)
			}
			cmd = exe
		}
		var env []string
		for k, v := range s.Env {
			env = append(env, k+"="+os.Expand(v, os.Getenv))
		}
		st, err := NewStdio(cmd, s.Args, env)
		if err != nil {
			return nil, fmt.Errorf("mcp-сервер %q: %w", s.Name, err)
		}
		t = st
	default:
		return nil, fmt.Errorf("mcp-сервер %q: не задан ни url, ни command", s.Name)
	}
	return Connect(ctx, s.Name, t, opts...)
}
