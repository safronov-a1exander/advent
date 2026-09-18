// Package profile — персонализация ассистента (день 12).
//
// Самый частый вопрос дня в чате курса был «чем профиль отличается от
// памяти», и ведущий разводил их трижды. Короткий ответ: память — это то,
// что агент **накопил**, общаясь с пользователем; профиль — то, **как мы
// заставляем агента работать** под конкретного пользователя и конкретную
// задачу. Память пишет агент, профиль пишет человек. Память — данные,
// профиль — конфиг оркестрации.
//
// Отсюда всё устройство пакета:
//
//   - профиль лежит в YAML рядом с конфигом, а не в каталоге памяти;
//   - его никто не «обновляет по ходу разговора» — его правят;
//   - в нём две части, и вторая важнее первой.
//
// Часть первая, очевидная, — как отвечать: стиль, формат, ограничения,
// зачем пользователь вообще здесь. Это то, что просит задание, и это
// уходит блоком в системный промпт каждого запроса.
//
// Часть вторая — **пайплайн**: какой дорогой гнать запрос в зависимости
// от того, что это за запрос. «Профиль = пайплайн из скиллов для той или
// иной задачи», как сказал ведущий. Фича идёт через сбор требований, план,
// код, валидацию; баг — через репродьюс, root cause, фикс. Разные стадии
// под разную задачу — это и есть профиль, а не только «обращайся ко мне
// на ты».
//
// Здесь пайплайн пока выбирает стратегию рассуждения (день 3) и класс
// модели. Стадиями с сохраняемым состоянием он станет на тринадцатом дне —
// профиль к тому моменту уже знает, какие стадии кому положены.
package profile

import (
	"fmt"
	"strings"
)

// Profile — настройка агента под конкретного пользователя.
type Profile struct {
	// ID — имя файла: profiles/<id>.yaml.
	ID string `yaml:"-"`
	// Name — человеческое имя для списков.
	Name string `yaml:"name"`
	// About — кто это; одна строка, попадает в промпт первой.
	About string `yaml:"about"`

	Style  Style    `yaml:"style"`
	Limits []string `yaml:"limits"`
	// Goal — зачем пользователь это делает. Лекция называла это третьим
	// китом персонализации рядом со стилем и ограничениями: «учим язык,
	// чтобы сдать TOEFL» и «чтобы спросить, как пройти в библиотеку» —
	// это два разных ассистента.
	Goal string `yaml:"goal"`

	// Pipelines — дороги под разные типы запросов. Первый в списке
	// считается дорогой по умолчанию.
	Pipelines []Pipeline `yaml:"pipelines"`
}

// Style — как разговаривать.
type Style struct {
	// Address — как обращаться: «на ты», «Олег Петрович».
	Address string `yaml:"address"`
	// Tone — формальный, разговорный, сухой.
	Tone string `yaml:"tone"`
	// Length — «три-пять предложений», «коротко», «подробно, с разбором».
	Length string `yaml:"length"`
	// Format — список, проза, с примерами кода, таблицей.
	Format string `yaml:"format"`
	// Level — уровень собеседника: что можно не объяснять.
	Level string `yaml:"level"`
}

// Empty — в стиле ничего не задано.
func (s Style) Empty() bool {
	return s.Address == "" && s.Tone == "" && s.Length == "" && s.Format == "" && s.Level == ""
}

// Pipeline — дорога запроса: какие стадии, каким классом модели и какой
// стратегией рассуждения.
type Pipeline struct {
	Name string `yaml:"name"`
	// When — слова, по которым запрос узнаётся. Пусто у дороги по
	// умолчанию. Это простой роутер, а не классификатор: «профиль можно
	// побить на файлы, сделать общий профиль-роутер и потом выбирать
	// конкретный» — здесь роутер живёт внутри одного профиля.
	When []string `yaml:"when"`
	// Stages — стадии дороги. Пока это описание: на тринадцатом дне по нему
	// поедет конечный автомат, а сейчас стадии видны в промпте и в отчёте,
	// чтобы модель знала, где она находится.
	Stages []string `yaml:"stages"`
	// Strategy — стратегия рассуждения дня 3 на этой дороге.
	Strategy string `yaml:"strategy"`
	// Tier — класс модели дня 5. Разные модели на разные дороги — это то,
	// «что не даёт модели самой себе поддакивать».
	Tier string `yaml:"tier"`
	// System — что дописать в системный промпт именно на этой дороге.
	System string `yaml:"system"`
}

