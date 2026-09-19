package agent

import (
	"fmt"
	"strings"

	"github.com/safronov-a1exander/advent/internal/llm"
)

// Config — всё, что определяет поведение агента.
//
// Это единственное место, где собраны параметры первой недели: модель и
// системный промпт (день 1), контроль формата (день 2), способ рассуждения
// (день 3), сэмплирование (день 4) и класс модели (день 5). Раньше они жили
// в состоянии экрана чата, и второго собеседника с другими настройками
// завести было негде. Теперь конфиг — обычное значение: его можно прочитать
// из YAML, скопировать, поменять одно поле и отдать новому агенту.
type Config struct {
	// Name — человеческое имя; по нему агенты группируются в отчётах пула.
	Name string `yaml:"name" json:"name,omitempty"`

	// Model — точный id модели. Если пусто, берётся модель класса Tier —
	// так конфиг не привязан к именам моделей конкретного провайдера.
	Model string `yaml:"model" json:"model,omitempty"`
	Tier  string `yaml:"tier" json:"tier,omitempty"` // weak | medium | strong

	System   string `yaml:"system" json:"system,omitempty"`
	Strategy string `yaml:"strategy" json:"strategy,omitempty"`
	Stream   bool   `yaml:"stream" json:"stream"`

	Temperature    *float64 `yaml:"temperature" json:"temperature,omitempty"`
	TopP           *float64 `yaml:"top_p" json:"top_p,omitempty"`
	Thinking       string   `yaml:"thinking" json:"thinking,omitempty"`
	Seed           *int     `yaml:"seed" json:"seed,omitempty"`
	MaxTokens      *int     `yaml:"max_tokens" json:"max_tokens,omitempty"`
	Stop           []string `yaml:"stop" json:"stop,omitempty"`
	ResponseFormat string   `yaml:"response_format" json:"response_format,omitempty"`

	// ContextLimit — собственный лимит контекста агента в токенах, меньше
	// окна модели. Запрос, который по оценке не влезает, не отправляется.
	// nil — ограничивает только окно модели на стороне провайдера.
	ContextLimit *int `yaml:"context_limit" json:"context_limit,omitempty"`

	// Context — стратегия сборки контекста (день 9): "" — вся история,
	// "summary" — сводка старой части плюс последние KeepLast сообщений.
	// Сжатие запускается, когда за хвостом накопилось SummarizeEvery сообщений.
	Context        string `yaml:"context" json:"context,omitempty"`
	KeepLast       *int   `yaml:"keep_last" json:"keep_last,omitempty"`
	SummarizeEvery *int   `yaml:"summarize_every" json:"summarize_every,omitempty"`

	// Модель памяти (день 11). Memory — режим: "" (слоёв нет), "manual"
	// (слои уходят в промпт, кладёт пользователь), "auto" (плюс служебный
	// вызов раскладки после каждой реплики).
	//
	// User и Task — ключи хранимых слоёв: чей долговременный слой и какой
	// задачи рабочий. Разговоров у задачи может быть много, задач у
	// пользователя тоже, поэтому слои привязаны не к агенту, а к ним.
	Memory string `yaml:"memory" json:"memory,omitempty"`
	User   string `yaml:"user" json:"user,omitempty"`
	Task   string `yaml:"task" json:"task,omitempty"`
	// Profile — id профиля пользователя (день 12): profiles/<id>.yaml.
	// Пусто — без профиля, агент работает как на одиннадцатом дне.
	Profile string `yaml:"profile" json:"profile,omitempty"`
}

// Clone — глубокая копия: указатели и срезы не делятся между агентами,
// иначе правка температуры у одного незаметно поменяла бы её у всех.
func (c Config) Clone() Config {
	out := c
	if c.Temperature != nil {
		out.Temperature = llm.F(*c.Temperature)
	}
	if c.TopP != nil {
		out.TopP = llm.F(*c.TopP)
	}
	if c.Seed != nil {
		out.Seed = llm.I(*c.Seed)
	}
	if c.MaxTokens != nil {
		out.MaxTokens = llm.I(*c.MaxTokens)
	}
	if c.Stop != nil {
		out.Stop = append([]string(nil), c.Stop...)
	}
	if c.ContextLimit != nil {
		out.ContextLimit = llm.I(*c.ContextLimit)
	}
	if c.KeepLast != nil {
		out.KeepLast = llm.I(*c.KeepLast)
	}
	if c.SummarizeEvery != nil {
		out.SummarizeEvery = llm.I(*c.SummarizeEvery)
	}
	return out
}

// Resolve подставляет модель по классу, если точный id не задан.
func (c *Config) Resolve(catalog []llm.ModelInfo) error {
	if strings.TrimSpace(c.Model) != "" {
		return nil
	}
	if c.Tier == "" {
		return fmt.Errorf("агент %q: не задана ни model, ни tier", c.Name)
	}
	for _, m := range catalog {
		if m.Tier == c.Tier {
			c.Model = m.ID
			return nil
		}
	}
	return fmt.Errorf("агент %q: в config.yaml нет модели класса %q", c.Name, c.Tier)
}

