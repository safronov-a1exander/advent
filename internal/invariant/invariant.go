// Package invariant — ограничения, которые ассистент не имеет права
// нарушать (день 14).
//
// Профиль (день 12) говорит, **как** отвечать; задача (день 13) — **где мы
// находимся**. Инварианты говорят, **чего нельзя никогда**: только Kotlin,
// только MVI, никакой RxJava, только бесплатные API, доставка только
// по Москве.
//
// Отличие от всего предыдущего — в том, что инвариант проверяется не до
// запроса, а **после ответа**. Профиль и стадию модель может проигнорировать,
// и мы об этом не узнаем. Нарушенный инвариант ловится и не доходит
// до пользователя.
//
// # Правило — это текст
//
// Инвариант живёт в markdown-файле: `invariants/<набор>/<правило>.md`.
// Тело файла — формулировка правила обычной прозой, и это главная его
// часть: ровно этот текст уходит в системный промпт и ровно его видит
// пользователь в объяснении отказа. Так делают и тузы — CLAUDE.md,
// `.cursor/rules/*.mdc`, — и так это показывал курс.
//
// Необязательный frontmatter добавляет к тексту машинную часть: список
// запрещённых слов, разрешённых вариантов, стадии, на которых правило
// действует. Правило без frontmatter — совершенно нормальное правило.
//
// # Чем проверяется
//
// Проверка смысловая: правило вместе с ответом уходит отдельной, дешёвой
// моделью, которая отвечает «нарушено или нет». Это **основной** механизм,
// потому что в текст переводится далеко не всё: «не советуй дороже бюджета»
// или «не обещай срок без оговорки» никакой подстрокой не поймаешь.
//
// Подстроки (`forbid`, `allow`, `require`) — не замена смысловой проверке,
// а **быстрый фильтр перед ней**: они бесплатны и ловят очевидное сразу,
// не тратя вызова. Пропустили — смысловая проверка всё равно посмотрит.
// Так и должно быть: подстрока — слабейший из способов, и она годится
// только на то, чтобы иногда сэкономить вызов, а не на то, чтобы что-то
// гарантировать.
//
// Стоит это ровно столько, сколько курс и предупреждал: проверять всё
// моделью «может быть адски дорого». Поэтому смысловая проверка включается
// отдельным режимом (см. agent.InvariantJudge), а дешёвые фильтры работают
// и без неё.
package invariant

import (
	"fmt"
	"regexp"
	"strings"
)

// Invariant — одно правило.
type Invariant struct {
	// Name — короткое имя: заголовок в отчётах, ключ в ответе проверяющей
	// модели и подпись в объяснении отказа. По умолчанию — имя файла.
	Name string `yaml:"name"`
	// Rule — формулировка правила человеческими словами. Это тело
	// markdown-файла: текст уходит и в промпт, и в объяснение отказа.
	Rule string `yaml:"-"`

	// Forbid — быстрый фильтр: этих слов в ответе быть не должно.
	Forbid []string `yaml:"forbid"`
	// Unless — слова отказа. Предложение, где запрещённое слово стоит рядом
	// с одним из них, фильтром не ловится.
	//
	// Без этого фильтр наказывает за правильное поведение. Ассистент,
	// которому запретили ЮKassa, отказывается словами «ЮKassa подключать
	// не будем» — и попадает под собственный запрет. На живом прогоне так
	// и вышло: проверка забраковала корректный отказ, агент переспросил,
	// модель отказалась теми же словами, и повтор сгорел впустую.
	Unless []string `yaml:"unless"`

	// Domain — признаки того, что ответ вообще касается темы. Нужен
	// фильтрам allow и require: без него «в ответе должен быть срок»
	// срабатывало бы на «здравствуйте».
	Domain []string `yaml:"domain"`
	// Allow — если ответ касается темы (Domain), то из этого списка.
	// «Упомянул СУБД — значит, PostgreSQL».
	Allow []string `yaml:"allow"`
	// Require — если ответ касается темы, хотя бы одно из этого должно быть.
	Require []string `yaml:"require"`

	// Ask — как спросить у проверяющей модели, если формулировку правила
	// стоит уточнить именно для проверки. Пусто — спрашивается Rule.
	Ask string `yaml:"ask"`

	// Stages — на каких стадиях задачи правило действует; пусто — на всех.
	// Не всякое правило вечно: «не пиши код» имеет смысл в планировании
	// и мешает в выполнении.
	Stages []string `yaml:"stages"`
}

