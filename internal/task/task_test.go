package task

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBlockShowsWhereWeAre(t *testing.T) {
	tk := New("бот", "Telegram-бот для записи")
	tk.SetPlan([]string{"схема БД", "хендлеры", "напоминания"})
	tk.Complete("схема БД готова")
	tk.Current = "пишу хендлеры"
	tk.Expect = "подтвердить формат слотов"

	b := tk.Block()
	for _, want := range []string{
		"[ЗАДАЧА]  Telegram-бот для записи",
		"[СТАДИЯ]  planning (планирование), шаг 2 из 3",
		"✓ 1. схема БД",
		"→ 2. хендлеры",
		"[СДЕЛАНО]",
		"[ЖДЁМ]    подтвердить формат слотов",
		"из planning сейчас можно только в: execution (выполнение)",
	} {
		if !strings.Contains(b, want) {
			t.Fatalf("в блоке нет %q:\n%s", want, b)
		}
	}
	if (*Task)(nil).Block() != "" {
		t.Fatal("без задачи блок пустой")
	}
}

func TestApplyMovesStateAndRejectsJumps(t *testing.T) {
	tk := New("бот", "бот")

	// План согласован — стадия закрывается.
	got := tk.Apply(Update{
		Plan:    []string{"схема", "хендлеры"},
		Advance: StateExecution,
		Current: "делаю схему",
		Why:     "пользователь одобрил план",
	}, DefaultTransitions)
	if tk.State != StateExecution || tk.Total() != 2 || tk.Step != 1 {
		t.Fatalf("после утверждения плана: %s шаг %d из %d", tk.State, tk.Step, tk.Total())
	}
	if !strings.Contains(got, "план: 2 шагов") || !strings.Contains(got, "стадия planning → execution") {
		t.Fatalf("описание изменений: %q", got)
	}

	// Прыжок через стадию отклоняется кодом, а не уговорами.
	got = tk.Apply(Update{Advance: StateDone}, DefaultTransitions)
	if tk.State != StateExecution {
		t.Fatalf("прыжок execution → done прошёл: %s", tk.State)
	}
	if !strings.Contains(got, "запрещён") {
		t.Fatalf("об отказе не сказано: %q", got)
	}

	// Пустое предложение ничего не меняет и ничего не пишет в ленту.
	if got := tk.Apply(Update{}, DefaultTransitions); got != "" {
		t.Fatalf("пустое обновление что-то поменяло: %q", got)
	}

	// План кладётся один раз: второй согласованный план не затирает первый.
	tk.Apply(Update{Plan: []string{"другое", "совсем"}}, DefaultTransitions)
	if tk.Total() != 2 || tk.Plan[0] != "схема" {
		t.Fatalf("план перезаписан: %v", tk.Plan)
	}
}

func TestParseUpdateIgnoresInventedStates(t *testing.T) {
	u, err := ParseUpdate("```json\n" + `{"advance":"рефлексия","current":"думаю"}` + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	if u.Advance != "" {
		t.Fatalf("выдуманная стадия принята: %q", u.Advance)
	}
	if u.Current != "думаю" {
		t.Fatal("остальные поля должны читаться")
	}

	if u, err := ParseUpdate("{}"); err != nil || !u.Empty() {
		t.Fatalf("пустой объект — норма: %v %v", u, err)
	}
	if _, err := ParseUpdate("конечно, вот обновление:"); err == nil {
		t.Fatal("не-JSON должен быть ошибкой")
	}
}

func TestResumeTellsWhereWeStopped(t *testing.T) {
	tk := New("бот", "бот записи")
	tk.SetPlan([]string{"а", "б", "в"})
	_ = tk.Advance(StateExecution, "план одобрен")
	tk.Complete("а сделано")
	tk.Current = "делаю б"
	tk.Expect = "прислать макет"

	r := tk.Resume()
	for _, want := range []string{"выполнение", "шаг 2 из 3", "делаю б", "прислать макет", "Сделано пунктов: 1"} {
		if !strings.Contains(r, want) {
			t.Fatalf("в «на чём остановились» нет %q: %s", want, r)
		}
	}
}

func TestStoreRoundTripAndPause(t *testing.T) {
	dir := t.TempDir()
	st := NewFileStore(dir)

	tk := New("бот-барбершопа", "бот записи")
	tk.SetPlan([]string{"схема", "хендлеры"})
	_ = tk.Advance(StateExecution, "план одобрен")
	tk.Complete("схема готова")
	tk.Current = "пишу хендлеры"
	if err := st.Save(tk); err != nil {
		t.Fatal(err)
	}

	// Пауза и продолжение: другой процесс поднимает то же состояние.
	again := NewFileStore(dir)
	back, err := again.Load("бот-барбершопа")
	if err != nil {
		t.Fatal(err)
	}
	if back == nil {
		t.Fatal("задача не поднялась")
	}
	if back.State != StateExecution || back.Step != 2 || back.Current != "пишу хендлеры" {
		t.Fatalf("состояние не то: %+v", back)
	}
	if len(back.Log) < 2 {
		t.Fatalf("журнал переходов потерялся: %+v", back.Log)
	}

	// Задачи, которой не было, — не ошибка: начинаем с чистого листа.
	none, err := again.Load("которой-нет")
	if err != nil || none != nil {
		t.Fatalf("несуществующая задача: %v %v", none, err)
	}

	ids, err := again.List()
	if err != nil || len(ids) != 1 {
		t.Fatalf("список задач: %v %v", ids, err)
	}
	if _, err := st.Load(filepath.Base("бот-барбершопа")); err != nil {
		t.Fatal(err)
	}
}
