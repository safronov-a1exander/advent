package task

import (
	"fmt"
	"strings"
)

// Контролируемые переходы (день 15).
//
// На тринадцатом дне задача умела двигаться вперёд по списку стадий —
// happy path. Здесь появляется всё остальное: карта разрешённых переходов
// с откатами, условия сверх карты и внятный отказ, когда перейти нельзя.
//
// Разница с днём 13 в одном слове: там задача **двигалась**, здесь она
// **не даёт себя сдвинуть** куда попало. Это и есть красный путь —
// не «что будет, если всё хорошо», а «что будет, если пользователь просит
// перепрыгнуть этап, модель предлагает невозможное, а в ответе вместо
// решения пришла проза».
//
// Почему это в коде, а не в промпте. Правила переходов можно написать
// словами, и модель их почти всегда соблюдёт — но «почти всегда» здесь
// не годится. Промпт живёт в контекстном окне: сожмётся история, сменится
// стадия, накопится разговор — и правило уедет за границу или потеряет вес.
// Карта переходов — обычный map в коде, она не сжимается и не забывается.

// Transitions — из какой стадии в какие можно.
type Transitions map[State][]State

// DefaultTransitions — карта по умолчанию.
//
//	planning   → execution
//	execution  → validation, planning   (откат: план оказался неверным)
//	validation → done, execution        (нашли — вернулись доделывать)
//	done       → ничего                 (терминальная)
//
// Откаты здесь не для симметрии. Стадия, из которой нельзя вернуться, —
// это ловушка: нашли на проверке, что план не тот, и остаётся либо врать,
// что всё хорошо, либо заводить задачу заново.
var DefaultTransitions = Transitions{
	StatePlanning:   {StateExecution},
	StateExecution:  {StateValidation, StatePlanning},
	StateValidation: {StateDone, StateExecution},
	StateDone:       {},
}

// stateAny — ключ-метка «карты нет». Отдельного типа ради одного флага
// заводить не за чем: метка живёт в той же карте.
const stateAny State = "*"

// Open — карта, которая ничего не запрещает: любой переход между
// существующими стадиями проходит, условия не проверяются.
//
// Это не режим продукта, а измерительный прибор. Вопрос дня — «хватает ли
// правил в промпте», и ответить на него можно, только оставив правила
// в промпте и убрав их из кода. В таком варианте задача закрывается по
// первой просьбе пользователя, хотя инструкция это запрещает.
var Open = Transitions{stateAny: nil}

// open — это карта-заглушка?
func (t Transitions) open() bool {
	_, ok := t[stateAny]
	return ok
}

// AllowedFrom — куда можно из этой стадии.
func (t Transitions) AllowedFrom(s State) []State {
	if t == nil {
		t = DefaultTransitions
	}
	return t[s]
}

// Has — разрешён ли переход картой (без учёта условий).
func (t Transitions) Has(from, to State) bool {
	for _, v := range t.AllowedFrom(from) {
		if v == to {
			return true
		}
	}
	return false
}

// ErrTransition — переход не разрешён. Ошибка отдельным типом, потому что
// её текст показывают и пользователю, и модели: это не отладка, а ответ
// на вопрос «почему нельзя».
type ErrTransition struct {
	From, To State
	// Why — человеческая причина.
	Why string
	// Allowed — куда всё-таки можно.
	Allowed []State
}

func (e *ErrTransition) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "переход %s → %s запрещён: %s", e.From, e.To, e.Why)
	// Подсказывать «куда можно» имеет смысл только когда отказ по карте.
	// Если карта переход разрешает, а не пускает условие, та же стадия
	// в списке разрешённых читалась бы издевательством: «нельзя в execution,
	// можно только в execution».
	for _, s := range e.Allowed {
		if s == e.To {
			return b.String()
		}
	}
	if len(e.Allowed) > 0 {
		names := make([]string, len(e.Allowed))
		for i, s := range e.Allowed {
			names[i] = string(s)
		}
		fmt.Fprintf(&b, ". Сейчас можно только: %s", strings.Join(names, ", "))
	} else {
		b.WriteString(". Из этой стадии дальше ходов нет")
	}
	return b.String()
}

