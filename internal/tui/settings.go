package tui

import (
	"strings"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/llm"
)

// Settings — параметры, которые крутятся прямо в интерфейсе.
//
// С шестого дня это редактор конфига агента: панель правит поля, а перед
// каждым вопросом они уходят в агента через AgentConfig. Сам запрос
// экран больше не собирает.
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

	// День 8 — собственный лимит контекста агента.
	ContextLimit *int

	// День 9 — стратегия контекста и параметры сжатия.
	Context        string
	KeepLast       *int
	SummarizeEvery *int

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
	f := EnumField("стратегия", "", agent.Strategies,
		func() string { return s.Strategy },
		func(v string) { s.Strategy = v })
	f.Value = func() string { return agent.StrategyLabel(s.Strategy) }
	f.HintFn = func() string { return agent.StrategyHint(s.Strategy) }
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

	// День 8 — собственный лимит контекста агента. В сценариях lab его нет:
	// там каждый вариант — отдельный запрос без истории.
	if !s.Overlay {
		f = append(f, IntField("лимит контекста",
			"свой потолок агента в токенах, меньше окна модели; запрос сверх него не отправляется. Пусто — решает окно модели",
			func() *int { return s.ContextLimit },
			func(v *int) { s.ContextLimit = v },
			500, 200, 1000000, 4000))

		// День 9 — как собирать контекст из истории.
		ctx := EnumField("контекст", "", agent.ContextStrategies,
			func() string { return s.Context },
			func(v string) { s.Context = v })
		ctx.Value = func() string { return agent.ContextLabel(s.Context) }
		ctx.HintFn = func() string { return agent.ContextHint(s.Context) }
		f = append(f, ctx,
			IntField("хвост как есть",
				"сколько последних сообщений идёт в запрос как есть (window, facts, summary); пусто — 4",
				func() *int { return s.KeepLast },
				func(v *int) { s.KeepLast = v },
				2, 0, 100, 4),
			IntField("сжимать каждые",
				"сжатие запускается, когда за хвостом накопилось столько сообщений (для summary); пусто — 10",
				func() *int { return s.SummarizeEvery },
				func(v *int) { s.SummarizeEvery = v },
				2, 2, 200, 10))
	}

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

// AgentConfig — снимок панели в виде конфига агента.
func (s *Settings) AgentConfig() agent.Config {
	return agent.Config{
		Model:          s.Model,
		System:         s.System,
		Strategy:       s.Strategy,
		Stream:         s.Stream,
		Temperature:    s.Temperature,
		TopP:           s.TopP,
		Thinking:       s.Thinking,
		Seed:           s.Seed,
		MaxTokens:      s.MaxTokens,
		Stop:           s.Stop,
		ResponseFormat: s.ResponseFormat,
		ContextLimit:   s.ContextLimit,
		Context:        s.Context,
		KeepLast:       s.KeepLast,
		SummarizeEvery: s.SummarizeEvery,
	}.Clone()
}

// LoadConfig показывает в панели конфиг агента — при переключении
// между агентами панель должна показывать настройки того, с кем говоришь.
func (s *Settings) LoadConfig(c agent.Config) {
	c = c.Clone()
	s.Model = c.Model
	s.System = c.System
	s.Strategy = c.Strategy
	s.Stream = c.Stream
	s.Temperature = c.Temperature
	s.TopP = c.TopP
	s.Thinking = c.Thinking
	s.Seed = c.Seed
	s.MaxTokens = c.MaxTokens
	s.Stop = c.Stop
	s.ResponseFormat = c.ResponseFormat
	s.ContextLimit = c.ContextLimit
	s.Context = c.Context
	s.KeepLast = c.KeepLast
	s.SummarizeEvery = c.SummarizeEvery
}

// Summary — короткая подпись отличий от значений по умолчанию.
func (s *Settings) Summary() string { return s.AgentConfig().Summary() }
