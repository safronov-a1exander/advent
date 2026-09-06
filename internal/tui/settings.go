package tui

import (
	"fmt"
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

	// Overlay — режим наложения поверх YAML-сценария (экран lab).
	// В нём пустое значение означает «как в сценарии», а не «не передавать».
	Overlay bool

	Model  string
	System string
	Stream bool

	// День 2 — контроль формата ответа.
	MaxTokens      *int
	Stop           []string
	ResponseFormat string

	// День 3 — способ рассуждения. Только для чата: в сценариях цепочки
	// описаны явно через steps.
	Strategy string

	// День 4 — сэмплирование.
	Temperature *float64
	TopP        *float64
	Thinking    string
	Seed        *int

	// Повторов на вариант; только для lab, 0 = как в сценарии.
	Repeat int
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
	if s.Overlay {
		// пустая строка = «модель берётся из сценария»
		ids = append([]string{""}, ids...)
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

// strategyField — способ рассуждения, добавленный на шаге 3.
// Подсказка меняется вместе со значением, поэтому HintFn, а не Hint.
func (s *Settings) strategyField() Field {
	f := EnumField("стратегия", "", Strategies,
		func() string { return s.Strategy },
		func(v string) { s.Strategy = v })
	f.Value = func() string { return strategyLabel(s.Strategy) }
	f.HintFn = func() string { return StrategyHint(s.Strategy) }
	return f
}

// tierField — быстрый переход между классами моделей из config.yaml.
// В задании дня 5 сравнивают «слабую / среднюю / сильную», и держать это
// отдельным полем удобнее, чем помнить имена моделей.
func (s *Settings) tierField() Field {
	tiers := []string{"weak", "medium", "strong"}
	current := func() string {
		for _, m := range s.Catalog {
			if m.ID == s.Model {
				return m.Tier
			}
		}
		return ""
	}
	set := func(t string) {
		for _, m := range s.Catalog {
			if m.Tier == t {
				s.Model = m.ID
				return
			}
		}
	}
	idx := func() int {
		cur := current()
		for i, t := range tiers {
			if t == cur {
				return i
			}
		}
		return -1
	}
	return Field{
		Label: "класс",
		Hint:  "слабая / средняя / сильная модель из config.yaml; переключает поле «модель»",
		Value: func() string {
			if c := current(); c != "" {
				return c
			}
			return "— (нет в конфиге)"
		},
		Left: func() {
			i := idx() - 1
			if i < 0 {
				i = len(tiers) - 1
			}
			set(tiers[i])
		},
		Right: func() {
			i := idx() + 1
			if i >= len(tiers) {
				i = 0
			}
			set(tiers[i])
		},
	}
}

// Fields — набор параметров для панели.
// День 1: из чего состоит запрос. День 2: чем контролируется формат ответа.
func (s *Settings) Fields() []Field {
	sysHint := "системный промпт; подставляется в каждый запрос заново"
	if s.Overlay {
		sysHint = "переопределяет system сценария; пусто — как в сценарии"
	}

	f := []Field{
		s.modelField(),
		s.tierField(),
		TextField("system", sysHint,
			func() string { return s.System },
			func(v string) { s.System = v }),
	}
	if !s.Overlay {
		f = append(f,
			s.strategyField(),
			BoolField("стриминг",
				"stream=true — ответ приходит по мере генерации (SSE)",
				func() bool { return s.Stream },
				func(v bool) { s.Stream = v }))
	}

	f = append(f,
		// hardMax 3.0 при штатном потолке DeepSeek 2.0 — намеренно:
		// иногда нужно вылезти за диапазон и получить 400 от API.
		FloatField("temperature",
			"ширина распределения при выборе следующего токена; у DeepSeek диапазон [0, 2], выше — ошибка 400",
			func() *float64 { return s.Temperature },
			func(v *float64) { s.Temperature = v },
			0.1, 0, 3.0, 0.7),

		FloatField("top_p",
			"nucleus sampling: берём токены, пока их суммарная вероятность не наберёт top_p",
			func() *float64 { return s.TopP },
			func(v *float64) { s.TopP = v },
			0.05, 0.05, 1.0, 0.9),

		EnumField("thinking",
			"disabled убирает рассуждения; без этого reasoning-токены съедают лимит max_tokens",
			[]string{"", "enabled", "disabled"},
			func() string { return s.Thinking },
			func(v string) { s.Thinking = v }),

		IntField("seed",
			"фиксирует сэмплирование; помогает воспроизводимости, но детерминизм не гарантирован",
			func() *int { return s.Seed },
			func(v *int) { s.Seed = v },
			1, 0, 1000000, 42),

		IntField("max_tokens",
			"потолок длины ответа; при упоре finish_reason становится length, а не stop",
			func() *int { return s.MaxTokens },
			func(v *int) { s.MaxTokens = v },
			50, 1, 32000, 300),

		TextField("stop",
			"стоп-последовательности через запятую; генерация обрывается на них, сама подстрока в ответ не попадает",
			func() string { return strings.Join(s.Stop, ", ") },
			func(v string) { s.Stop = splitList(v) }),

		EnumField("response_format",
			"json_object заставляет API вернуть валидный JSON — это гарантия, а не просьба в промпте",
			[]string{"", "text", "json_object"},
			func() string { return s.ResponseFormat },
			func(v string) { s.ResponseFormat = v }),
	)

	if s.Overlay {
		f = append(f, IntField("повторов",
			"сколько раз прогнать каждый вариант; больше одного — видно стабильность",
			func() *int {
				if s.Repeat <= 0 {
					return nil
				}
				n := s.Repeat
				return &n
			},
			func(v *int) {
				if v == nil {
					s.Repeat = 0
					return
				}
				s.Repeat = *v
			},
			1, 1, 20, 3))
	}
	return f
}

// splitList режет «a, b, c» в список, выкидывая пустые куски.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Apply переносит настройки в запрос к API.
func (s *Settings) Apply(req *llm.Request) {
	req.Model = s.Model
	req.Temperature = s.Temperature
	req.TopP = s.TopP
	req.Seed = s.Seed
	if s.Thinking != "" {
		req.Thinking = &llm.Thinking{Type: s.Thinking}
	} else {
		req.Thinking = nil
	}
	req.MaxTokens = s.MaxTokens
	req.Stop = s.Stop
	if s.ResponseFormat != "" {
		req.ResponseFormat = &llm.ResponseFormat{Type: s.ResponseFormat}
	} else {
		req.ResponseFormat = nil
	}
}

// Summary — короткая подпись отличий от значений по умолчанию.
// Имя модели сюда не входит: оно и так печатается рядом.
func (s *Settings) Summary() string {
	var parts []string
	if s.Strategy != "" {
		parts = append(parts, s.Strategy)
	}
	if s.Temperature != nil {
		parts = append(parts, fmt.Sprintf("t=%g", *s.Temperature))
	}
	if s.TopP != nil {
		parts = append(parts, fmt.Sprintf("top_p=%g", *s.TopP))
	}
	if s.Thinking != "" {
		parts = append(parts, "think="+s.Thinking)
	}
	if s.Seed != nil {
		parts = append(parts, fmt.Sprintf("seed=%d", *s.Seed))
	}
	if s.MaxTokens != nil {
		parts = append(parts, fmt.Sprintf("max=%d", *s.MaxTokens))
	}
	if len(s.Stop) > 0 {
		parts = append(parts, "stop="+strings.Join(s.Stop, "|"))
	}
	if s.ResponseFormat != "" {
		parts = append(parts, "fmt="+s.ResponseFormat)
	}
	if !s.Stream {
		parts = append(parts, "без стриминга")
	}
	return strings.Join(parts, " · ")
}
