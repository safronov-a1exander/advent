// Package config собирает настройки из config.yaml + переменных окружения.
// Ключ API в файл не пишем никогда — только env или config.local.yaml (в .gitignore).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/mcp"
	"github.com/safronov-a1exander/advent/internal/rag"
	"gopkg.in/yaml.v3"
)

type Provider struct {
	Name       string          `yaml:"name"`
	BaseURL    string          `yaml:"base_url"`
	APIKeyEnv  string          `yaml:"api_key_env"`
	APIKey     string          `yaml:"api_key"` // только для config.local.yaml
	Models     []llm.ModelInfo `yaml:"models"`
	DefaultMod string          `yaml:"default_model"`
}

type Config struct {
	DefaultProvider string     `yaml:"default_provider"`
	Providers       []Provider `yaml:"providers"`
	RunsDir         string     `yaml:"runs_dir"`
	ReportsDir      string     `yaml:"reports_dir"`
	// SessionsDir — где лежат сохранённые разговоры агентов (день 7).
	SessionsDir string `yaml:"sessions_dir"`
	// ProfilesDir — где лежат профили пользователей (день 12). Профиль —
	// конфиг, а не данные: каталог ложится в репозиторий рядом с кодом.
	ProfilesDir string `yaml:"profiles_dir"`
	// InvariantsDir — где лежат наборы инвариантов (день 14). Как и профили,
	// это конфиг в репозитории, но область другая: профиль привязан
	// к пользователю, набор инвариантов — к проекту.
	InvariantsDir string `yaml:"invariants_dir"`
	// TasksDir — где лежат состояния задач (день 13). Отдельно от памяти:
	// память задачи отвечает «что мы выяснили», состояние — «где мы в ней».
	TasksDir string `yaml:"tasks_dir"`
	// MemoryDir — где лежат хранимые слои памяти: рабочая (по задаче) и
	// долговременная (по пользователю) (день 11). Отдельно от разговоров:
	// слои переживают разговор и принадлежат не ему.
	MemoryDir string `yaml:"memory_dir"`
	// MCPServers — MCP-серверы, к которым умеет подключаться стенд (день 16):
	// удалённые по адресу, локальные по команде запуска.
	MCPServers []mcp.Spec `yaml:"mcp_servers"`
	// Embedders — модели эмбеддингов (день 21): локальный llama.cpp, Ollama,
	// облако или заглушка. Отдельно от providers: у них другой эндпоинт
	// и другая цена, а чат-модель с ними не пересекается.
	Embedders []rag.EmbedderSpec `yaml:"embedders"`
	// Rerankers — кросс-энкодеры второго этапа поиска (день 23).
	Rerankers []rag.RerankerSpec `yaml:"rerankers"`
	// RAG — база знаний: что индексировать, как резать, где хранить (день 21).
	RAG RAGConfig `yaml:"rag"`
}

var ErrNoKey = errors.New("не найден API-ключ")

// Load читает config.yaml, затем накладывает config.local.yaml (если есть).
// Перед этим подхватывает .env — так ключ можно держать в файле рядом
// с проектом, не трогая переменные окружения системы.
func Load(dir string) (*Config, error) {
	if err := loadDotEnv(filepath.Join(dir, ".env")); err != nil {
		return nil, err
	}
	cfg := &Config{}
	base := filepath.Join(dir, "config.yaml")
	if err := readInto(base, cfg); err != nil {
		return nil, err
	}
	local := filepath.Join(dir, "config.local.yaml")
	if _, err := os.Stat(local); err == nil {
		overlay := &Config{}
		if err := readInto(local, overlay); err != nil {
			return nil, err
		}
		cfg.merge(overlay)
	}
	if cfg.RunsDir == "" {
		cfg.RunsDir = filepath.Join(dir, "runs")
	}
	if cfg.ReportsDir == "" {
		cfg.ReportsDir = filepath.Join(dir, "reports")
	}
	if cfg.ProfilesDir == "" {
		cfg.ProfilesDir = filepath.Join(dir, "profiles")
	}
	if cfg.InvariantsDir == "" {
		cfg.InvariantsDir = filepath.Join(dir, "invariants")
	}
	if cfg.TasksDir == "" {
		cfg.TasksDir = filepath.Join(dir, "tasks")
	}
	if cfg.MemoryDir == "" {
		cfg.MemoryDir = filepath.Join(dir, "memory")
	}
	if cfg.SessionsDir == "" {
		cfg.SessionsDir = filepath.Join(dir, "sessions")
	}
	if cfg.RAG.IndexDir == "" {
		cfg.RAG.IndexDir = filepath.Join(dir, "index")
	}
	if cfg.RAG.Strategy == "" {
		cfg.RAG.Strategy = rag.Structure
	}
	return cfg, nil
}

