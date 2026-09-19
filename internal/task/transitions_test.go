package task

import (
	"errors"
	"strings"
	"testing"
)

// Карта: что разрешено, что нет и что откат.
func TestMapAllowsRollbacksAndForbidsJumps(t *testing.T) {
	cases := []struct {
		from, to State
		want     bool
		why      string
	}{
		{StatePlanning, StateExecution, true, "вперёд по карте"},
		{StateExecution, StateValidation, true, "вперёд по карте"},
		{StateValidation, StateDone, true, "вперёд по карте"},
		{StateExecution, StatePlanning, true, "откат: план оказался неверным"},
		{StateValidation, StateExecution, true, "откат: проверка нашла недоделку"},
		{StatePlanning, StateDone, false, "прыжок через две стадии"},
		{StatePlanning, StateValidation, false, "проверять нечего"},
		{StateExecution, StateDone, false, "финал без валидации"},
		{StateDone, StateExecution, false, "из закрытой задачи ходов нет"},
		{StateDone, StatePlanning, false, "из закрытой задачи ходов нет"},
	}
	for _, c := range cases {
		if got := DefaultTransitions.Has(c.from, c.to); got != c.want {
			t.Fatalf("%s → %s = %v, ожидали %v (%s)", c.from, c.to, got, c.want, c.why)
		}
	}
}

// Отказ объясняет, что не так и куда всё-таки можно: этот текст видит
// и пользователь, и модель.
func TestRefusalExplainsAndOffersWayOut(t *testing.T) {
	tk := New("бот", "бот")
	tk.SetPlan([]string{"схема", "хендлеры"})
	_ = tk.Advance(StateExecution, "план утверждён")

	err := tk.Move(StateDone, "пользователь торопится", DefaultTransitions)
	if err == nil {
		t.Fatal("execution → done прошёл без валидации")
	}
	var te *ErrTransition
	if !errors.As(err, &te) {
		t.Fatalf("ошибка не *ErrTransition: %T", err)
	}
	for _, want := range []string{"execution → done запрещён", "без проверки", "можно только: validation, planning"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("в отказе нет %q: %s", want, err)
		}
	}
	if tk.State != StateExecution {
		t.Fatalf("стадия всё-таки сменилась: %s", tk.State)
	}
}

// Условия сверх карты: карта разрешает, но переходить пока не с чем.
func TestConditionsBeyondTheMap(t *testing.T) {
	// Нельзя делать реализацию до утверждённого плана — формулировка задания.
	tk := New("бот", "бот")
	err := tk.Move(StateExecution, "", DefaultTransitions)
	if err == nil || !strings.Contains(err.Error(), "план не утверждён") {
		t.Fatalf("переход без плана: %v", err)
	}
	// Подсказки «можно только: execution» тут быть не должно: она про тот же
	// переход, в котором только что отказали.
	if strings.Contains(err.Error(), "можно только") {
		t.Fatalf("отказ по условию предлагает тот же переход: %s", err)
	}

	tk.SetPlan([]string{"схема", "хендлеры"})
	if err := tk.Move(StateExecution, "план утверждён", DefaultTransitions); err != nil {
		t.Fatalf("с планом переход должен пройти: %v", err)
	}

	// Нельзя закрыть задачу с недоделанным планом.
	tk.Complete("схема готова")
	_ = tk.Advance(StateValidation, "")
	err = tk.Move(StateDone, "", DefaultTransitions)
	if err == nil || !strings.Contains(err.Error(), "сделано 1 из 2") {
		t.Fatalf("закрытие с недоделанным планом: %v", err)
	}

	// Откат назад доделывать — законный ход и не требует условий.
	if err := tk.Move(StateExecution, "нашли недоделку", DefaultTransitions); err != nil {
		t.Fatalf("откат validation → execution: %v", err)
	}
	tk.Complete("хендлеры готовы")
	_ = tk.Advance(StateValidation, "")
	if err := tk.Move(StateDone, "проверено", DefaultTransitions); err != nil {
		t.Fatalf("закрытие после доделки: %v", err)
	}
}

// Выдуманная стадия — самый частый сход с маршрута: так ошибается и модель,
// и человек в командной строке.
func TestInventedStateIsRefusedNotApplied(t *testing.T) {
	tk := New("бот", "бот")
	err := tk.Move("review", "", DefaultTransitions)
	if err == nil || !strings.Contains(err.Error(), "не существует") {
		t.Fatalf("выдуманная стадия: %v", err)
	}
	if tk.State != StatePlanning {
		t.Fatalf("стадия сменилась на выдуманную: %s", tk.State)
	}
}

// Отказы попадают в журнал: без этого выглядит, будто ничего не происходило,
// а на самом деле кто-то пытался срезать угол.
func TestRejectionsAreLogged(t *testing.T) {
	tk := New("бот", "бот")
	_ = tk.Move(StateDone, "", DefaultTransitions)
	_ = tk.Move(StateValidation, "", DefaultTransitions)
	if tk.Rejected() != 2 {
		t.Fatalf("отказов в журнале %d, ожидали 2", tk.Rejected())
	}
	// Первая запись — «задача заведена», дальше два отказа.
	if n := len(tk.Log); n != 3 {
		t.Fatalf("записей в журнале %d, ожидали 3", n)
	}
	if !strings.HasPrefix(tk.Log[1].Note, "отклонено:") {
		t.Fatalf("запись отказа: %q", tk.Log[1].Note)
	}
	// Успешный переход отказом не считается.
	tk.SetPlan([]string{"шаг"})
	_ = tk.Move(StateExecution, "план утверждён", DefaultTransitions)
	if tk.Rejected() != 2 {
		t.Fatalf("успешный переход посчитали отказом: %d", tk.Rejected())
	}
}

// Красный путь дня 15: модель вернула прозу вокруг JSON. Ход не ломается.
func TestProseAroundJSONSurvives(t *testing.T) {
	u, err := ParseUpdate(`Конечно! Вот изменения состояния:
{"advance": "execution", "why": "план одобрен"}
Если нужно что-то ещё — дайте знать.`)
	if err != nil {
		t.Fatalf("проза вокруг объекта не разобралась: %v", err)
	}
	if u.Advance != StateExecution || u.Why != "план одобрен" {
		t.Fatalf("разобрали не то: %+v", u)
	}

	// Скобка внутри строки не должна обрывать объект.
	u, err = ParseUpdate(`вот: {"current": "правлю шаблон {name}", "expect": "ответ"} всё`)
	if err != nil {
		t.Fatalf("скобка в значении сломала разбор: %v", err)
	}
	if u.Current != "правлю шаблон {name}" {
		t.Fatalf("значение со скобкой: %q", u.Current)
	}

	// Совсем без объекта — ошибка, но состояние задачи её переживает.
	tk := New("бот", "бот")
	if _, err := ParseUpdate("Не могу определить изменения состояния."); err == nil {
		t.Fatal("проза без объекта должна быть ошибкой")
	}
	if tk.State != StatePlanning {
		t.Fatalf("состояние поехало: %s", tk.State)
	}
}