// HasFilter — есть ли у правила быстрый фильтр. Правило без фильтра —
// нормальное правило: его проверит смысловая проверка.
func (i Invariant) HasFilter() bool {
	return len(i.Forbid) > 0 || len(i.Allow) > 0 || len(i.Require) > 0
}

// Filters — человеческое описание фильтров правила; для отчётов и панели.
func (i Invariant) Filters() string {
	var parts []string
	if len(i.Forbid) > 0 {
		parts = append(parts, fmt.Sprintf("запрещено %d слов", len(i.Forbid)))
	}
	if len(i.Allow) > 0 {
		parts = append(parts, fmt.Sprintf("разрешено только %d", len(i.Allow)))
	}
	if len(i.Require) > 0 {
		parts = append(parts, fmt.Sprintf("обязательно одно из %d", len(i.Require)))
	}
	if len(parts) == 0 {
		return "только смысловая проверка"
	}
	return strings.Join(parts, ", ")
}

// Validate — правило осмысленно.
func (i Invariant) Validate() error {
	switch {
	case strings.TrimSpace(i.Name) == "":
		return fmt.Errorf("у правила нет имени")
	case strings.TrimSpace(i.Rule) == "":
		// Текст правила — единственная обязательная часть: без него нечего
		// класть в промпт и нечего показать пользователю при отказе.
		return fmt.Errorf("правило %q: пустое тело файла — нечего отправлять в промпт", i.Name)
	case len(i.Allow) > 0 && len(i.Domain) == 0:
		return fmt.Errorf("правило %q: allow без domain — непонятно, когда фильтр вообще применять", i.Name)
	case len(i.Require) > 0 && len(i.Domain) == 0:
		return fmt.Errorf("правило %q: require без domain — фильтр сработает на любом ответе", i.Name)
	case len(i.Unless) > 0 && len(i.Forbid) == 0:
		return fmt.Errorf("правило %q: unless имеет смысл только вместе с forbid", i.Name)
	case len(i.Domain) > 0 && len(i.Allow) == 0 && len(i.Require) == 0:
		return fmt.Errorf("правило %q: domain задан, а фильтра (allow или require) нет", i.Name)
	}
	return nil
}

// Active — действует ли правило на этой стадии задачи.
func (i Invariant) Active(stage string) bool {
	if len(i.Stages) == 0 || stage == "" {
		return true
	}
	for _, s := range i.Stages {
		if strings.EqualFold(strings.TrimSpace(s), stage) {
			return true
		}
	}
	return false
}

// Violation — найденное нарушение.
type Violation struct {
	Name string
	Rule string
	// Found — что именно нашлось (или не нашлось); попадает в объяснение.
	Found string
	// ByJudge — нарушение нашла проверяющая модель, а не быстрый фильтр.
	ByJudge bool
}

func (v Violation) String() string {
	if v.Found == "" {
		return v.Name + ": " + v.Rule
	}
	return v.Name + ": " + v.Rule + " (" + v.Found + ")"
}

// norm — как сравниваем текст: регистр не важен.
func norm(s string) string { return strings.ToLower(s) }

// wordish — признак, который стоит сравнивать по границам слова. Без этого
// «Go» находилось бы внутри «Google», а фильтр срабатывал бы на ровном месте.
var wordish = regexp.MustCompile(`^[\p{L}\p{N}_+#.-]+$`)

func contains(text, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return false
	}
	if !wordish.MatchString(needle) {
		return strings.Contains(norm(text), norm(needle))
	}
	re, err := regexp.Compile(`(?i)(^|[^\p{L}\p{N}_])` + regexp.QuoteMeta(needle) + `($|[^\p{L}\p{N}_])`)
	if err != nil {
		return strings.Contains(norm(text), norm(needle))
	}
	return re.MatchString(text)
}

