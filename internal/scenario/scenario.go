// Package scenario описывает эксперимент в YAML: один и тот же вопрос,
// прогнанный несколькими способами, плюс проверки ответа.
//
// Формат общий для всех шагов: во 2-й день варианты отличаются
// параметрами формата, в 3-й — цепочками шагов, в 4-й — температурой,
// в 5-й — моделью.
package scenario

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/safronov-a1exander/advent/internal/llm"
	"gopkg.in/yaml.v3"
)

// Params — параметры генерации в YAML-виде (все поля необязательные).
type Params struct {
	Temperature    *float64 `yaml:"temperature"`
	TopP           *float64 `yaml:"top_p"`
	MaxTokens      *int     `yaml:"max_tokens"`
	Stop           []string `yaml:"stop"`
	ResponseFormat string   `yaml:"response_format"` // text | json_object
	Thinking       string   `yaml:"thinking"`        // enabled | disabled
	Seed           *int     `yaml:"seed"`
}

// Merge накладывает o поверх p — заданные поля побеждают.
func (p Params) Merge(o *Params) Params {
	if o == nil {
		return p
	}
	if o.Temperature != nil {
		p.Temperature = o.Temperature
	}
	if o.TopP != nil {
		p.TopP = o.TopP
	}
	if o.MaxTokens != nil {
		p.MaxTokens = o.MaxTokens
	}
	if o.Stop != nil {
		p.Stop = o.Stop
	}
	if o.ResponseFormat != "" {
		p.ResponseFormat = o.ResponseFormat
	}
	if o.Thinking != "" {
		p.Thinking = o.Thinking
	}
	if o.Seed != nil {
		p.Seed = o.Seed
	}
	return p
}

// Apply переносит параметры в запрос к API.
func (p Params) Apply(r *llm.Request) {
	r.Temperature = p.Temperature
	r.TopP = p.TopP
	r.MaxTokens = p.MaxTokens
	r.Stop = p.Stop
	r.Seed = p.Seed
	if p.ResponseFormat != "" {
		r.ResponseFormat = &llm.ResponseFormat{Type: p.ResponseFormat}
	}
	if p.Thinking != "" {
		r.Thinking = &llm.Thinking{Type: p.Thinking}
	}
}

// Describe — короткая подпись параметров для таблиц и видео.
func (p Params) Describe() string {
	var parts []string
	if p.Temperature != nil {
		parts = append(parts, fmt.Sprintf("t=%g", *p.Temperature))
	}
	if p.MaxTokens != nil {
		parts = append(parts, fmt.Sprintf("max=%d", *p.MaxTokens))
	}
	if len(p.Stop) > 0 {
		parts = append(parts, "stop="+strings.Join(p.Stop, "|"))
	}
	if p.ResponseFormat != "" {
		parts = append(parts, "fmt="+p.ResponseFormat)
	}
	if p.Thinking != "" {
		parts = append(parts, "think="+p.Thinking)
	}
	if p.Seed != nil {
		parts = append(parts, fmt.Sprintf("seed=%d", *p.Seed))
	}
	if len(parts) == 0 {
		return "— (значения API по умолчанию)"
	}
	return strings.Join(parts, " ")
}

// Checks — автоматические проверки ответа. Нужны, чтобы в видео было видно
// не «мне кажется, формат соблюдён», а объективный вердикт.
type Checks struct {
	JSON           bool     `yaml:"json"`            // ответ обязан парситься как JSON
	ArrayPath      string   `yaml:"array_path"`      // поле верхнего уровня с массивом
	RequiredFields []string `yaml:"required_fields"` // поля у каждого элемента массива
	MaxWords       int      `yaml:"max_words"`
	MaxChars       int      `yaml:"max_chars"`
	MustContain    []string `yaml:"must_contain"`
	MustNotContain []string `yaml:"must_not_contain"`
	FinishReason   string   `yaml:"finish_reason"` // ожидаемый finish_reason
	// ExpectError — вариант считается успешным, если API вернул ошибку
	// с этой подстрокой. Нужен, чтобы показать границы параметров
	// (например temperature вне [0, 2]) как заявленный результат, а не сбой.
	ExpectError string `yaml:"expect_error"`
}

func (c Checks) Empty() bool {
	return !c.JSON && c.ArrayPath == "" && len(c.RequiredFields) == 0 &&
		c.MaxWords == 0 && c.MaxChars == 0 && len(c.MustContain) == 0 &&
		len(c.MustNotContain) == 0 && c.FinishReason == "" && c.ExpectError == ""
}