func readInto(path string, into *Config) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("читаю %s: %w", path, err)
	}
	return yaml.Unmarshal(b, into)
}

func (c *Config) merge(o *Config) {
	if o.DefaultProvider != "" {
		c.DefaultProvider = o.DefaultProvider
	}
	if o.RunsDir != "" {
		c.RunsDir = o.RunsDir
	}
	if o.ReportsDir != "" {
		c.ReportsDir = o.ReportsDir
	}
	if o.SessionsDir != "" {
		c.SessionsDir = o.SessionsDir
	}
	if o.RAG.Embedder != "" {
		c.RAG.Embedder = o.RAG.Embedder
	}
	if o.RAG.Reranker != "" {
		c.RAG.Reranker = o.RAG.Reranker
	}
	for _, r := range o.Rerankers {
		replaced := false
		for i := range c.Rerankers {
			if c.Rerankers[i].Name == r.Name {
				c.Rerankers[i], replaced = r, true
			}
		}
		if !replaced {
			c.Rerankers = append(c.Rerankers, r)
		}
	}
	// модели эмбеддингов из config.local.yaml заменяют одноимённые
	for _, e := range o.Embedders {
		replaced := false
		for i := range c.Embedders {
			if c.Embedders[i].Name == e.Name {
				c.Embedders[i], replaced = e, true
			}
		}
		if !replaced {
			c.Embedders = append(c.Embedders, e)
		}
	}
	// MCP-серверы из config.local.yaml заменяют одноимённые целиком,
	// а новые добавляются: так можно подставить свой адрес или заголовок
	// с ключом, не трогая общий config.yaml.
	for _, ms := range o.MCPServers {
		replaced := false
		for i := range c.MCPServers {
			if c.MCPServers[i].Name == ms.Name {
				c.MCPServers[i], replaced = ms, true
			}
		}
		if !replaced {
			c.MCPServers = append(c.MCPServers, ms)
		}
	}
	for _, op := range o.Providers {
		found := false
		for i := range c.Providers {
			if c.Providers[i].Name != op.Name {
				continue
			}
			found = true
			if op.BaseURL != "" {
				c.Providers[i].BaseURL = op.BaseURL
			}
			if op.APIKey != "" {
				c.Providers[i].APIKey = op.APIKey
			}
			if op.APIKeyEnv != "" {
				c.Providers[i].APIKeyEnv = op.APIKeyEnv
			}
			if op.DefaultMod != "" {
				c.Providers[i].DefaultMod = op.DefaultMod
			}
			if len(op.Models) > 0 {
				c.Providers[i].Models = op.Models
			}
		}
		if !found {
			c.Providers = append(c.Providers, op)
		}
	}
}

func (c *Config) Provider(name string) (*Provider, error) {
	if name == "" {
		name = c.DefaultProvider
	}
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i], nil
		}
	}
	return nil, fmt.Errorf("провайдер %q не найден в config.yaml", name)
}

