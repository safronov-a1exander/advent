package agent

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/safronov-a1exander/advent/internal/llm"
)

// Fleet — описание группы агентов в YAML.
//
//	question: "Сколько я потратил на кафе?"
//	parallel: 5
//	defaults:            # общее для всех
//	  system: "Ты ассистент по личным финансам…"
//	agents:
//	  - name: точный
//	    tier: weak
//	    temperature: 0
//	    replicas: 4       # четыре одинаковых агента
//	  - name: творческий
//	    temperature: 1.2
//	    replicas: 3
//
// Группа наследует defaults и переопределяет только своё — поэтому флот
// любого размера описывается десятком строк: масштаб задают replicas.
type Fleet struct {
	Question string `yaml:"question"`
	// Expect — подстрока верного ответа; если задана, в сводке видно,
	// сколько агентов каждой группы ответили правильно.
	Expect   string       `yaml:"expect"`
	Parallel int          `yaml:"parallel"`
	Defaults Config       `yaml:"defaults"`
	Agents   []FleetGroup `yaml:"agents"`
}

// FleetGroup — одна группа одинаково настроенных агентов.
type FleetGroup struct {
	Config   `yaml:",inline"`
	Replicas int `yaml:"replicas"`
}

// LoadFleet читает файл флота.
func LoadFleet(path string) (*Fleet, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f Fleet
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(f.Agents) == 0 {
		return nil, fmt.Errorf("%s: пустой список agents", path)
	}
	return &f, nil
}

// Configs разворачивает группы в плоский список конфигов: defaults,
// поверх них поля группы, модель по классу — из каталога провайдера.
func (f *Fleet) Configs(catalog []llm.ModelInfo) ([]Config, error) {
	var out []Config
	for i, g := range f.Agents {
		n := g.Replicas
		if n <= 0 {
			n = 1
		}
		cfg := overlay(f.Defaults, g.Config)
		if cfg.Name == "" {
			cfg.Name = fmt.Sprintf("group%d", i+1)
		}
		if err := cfg.Resolve(catalog); err != nil {
			return nil, err
		}
		for j := 0; j < n; j++ {
			out = append(out, cfg.Clone())
		}
	}
	return out, nil
}
