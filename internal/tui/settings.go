package tui

import (
	"strings"

	"github.com/safronov-a1exander/advent/internal/llm"
)

// Settings — параметры запроса, которые крутятся прямо в интерфейсе.
//
// Каждый шаг дописывает сюда своё, а Fields() отдаёт панели
// актуальный набор. За счёт этого на ветке дня N доступны все параметры
// дней 1…N: то, что появилось во втором дне, остаётся под рукой в четвёртом.
type Settings struct {
	// Catalog — модели из config.yaml. Нужен для выбора стрелками.
	Catalog []llm.ModelInfo

	Model  string
	System string
	Stream bool
}

func NewSettings(catalog []llm.ModelInfo, model, system string) *Settings {
	return &Settings{
		Catalog: catalog,
		Model:   model,
		System:  system,
		Stream:  true,
	}
}

// modelIDs — список для перебора стрелками; текущая модель добавляется,
// даже если её нет в конфиге (её можно ввести руками).
func (s *Settings) modelIDs() []string {
	ids := make([]string, 0, len(s.Catalog)+1)
	seen := map[string]bool{}
	for _, m := range s.Catalog {
		if !seen[m.ID] {
			seen[m.ID] = true
			ids = append(ids, m.ID)
		}
	}
	if s.Model != "" && !seen[s.Model] {
		ids = append(ids, s.Model)
	}
	if len(ids) == 0 {
		ids = []string{s.Model}
	}
	return ids
}

// modelField вынесен отдельно: его переиспользуют поздние дни.
func (s *Settings) modelField() Field {
	f := EnumField("модель",
		"←→ перебирает модели из config.yaml, Enter — ввести имя руками",
		s.modelIDs(),
		func() string { return s.Model },
		func(v string) { s.Model = v })
	f.Text = func() string { return s.Model }
	f.SetText = func(v string) error { s.Model = strings.TrimSpace(v); return nil }
	return f
}

// Fields — набор параметров для панели. День 1: из чего вообще состоит запрос.
func (s *Settings) Fields() []Field {
	return []Field{
		s.modelField(),
		TextField("system", "системный промпт; применяется со следующего запроса",
			func() string { return s.System },
			func(v string) { s.System = v }),
		BoolField("стриминг", "stream=true — ответ приходит по мере генерации (SSE)",
			func() bool { return s.Stream },
			func(v bool) { s.Stream = v }),
	}
}

// Apply переносит настройки в запрос к API.
func (s *Settings) Apply(req *llm.Request) {
	req.Model = s.Model
}

// Summary — короткая подпись отличий от значений по умолчанию.
// Имя модели сюда не входит: оно и так печатается рядом.
func (s *Settings) Summary() string {
	var parts []string
	if !s.Stream {
		parts = append(parts, "без стриминга")
	}
	return strings.Join(parts, " · ")
}