// MCPServer — описание MCP-сервера по имени из mcp_servers.
func (c *Config) MCPServer(name string) (mcp.Spec, error) {
	var names []string
	for _, s := range c.MCPServers {
		if s.Name == name {
			return s, nil
		}
		names = append(names, s.Name)
	}
	return mcp.Spec{}, fmt.Errorf("mcp-сервер %q не найден в config.yaml (есть: %s)", name, strings.Join(names, ", "))
}

// Key достаёт ключ: сначала env (приоритет), затем config.local.yaml.
func (p *Provider) Key() (string, error) {
	if p.APIKeyEnv != "" {
		if v := strings.TrimSpace(os.Getenv(p.APIKeyEnv)); v != "" {
			return v, nil
		}
	}
	if v := strings.TrimSpace(p.APIKey); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%w: задай %s или api_key в config.local.yaml", ErrNoKey, p.APIKeyEnv)
}

func (p *Provider) PricingMap() map[string]llm.ModelInfo {
	m := make(map[string]llm.ModelInfo, len(p.Models))
	for _, mi := range p.Models {
		m[mi.ID] = mi
	}
	return m
}

// Client собирает готовый LLM-клиент по имени провайдера.
func (c *Config) Client(name string) (*llm.Client, *Provider, error) {
	p, err := c.Provider(name)
	if err != nil {
		return nil, nil, err
	}
	key, err := p.Key()
	if err != nil {
		return nil, nil, err
	}
	return llm.New(p.Name, p.BaseURL, key, llm.WithPricing(p.PricingMap())), p, nil
}

// ModelByTier ищет модель нужного класса (weak/medium/strong).
func (p *Provider) ModelByTier(tier string) (llm.ModelInfo, bool) {
	for _, m := range p.Models {
		if m.Tier == tier {
			return m, true
		}
	}
	return llm.ModelInfo{}, false
}

// RAGConfig — база знаний стенда (день 21).
type RAGConfig struct {
	// Embedder — имя модели из embedders по умолчанию.
	Embedder string `yaml:"embedder"`
	// Reranker — имя реранкера из rerankers по умолчанию (день 23).
	Reranker string `yaml:"reranker"`
	// IndexDir — где лежат индексы. Их можно пересобрать из документов,
	// поэтому каталог в .gitignore, как runs и reports.
	IndexDir string       `yaml:"index_dir"`
	Sources  rag.Sources  `yaml:"sources"`
	Chunking rag.Chunking `yaml:"chunking"`
	Strategy rag.Strategy `yaml:"strategy"`
}

// Embedder — клиент эмбеддингов по имени; пусто — rag.embedder.
func (c *Config) Embedder(name string) (*rag.HTTPEmbedder, error) {
	if name == "" {
		name = c.RAG.Embedder
	}
	var names []string
	for _, e := range c.Embedders {
		if e.Name == name {
			key := ""
			if e.APIKeyEnv != "" {
				key = strings.TrimSpace(os.Getenv(e.APIKeyEnv))
			}
			return rag.NewHTTPEmbedder(e, key), nil
		}
		names = append(names, e.Name)
	}
	return nil, fmt.Errorf("модель эмбеддингов %q не найдена в config.yaml (есть: %s)", name, strings.Join(names, ", "))
}

// Reranker — клиент реранкера по имени; пусто — rag.reranker (день 23).
func (c *Config) Reranker(name string) (*rag.HTTPReranker, error) {
	if name == "" {
		name = c.RAG.Reranker
	}
	var names []string
	for _, r := range c.Rerankers {
		if r.Name == name {
			key := ""
			if r.APIKeyEnv != "" {
				key = strings.TrimSpace(os.Getenv(r.APIKeyEnv))
			}
			return rag.NewHTTPReranker(r, key), nil
		}
		names = append(names, r.Name)
	}
	return nil, fmt.Errorf("реранкер %q не найден в config.yaml (есть: %s)", name, strings.Join(names, ", "))
}