// Step — один вызов LLM внутри варианта.
type Step struct {
	System  string  `yaml:"system"`
	User    string  `yaml:"user"`
	Capture string  `yaml:"capture"` // имя переменной для ответа
	Params  *Params `yaml:"params"`
	Label   string  `yaml:"label"`
	// KeepHistory: продолжать диалог предыдущими шагами, а не начинать заново.
	KeepHistory bool `yaml:"keep_history"`
}

// Variant — один способ решить задачу.
type Variant struct {
	ID     string  `yaml:"id"`
	Label  string  `yaml:"label"`
	Model  string  `yaml:"model"`
	System string  `yaml:"system"`
	Params *Params `yaml:"params"`
	Steps  []Step  `yaml:"steps"`
	Checks *Checks `yaml:"checks"`
	Note   string  `yaml:"note"` // что этот вариант демонстрирует
}

type Scenario struct {
	Name string `yaml:"name"`
	// Title — как эксперимент называется для человека. Показывается на экране
	// вместо служебного Name: на видео должен быть продукт, а не имя файла.
	Title       string            `yaml:"title"`
	Description string            `yaml:"description"`
	Model       string            `yaml:"model"`
	Provider    string            `yaml:"provider"`
	System      string            `yaml:"system"`
	Repeat      int               `yaml:"repeat"`
	Vars        map[string]string `yaml:"vars"`
	// VarsFiles — переменные, читаемые из файлов: {имя: путь}. Нужны,
	// чтобы общие для всех дней данные (выписка, а позже база для RAG)
	// лежали в одном месте, а не копировались по сценариям.
	VarsFiles map[string]string `yaml:"vars_files"`
	Params    Params            `yaml:"params"`
	Checks    Checks            `yaml:"checks"`
	Variants  []Variant         `yaml:"variants"`

	path string
}

func Load(path string) (*Scenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Scenario
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.path = path
	if err := s.loadVarsFiles(); err != nil {
		return nil, err
	}
	if s.Repeat <= 0 {
		s.Repeat = 1
	}
	if len(s.Variants) == 0 {
		return nil, fmt.Errorf("%s: не задано ни одного варианта", path)
	}
	seen := map[string]bool{}
	for i := range s.Variants {
		v := &s.Variants[i]
		if v.ID == "" {
			return nil, fmt.Errorf("%s: у варианта #%d нет id", path, i+1)
		}
		if seen[v.ID] {
			return nil, fmt.Errorf("%s: id варианта %q повторяется", path, v.ID)
		}
		seen[v.ID] = true
		if v.Label == "" {
			v.Label = v.ID
		}
		if len(v.Steps) == 0 {
			return nil, fmt.Errorf("%s: у варианта %q нет шагов", path, v.ID)
		}
	}
	return &s, nil
}

// loadVarsFiles подставляет содержимое файлов в переменные.
// Путь ищется сначала как есть (от корня проекта), затем рядом со сценарием.
func (s *Scenario) loadVarsFiles() error {
	if len(s.VarsFiles) == 0 {
		return nil
	}
	if s.Vars == nil {
		s.Vars = map[string]string{}
	}
	for name, rel := range s.VarsFiles {
		b, err := os.ReadFile(rel)
		if err != nil {
			alt := filepath.Join(filepath.Dir(s.path), rel)
			b, err = os.ReadFile(alt)
			if err != nil {
				return fmt.Errorf("%s: переменная %q: не нашёл файл %q", s.path, name, rel)
			}
		}
		s.Vars[name] = strings.TrimRight(string(b), "\r\n")
	}
	return nil
}

func (s *Scenario) Path() string { return s.path }

// DisplayTitle — человеческое название для экрана.
func (s *Scenario) DisplayTitle() string {
	if strings.TrimSpace(s.Title) != "" {
		return s.Title
	}
	return s.Name
}

// EffectiveParams — параметры для конкретного шага конкретного варианта.
func (s *Scenario) EffectiveParams(v Variant, st Step) Params {
	return s.Params.Merge(v.Params).Merge(st.Params)
}

func (s *Scenario) EffectiveChecks(v Variant) Checks {
	if v.Checks != nil {
		return *v.Checks
	}
	return s.Checks
}

func (s *Scenario) EffectiveModel(v Variant, fallback string) string {
	if v.Model != "" {
		return v.Model
	}
	if s.Model != "" {
		return s.Model
	}
	return fallback
}

// Render подставляет переменные: {{.task}} из vars и {{.имя_capture}}
// из ответов предыдущих шагов.
func Render(tpl string, vars map[string]string) (string, error) {
	if !strings.Contains(tpl, "{{") {
		return tpl, nil
	}
	t, err := template.New("s").Option("missingkey=error").Parse(tpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		return "", err
	}
	return buf.String(), nil
}
