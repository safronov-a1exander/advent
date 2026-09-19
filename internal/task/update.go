package task

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Продвижение задачи (день 13).
//
// Состояние надо не только показывать модели, но и двигать. Двигать его
// может либо код по жёсткому признаку, либо сама модель. Жёсткого признака
// в свободном ответе нет: «план готов, согласуем?» и «вот план, приступаю» —
// это одно слово разницы и две разные стадии.
//
// Поэтому после каждого ответа идёт служебный вызов, который смотрит на
// ответ ассистента и реплику пользователя и возвращает изменение состояния.
// Приём тот же, что у раскладки памяти дня 11, и по той же причине: ответ —
// разница, а не всё состояние целиком. Обычный ход даёт пустой объект,
// почти нулевой выход и неизменный префикс.
//
// Решение вернуть это **модели**, а не парсить ответ регулярками, сознательное.
// Разметка вида «закончи ответ строкой STEP_DONE» ломается ровно тогда, когда
// ломается всё остальное: модель увлеклась и забыла маркер. Отдельный вызов
// с температурой 0 и коротким JSON надёжнее и стоит копейки.
//
// Что он **не** решает: он предлагает, а не приказывает. Куда задаче можно
// перейти на самом деле, решает код — и на тринадцатом дне это ещё простое
// «вперёд по списку», а на пятнадцатом станет картой переходов с запретами.

// UpdateSystem — инструкция продвижения.
const UpdateSystem = `Ты следишь за состоянием рабочей задачи и после каждого хода решаешь, что в нём изменилось.

Стадии задачи:
- planning — собираем требования и согласуем план; заканчивается, когда пользователь явно одобрил план;
- execution — делаем по утверждённому плану; заканчивается, когда все шаги плана сделаны;
- validation — проверяем сделанное; заканчивается, когда проверка пройдена или найденное исправлено;
- done — задача закрыта.

Вперёд стадии идут по порядку, но назад ходить можно и нужно: если на проверке
выяснилось, что работа не доделана, предложи "advance": "execution"; если план
оказался неверным — "advance": "planning". Прыгать через стадию нельзя: такое
предложение всё равно отклонит код.

Тебе дают текущее состояние, последний вопрос пользователя и ответ ассистента.
Верни JSON — только то, что изменилось:
{"advance": "execution", "plan": ["шаг 1", "шаг 2"], "completed": "что именно сделано на этом ходу", "current": "что делаем сейчас", "expect": "чего ждём от пользователя", "why": "одной строкой, почему стадия сменилась"}

Правила:
- все поля необязательные; если ничего не изменилось — верни {};
- "advance" ставь ТОЛЬКО когда текущая стадия действительно закончена. Одного намерения мало: план, который ассистент предложил, но пользователь не одобрил, стадию не закрывает;
- "plan" возвращай один раз — когда план окончательно согласован. Пункты короткие, по-русски, в порядке выполнения;
- "completed" ставь ТОЛЬКО когда пользователь сообщил, что пункт плана закончен. Обсуждение пункта, уточняющие вопросы по нему и планы его сделать — это не «сделано»; каждое лишнее "completed" двигает счётчик шагов вперёд реальной работы;
- "expect" — одно конкретное действие, которого задача ждёт, чтобы двинуться дальше;
- ничего не выдумывай: если пользователь не одобрял план, стадия остаётся planning, сколько бы ассистент ни предлагал.`

// Update — что служебный вызов предлагает изменить.
type Update struct {
	Advance   State    `json:"advance,omitempty"`
	Plan      []string `json:"plan,omitempty"`
	Completed string   `json:"completed,omitempty"`
	Current   string   `json:"current,omitempty"`
	Expect    string   `json:"expect,omitempty"`
	Why       string   `json:"why,omitempty"`
}

// Empty — предлагать нечего.
func (u Update) Empty() bool {
	return u.Advance == "" && len(u.Plan) == 0 && u.Completed == "" && u.Current == "" && u.Expect == ""
}

