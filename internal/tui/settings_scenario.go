package tui

import (
	"strconv"
	"strings"

	"github.com/safronov-a1exander/advent/internal/scenario"
)

// ScenarioOverride превращает панель в слой параметров, который кладётся
// поверх YAML-сценария. Пустые поля не переопределяют ничего — так можно
// подкрутить одну температуру, не трогая остального.
func (s *Settings) ScenarioOverride() *scenario.Params {
	p := &scenario.Params{}
	touched := false

	if s.Temperature != nil {
		p.Temperature = s.Temperature
		touched = true
	}
	if s.TopP != nil {
		p.TopP = s.TopP
		touched = true
	}
	if s.Thinking != "" {
		p.Thinking = s.Thinking
		touched = true
	}
	if s.Seed != nil {
		p.Seed = s.Seed
		touched = true
	}
	if s.MaxTokens != nil {
		p.MaxTokens = s.MaxTokens
		touched = true
	}
	if len(s.Stop) > 0 {
		p.Stop = s.Stop
		touched = true
	}
	if s.ResponseFormat != "" {
		p.ResponseFormat = s.ResponseFormat
		touched = true
	}
	if !touched {
		return nil
	}
	return p
}

// OverrideSummary — что именно панель навязывает сценарию.
// Показывается на экране, чтобы в видео не гадать, «а с чем это прогнали».
func (s *Settings) OverrideSummary() string {
	var parts []string
	if strings.TrimSpace(s.Model) != "" {
		parts = append(parts, "модель "+s.Model)
	}
	if strings.TrimSpace(s.System) != "" {
		parts = append(parts, "свой system")
	}
	if sum := s.Summary(); sum != "" {
		parts = append(parts, sum)
	}
	if s.Repeat > 0 {
		parts = append(parts, "повторов "+strconv.Itoa(s.Repeat))
	}
	if len(parts) == 0 {
		return "всё как в сценарии"
	}
	return strings.Join(parts, " · ")
}