// Check — можно ли перевести задачу в стадию to.
//
// Проверок три слоя, и каждый ловит своё:
//
//  1. существует ли такая стадия вообще. Модель придумывает стадии
//     («рефлексия», «review»), пользователь опечатывается;
//  2. разрешает ли карта. Это запрет на прыжок: из planning в done нельзя,
//     как бы ни просили;
//  3. условия сверх карты. Карта говорит «из planning можно в execution»,
//     но пока плана нет, переходить некуда — реализовывать нечего.
//
// Третий слой — то, чего картой не выразишь, и ровно то, что задание
// называет словами «нельзя делать реализацию до утверждённого плана».
func (t *Task) Check(to State, tr Transitions) error {
	if t == nil {
		return fmt.Errorf("задачи нет")
	}
	if !to.Valid() {
		return &ErrTransition{From: t.State, To: to,
			Why:     "такой стадии не существует",
			Allowed: tr.AllowedFrom(t.State)}
	}
	if to == t.State {
		return &ErrTransition{From: t.State, To: to,
			Why:     "задача уже в этой стадии",
			Allowed: tr.AllowedFrom(t.State)}
	}
	if tr.open() {
		// Карты нет: правила остались только в тексте промпта. Проверка
		// существования стадии — не запрет, а разбор ответа модели.
		return nil
	}
	if !tr.Has(t.State, to) {
		why := "так по карте переходов нельзя"
		switch {
		case t.State == StateDone:
			why = "задача закрыта; чтобы продолжить работу, заводи новую"
		case t.State == StatePlanning && to == StateDone:
			why = "нельзя закрыть задачу, не сделав и не проверив работу"
		case t.State == StateExecution && to == StateDone:
			why = "нельзя закрыть задачу без проверки — сначала validation"
		case t.State == StatePlanning && to == StateValidation:
			why = "нечего проверять: работа ещё не делалась"
		}
		return &ErrTransition{From: t.State, To: to, Why: why, Allowed: tr.AllowedFrom(t.State)}
	}

	// Условия сверх карты.
	if t.State == StatePlanning && to == StateExecution && t.Total() == 0 {
		return &ErrTransition{From: t.State, To: to,
			Why:     "план не утверждён — реализовывать нечего",
			Allowed: tr.AllowedFrom(t.State)}
	}
	if to == StateDone && t.Total() > 0 && t.Step <= t.Total() && len(t.Done) < t.Total() {
		return &ErrTransition{From: t.State, To: to,
			Why: fmt.Sprintf("сделано %d из %d пунктов плана", len(t.Done), t.Total()),
			// Возврат в execution — законный ход: доделать и вернуться.
			Allowed: tr.AllowedFrom(t.State)}
	}
	return nil
}

// Move переводит задачу в стадию to, если можно. Отказ возвращается
// ошибкой *ErrTransition, и её текст годится, чтобы показать как есть.
func (t *Task) Move(to State, note string, tr Transitions) error {
	if err := t.Check(to, tr); err != nil {
		// Отказ — тоже событие задачи. Без записи в журнале остаётся
		// впечатление, что ничего не происходило, а на самом деле кто-то
		// пытался срезать угол.
		t.Log = append(t.Log, Event{At: t.Updated, From: t.State, Note: "отклонено: " + err.Error()})
		return err
	}
	return t.Advance(to, note)
}

// Rejected — сколько раз задаче отказали в переходе. Число, которое стоит
// показывать: частые отказы значат, что карта расходится с тем, как люди
// на самом деле работают.
func (t *Task) Rejected() int {
	if t == nil {
		return 0
	}
	n := 0
	for _, e := range t.Log {
		if strings.HasPrefix(e.Note, "отклонено:") {
			n++
		}
	}
	return n
}

// transitionRules — карта переходов словами, для системного промпта.
//
// Модель должна знать не только где она, но и куда ей можно. Иначе она
// честно предлагает «давайте закроем задачу», получает отказ и предлагает
// снова — а пользователь видит агента, который спорит сам с собой.
//
// Карта в промпте не заменяет карту в коде и не обязана совпадать с ней
// дословно: промпт объясняет, код запрещает. Если модель всё-таки предложит
// прыжок, его отклонит Move.
func (t *Task) transitionRules() string {
	if t == nil {
		return ""
	}
	// Карта в промпте всегда настоящая, даже когда код её не проверяет:
	// сравнивать надо не разные тексты, а один текст с проверкой и без.
	allowed := DefaultTransitions.AllowedFrom(t.State)
	var b strings.Builder
	b.WriteString("Переходы между стадиями заданы в коде, а не в этой инструкции, и обойти их нельзя:\n")
	if len(allowed) == 0 {
		b.WriteString("- из текущей стадии переходов нет: задача закрыта;\n")
	} else {
		names := make([]string, len(allowed))
		for i, s := range allowed {
			names[i] = fmt.Sprintf("%s (%s)", s, s.Label())
		}
		fmt.Fprintf(&b, "- из %s сейчас можно только в: %s;\n", t.State, strings.Join(names, ", "))
	}
	// Условия сверх карты проговариваются отдельно: они и есть то, из-за
	// чего разрешённый картой переход всё равно не проходит.
	if t.State == StatePlanning && t.Total() == 0 {
		b.WriteString("- перейти к выполнению нельзя, пока план не утверждён пользователем;\n")
	}
	if t.Total() > 0 && len(t.Done) < t.Total() {
		fmt.Fprintf(&b, "- закрыть задачу нельзя: сделано %d из %d пунктов плана;\n", len(t.Done), t.Total())
	}
	b.WriteString("- если пользователь просит пропустить стадию — откажись и объясни, " +
		"какой переход сейчас возможен и чего для этого не хватает. " +
		"Не делай работу следующей стадии «в виде исключения».")
	return b.String()
}