// Pick выбирает дорогу под запрос: первая, чьё слово встретилось в тексте.
// Если ни одна не подошла — дорога по умолчанию (первая в списке).
//
// Выбор нарочно тупой и объяснимый. Отдельный вызов LLM-классификатора был
// бы точнее, но стоил бы токенов на каждый запрос и, главное, сделал бы
// поведение непредсказуемым: пользователь не понимал бы, почему один и тот
// же вопрос сегодня пошёл через план, а вчера — напрямую.
func (p *Profile) Pick(query string) (Pipeline, bool) {
	if p == nil || len(p.Pipelines) == 0 {
		return Pipeline{}, false
	}
	q := strings.ToLower(query)
	for _, pl := range p.Pipelines {
		for _, w := range pl.When {
			if w = strings.ToLower(strings.TrimSpace(w)); w != "" && strings.Contains(q, w) {
				return pl, true
			}
		}
	}
	return p.Pipelines[0], true
}

// Default — дорога по умолчанию.
func (p *Profile) Default() (Pipeline, bool) {
	if p == nil || len(p.Pipelines) == 0 {
		return Pipeline{}, false
	}
	return p.Pipelines[0], true
}

// Block — профиль для системного промпта. Пустой профиль даёт пустую строку.
//
// Блок собирается только из заполненных полей: пустое «тон: » модель читает
// как указание и начинает выдумывать, каким же тон должен быть.
func (p *Profile) Block() string {
	if p == nil {
		return ""
	}
	var lines []string
	add := func(label, value string) {
		if v := strings.TrimSpace(value); v != "" {
			lines = append(lines, fmt.Sprintf("- %s: %s", label, v))
		}
	}
	add("кто собеседник", p.About)
	add("обращение", p.Style.Address)
	add("тон", p.Style.Tone)
	add("длина ответа", p.Style.Length)
	add("формат", p.Style.Format)
	add("уровень", p.Style.Level)
	add("зачем ему это", p.Goal)
	for _, l := range p.Limits {
		add("ограничение", l)
	}
	if len(lines) == 0 {
		return ""
	}
	return "Профиль пользователя — так ты отвечаешь именно ему:\n" + strings.Join(lines, "\n")
}

// StageBlock — стадии выбранной дороги для системного промпта.
//
// Отдельным блоком, а не внутри профиля: профиль у пользователя один,
// а дорога зависит от запроса, и держать их врозь честнее — видно, что
// меняется от запроса к запросу, а что нет.
func (pl Pipeline) StageBlock() string {
	if len(pl.Stages) == 0 && strings.TrimSpace(pl.System) == "" {
		return ""
	}
	var b strings.Builder
	if len(pl.Stages) > 0 {
		fmt.Fprintf(&b, "Такие запросы у этого пользователя идут дорогой «%s»: %s.\n",
			pl.Name, strings.Join(pl.Stages, " → "))
		b.WriteString("Держись этого порядка и не перепрыгивай стадии.")
	}
	if s := strings.TrimSpace(pl.System); s != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(s)
	}
	return b.String()
}

// Summary — однострочная подпись профиля для шапки и отчётов.
func (p *Profile) Summary() string {
	if p == nil {
		return ""
	}
	name := p.Name
	if name == "" {
		name = p.ID
	}
	var parts []string
	if !p.Style.Empty() {
		parts = append(parts, "стиль")
	}
	if len(p.Limits) > 0 {
		parts = append(parts, fmt.Sprintf("ограничений %d", len(p.Limits)))
	}
	if len(p.Pipelines) > 0 {
		parts = append(parts, fmt.Sprintf("дорог %d", len(p.Pipelines)))
	}
	if len(parts) == 0 {
		return name
	}
	return name + " · " + strings.Join(parts, " · ")
}

// Validate — что профиль вообще осмысленный. Зовётся при чтении файла:
// тихо не работающий профиль хуже, чем отказ его прочитать.
func (p *Profile) Validate() error {
	if p.Name == "" && p.About == "" && p.Style.Empty() && len(p.Limits) == 0 && len(p.Pipelines) == 0 {
		return fmt.Errorf("профиль %q пустой: нечего подмешивать в запрос", p.ID)
	}
	seen := map[string]bool{}
	for i, pl := range p.Pipelines {
		if strings.TrimSpace(pl.Name) == "" {
			return fmt.Errorf("профиль %q: у дороги %d нет имени", p.ID, i+1)
		}
		if seen[pl.Name] {
			return fmt.Errorf("профиль %q: две дороги с именем %q", p.ID, pl.Name)
		}
		seen[pl.Name] = true
		if i > 0 && len(pl.When) == 0 {
			// Дорога без when никогда не выберется, кроме первой: молча
			// мёртвая ветка конфига — худший вид сюрприза.
			return fmt.Errorf("профиль %q: дорога %q без when недостижима — такой может быть только первая", p.ID, pl.Name)
		}
	}
	return nil
}