// UpdatePrompt — тело запроса: состояние, реплика и ответ.
func UpdatePrompt(t *Task, question, answer string) string {
	var b strings.Builder
	b.WriteString("Текущее состояние:\n")
	fmt.Fprintf(&b, "  стадия: %s\n", t.State)
	if n := t.Total(); n > 0 {
		fmt.Fprintf(&b, "  шаг: %d из %d\n", t.Step, n)
		b.WriteString("  план:\n")
		for i, s := range t.Plan {
			fmt.Fprintf(&b, "    %d. %s\n", i+1, s)
		}
	} else {
		b.WriteString("  план: ещё не согласован\n")
	}
	if t.Current != "" {
		fmt.Fprintf(&b, "  сейчас: %s\n", t.Current)
	}
	if len(t.Done) > 0 {
		fmt.Fprintf(&b, "  сделано: %s\n", strings.Join(t.Done, "; "))
	}
	fmt.Fprintf(&b, "\nВопрос пользователя:\n%s\n\nОтвет ассистента:\n%s", strings.TrimSpace(question), strings.TrimSpace(answer))
	return b.String()
}

// ParseUpdate читает ответ служебного вызова.
//
// Это место красного пути дня 15: «LLM вернула прозу вместо чёткого ответа,
// и вся цепочка сломалась». Ломаться тут нечему — состояние просто не
// изменится, — но прозу вокруг JSON стоит пережить: модель любит
// поздороваться перед объектом и попрощаться после.
func ParseUpdate(raw string) (Update, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "```"), "```")
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") {
		raw = firstObject(raw)
	}
	var u Update
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &u); err != nil {
		return Update{}, fmt.Errorf("продвижение задачи не в JSON: %w", err)
	}
	u.Advance = State(strings.ToLower(strings.TrimSpace(string(u.Advance))))
	if u.Advance != "" && !u.Advance.Valid() {
		// Модель придумала стадию — просто игнорируем предложение перейти.
		// Ход из-за этого падать не должен: задача останется где была.
		u.Advance = ""
	}
	return u, nil
}

// Apply переносит предложение в задачу и возвращает описание изменений
// для ленты. Куда можно переходить, решает карта: служебный вызов
// предлагает, Move разрешает или отказывает.
func (t *Task) Apply(u Update, tr Transitions) string {
	if t == nil || u.Empty() {
		return ""
	}
	var parts []string
	if len(u.Plan) > 0 && t.Total() == 0 {
		t.SetPlan(u.Plan)
		parts = append(parts, fmt.Sprintf("план: %d шагов", t.Total()))
	}
	if u.Completed != "" {
		t.Complete(u.Completed)
		parts = append(parts, "сделано: "+u.Completed)
	}
	if u.Current != "" && u.Current != t.Current {
		t.Current = u.Current
		t.touch()
		parts = append(parts, "сейчас: "+u.Current)
	}
	if u.Expect != "" && u.Expect != t.Expect {
		t.Expect = u.Expect
		t.touch()
		parts = append(parts, "ждём: "+u.Expect)
	}
	if u.Advance != "" && u.Advance != t.State {
		from := t.State
		if err := t.Move(u.Advance, u.Why, tr); err != nil {
			// Отказ показывается так же подробно, как переход. Это ответ
			// на «почему задача не двигается», и его читает человек.
			parts = append(parts, err.Error())
		} else {
			parts = append(parts, fmt.Sprintf("стадия %s → %s", from, u.Advance))
		}
	}
	return strings.Join(parts, " · ")
}

// firstObject достаёт первый объект верхнего уровня из текста вокруг.
// Скобки считаются со знанием строк: "{" внутри значения не считается.
func firstObject(raw string) string {
	start := strings.IndexByte(raw, '{')
	if start < 0 {
		return raw
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(raw); i++ {
		c := raw[i]
		switch {
		case esc:
			esc = false
		case c == '\\':
			esc = inStr
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			if depth--; depth == 0 {
				return raw[start : i+1]
			}
		}
	}
	return raw
}
