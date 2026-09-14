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
	if cfg.SessionsDir == "" {
		cfg.SessionsDir = filepath.Join(dir, "sessions")
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