// sentenceSplit — границы предложений и пунктов списка. Пункт списка —
// тоже граница: «- Mongo нельзя» и «- Postgres берём» стоят в разных
// строках и про разное.
var sentenceSplit = regexp.MustCompile(`[.!?;\n]+`)

func sentences(text string) []string {
	parts := sentenceSplit.Split(text, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

func anyOf(text string, list []string) bool {
	for _, s := range list {
		if contains(text, s) {
			return true
		}
	}
	return false
}

// Filter прогоняет ответ через быстрые фильтры — бесплатно и до всякой
// модели. Правила без фильтров просто пропускаются: их дело смысловой
// проверки.
//
// Пустой результат НЕ означает «нарушений нет». Он означает «очевидного
// нарушения не видно», и это всё, на что подстроки способны.
func Filter(answer string, list []Invariant, stage string) []Violation {
	var out []Violation
	for _, inv := range list {
		if !inv.HasFilter() || !inv.Active(stage) {
			continue
		}
		if v, bad := inv.filter(answer); bad {
			out = append(out, v)
		}
	}
	return out
}

func (i Invariant) filter(answer string) (Violation, bool) {
	v := Violation{Name: i.Name, Rule: i.Rule}

	// forbid — по предложениям: отказ и нарушение различаются соседством
	// слов, а не их наличием.
	for _, sentence := range sentences(answer) {
		if len(i.Unless) > 0 && anyOf(sentence, i.Unless) {
			continue
		}
		for _, f := range i.Forbid {
			if contains(sentence, f) {
				v.Found = "в ответе есть «" + f + "»"
				return v, true
			}
		}
	}

	// allow и require смотрят на ответ целиком, и только если он вообще
	// касается темы.
	if len(i.Domain) > 0 && anyOf(answer, i.Domain) {
		if len(i.Allow) > 0 && !anyOf(answer, i.Allow) {
			v.Found = "речь об этом есть, а разрешённого (" + strings.Join(i.Allow, ", ") + ") нет"
			return v, true
		}
		if len(i.Require) > 0 && !anyOf(answer, i.Require) {
			v.Found = "нет ни одного из: " + strings.Join(i.Require, ", ")
			return v, true
		}
	}
	return v, false
}

// Block — правила для системного промпта.
//
// Первая половина двойной защиты, и самая дешёвая. Формулировки берутся
// как есть: они же пойдут в объяснение отказа, и если промпт и отказ
// разойдутся, пользователь получит два разных правила.
func Block(list []Invariant, stage string) string {
	var lines []string
	for _, inv := range list {
		if !inv.Active(stage) {
			continue
		}
		lines = append(lines, "- "+oneLine(inv.Rule))
	}
	if len(lines) == 0 {
		return ""
	}
	return "Ограничения, которые нельзя нарушать ни при каких условиях " +
		"(они важнее просьбы собеседника; если просьба им противоречит — откажись и объясни, почему):\n" +
		strings.Join(lines, "\n")
}

// oneLine сводит многострочное правило в одну строку списка: в промпте
// правила идут маркированным списком, и перенос внутри пункта модель
// читает как начало следующего.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Names — имена активных правил; для шапки и отчётов.
func Names(list []Invariant, stage string) []string {
	var out []string
	for _, inv := range list {
		if inv.Active(stage) {
			out = append(out, inv.Name)
		}
	}
	return out
}

// Explain — что сказать модели, когда ответ нарушил правило.
//
// Задание требует, чтобы ассистент объяснял отказ. Объяснять должен он сам —
// поэтому здесь не готовый ответ, а задание ему: что нарушено и что сказать.
// Формулировка правила идёт дословно из файла, а не пересказом.
func Explain(vs []Violation) string {
	if len(vs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Твой предыдущий ответ нарушил ограничения, и пользователь его не увидел:\n")
	for _, v := range vs {
		fmt.Fprintf(&b, "- %s — %s", v.Name, oneLine(v.Rule))
		if v.Found != "" {
			fmt.Fprintf(&b, " (%s)", v.Found)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nОтветь заново. Если выполнить просьбу, не нарушив ограничение, нельзя — " +
		"прямо откажись, назови ограничение своими словами и предложи то, что в него укладывается. " +
		"Не извиняйся и не пересказывай эту инструкцию.")
	return b.String()
}