// Apply переносит параметры в запрос к API. Сообщения собирает агент.
func (c Config) Apply(req *llm.Request) {
	req.Model = c.Model
	req.Temperature = c.Temperature
	req.TopP = c.TopP
	req.Seed = c.Seed
	req.MaxTokens = c.MaxTokens
	req.Stop = c.Stop
	req.Thinking = nil
	if c.Thinking != "" {
		req.Thinking = &llm.Thinking{Type: c.Thinking}
	}
	req.ResponseFormat = nil
	if c.ResponseFormat != "" {
		req.ResponseFormat = &llm.ResponseFormat{Type: c.ResponseFormat}
	}
}

// Summary — короткая подпись отличий от значений по умолчанию.
// Имя модели сюда не входит: оно и так печатается рядом.
func (c Config) Summary() string {
	var parts []string
	if c.Strategy != "" {
		parts = append(parts, c.Strategy)
	}
	if c.Temperature != nil {
		parts = append(parts, fmt.Sprintf("t=%g", *c.Temperature))
	}
	if c.TopP != nil {
		parts = append(parts, fmt.Sprintf("top_p=%g", *c.TopP))
	}
	if c.Thinking != "" {
		parts = append(parts, "think="+c.Thinking)
	}
	if c.Seed != nil {
		parts = append(parts, fmt.Sprintf("seed=%d", *c.Seed))
	}
	if c.MaxTokens != nil {
		parts = append(parts, fmt.Sprintf("max=%d", *c.MaxTokens))
	}
	if len(c.Stop) > 0 {
		parts = append(parts, "stop="+strings.Join(c.Stop, "|"))
	}
	if c.ResponseFormat != "" {
		parts = append(parts, "fmt="+c.ResponseFormat)
	}
	if c.ContextLimit != nil {
		parts = append(parts, fmt.Sprintf("ctx≤%d", *c.ContextLimit))
	}
	switch c.Context {
	case ContextSummary:
		parts = append(parts, fmt.Sprintf("summary: хвост %d, сжатие каждые %d", c.keepLast(), c.summarizeEvery()))
	case ContextWindow:
		parts = append(parts, fmt.Sprintf("window: последние %d", c.keepLast()))
	case ContextFacts:
		parts = append(parts, fmt.Sprintf("facts + последние %d", c.keepLast()))
	}
	if c.Profile != "" {
		parts = append(parts, "профиль: "+c.Profile)
	}
	if c.Memory != "" {
		mem := "память: " + c.Memory
		if c.User != "" {
			mem += " · юзер " + c.User
		}
		if c.Task != "" {
			mem += " · задача " + c.Task
		}
		parts = append(parts, mem)
	}
	if !c.Stream {
		parts = append(parts, "без стриминга")
	}
	return strings.Join(parts, " · ")
}

// overlay накладывает на базовый конфиг заданные поля другого.
// Нужен файлу флота: общие настройки пишутся один раз в defaults,
// а у каждой группы агентов — только то, чем она отличается.
func overlay(base, top Config) Config {
	out := base.Clone()
	if top.Name != "" {
		out.Name = top.Name
	}
	// Класс и точное имя взаимоисключающи: что задала группа, то и действует.
	// Если заданы оба, побеждает точное имя.
	if top.Tier != "" {
		out.Tier, out.Model = top.Tier, ""
	}
	if top.Model != "" {
		out.Model, out.Tier = top.Model, ""
	}
	if top.System != "" {
		out.System = top.System
	}
	if top.Strategy != "" {
		out.Strategy = top.Strategy
	}
	if top.Stream {
		out.Stream = true
	}
	if top.Temperature != nil {
		out.Temperature = llm.F(*top.Temperature)
	}
	if top.TopP != nil {
		out.TopP = llm.F(*top.TopP)
	}
	if top.Thinking != "" {
		out.Thinking = top.Thinking
	}
	if top.Seed != nil {
		out.Seed = llm.I(*top.Seed)
	}
	if top.MaxTokens != nil {
		out.MaxTokens = llm.I(*top.MaxTokens)
	}
	if top.Stop != nil {
		out.Stop = append([]string(nil), top.Stop...)
	}
	if top.ResponseFormat != "" {
		out.ResponseFormat = top.ResponseFormat
	}
	if top.ContextLimit != nil {
		out.ContextLimit = llm.I(*top.ContextLimit)
	}
	if top.Context != "" {
		out.Context = top.Context
	}
	if top.KeepLast != nil {
		out.KeepLast = llm.I(*top.KeepLast)
	}
	if top.SummarizeEvery != nil {
		out.SummarizeEvery = llm.I(*top.SummarizeEvery)
	}
	if top.Profile != "" {
		out.Profile = top.Profile
	}
	if top.Memory != "" {
		out.Memory = top.Memory
	}
	if top.User != "" {
		out.User = top.User
	}
	if top.Task != "" {
		out.Task = top.Task
	}
	return out
}

// Overlay — конфиг base с заданными полями top поверх. Для сценариев,
// где общие настройки пишутся один раз, а варианты отличаются парой полей.
func Overlay(base, top Config) Config { return overlay(base, top) }
