package memory

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Раскладка по слоям (день 11).
//
// «Явно выбирать, что и куда сохраняется» можно двумя способами, и оба
// нужны. Руками — пользователь сам говорит «это в долговременную»: надёжно,
// но человек ленив и половину не запишет. Служебным вызовом — модель сама
// раскладывает каждую реплику по слоям: ничего не забудет, но ошибётся.
//
// Здесь второй способ. Отличие от фактов дня 10 существенное: факты каждый
// ход переписывали весь блок целиком, и это вышло дорого — выход вдвое
// больше остальных вариантов, а меняющийся системный промпт убивал кэш
// префикса (85% → 1%). Поэтому раскладка возвращает не весь набор, а
// только разницу: что запомнить и что забыть. Обычная реплика даёт пустые
// списки и почти нулевой выход, а блоки слоёв между ходами не меняются —
// значит, префикс остаётся прежним и кэш работает.

// RouterSystem — инструкция раскладки. Слои описаны через время жизни,
// а не через названия: «долговременная» и «рабочая» модель понимает
// по-разному, а «переживёт эту задачу» — однозначно.
const RouterSystem = `Ты раскладываешь новую информацию из разговора по трём слоям памяти ассистента.

Слои и их время жизни:
- "user" — долговременная память о собеседнике. Переживает и этот разговор, и эту задачу. Сюда: кто он, как его зовут, чем занимается, на чём пишет, как просит к себе обращаться, устойчивые предпочтения и запреты, знания о нём, которые пригодятся в любом будущем разговоре.
- "task" — рабочая память текущей задачи. Переживает разговор, но не переживёт смену задачи. Сюда: цель задачи, требования, сроки, бюджет, принятые решения, договорённости, открытые вопросы.
- "chat" — краткосрочная память текущего разговора. Сюда: мелочи, нужные только сейчас — на чём остановились, что пользователь просил показать следующим.

Тебе дают текущее содержимое слоёв, последний ответ ассистента и новое сообщение пользователя.
Верни JSON:
{"remember": [{"scope": "user|task|chat", "key": "короткий ключ", "value": "значение"}], "forget": [{"scope": "...", "key": "..."}]}

Правила:
- возвращай ТОЛЬКО изменения. Если новое сообщение ничего не добавляет — верни {"remember": [], "forget": []};
- ключи короткие, по-русски, в нижнем регистре: "имя", "стек", "бюджет", "срок", "запрещено";
- значения — дословно, с числами и единицами, без пересказа;
- если значение ключа изменилось — положи его в remember с новым значением, слой тот же;
- если пользователь отменил что-то — положи ключ в forget;
- один факт кладётся в один слой, не дублируй его в два;
- сомневаешься между user и task — выбирай task: задача кончится, и лишнее уйдёт с ней;
- решение, которое ассистент предложил, а пользователь принял, — запомни;
- советы ассистента, которые пользователь не принял, не запоминай;
- ничего не выдумывай и ничего не вычисляй за пользователя.`

// Change — одна правка памяти, предложенная раскладкой.
type Change struct {
	Scope Scope  `json:"scope"`
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}

// Plan — что раскладка предлагает изменить.
type Plan struct {
	Remember []Change `json:"remember"`
	Forget   []Change `json:"forget"`
}

// Empty — раскладка ничего не предложила.
func (p Plan) Empty() bool { return len(p.Remember) == 0 && len(p.Forget) == 0 }

// RouterPrompt — тело запроса раскладки: текущая память, последний ответ
// ассистента (чтобы понимать «да, давай так») и новая реплика.
func RouterPrompt(m *Memory, lastReply, text string) string {
	var b strings.Builder
	b.WriteString("Текущее содержимое слоёв:\n")
	for _, s := range Scopes {
		l := m.Layer(s)
		fmt.Fprintf(&b, "%s:\n", s)
		if l.Len() == 0 {
			b.WriteString("  (пусто)\n")
			continue
		}
		for _, e := range l.Entries() {
			fmt.Fprintf(&b, "  %s: %s\n", e.Key, e.Value)
		}
	}
	reply := strings.TrimSpace(lastReply)
	if reply == "" {
		reply = "(ответов ещё не было)"
	}
	fmt.Fprintf(&b, "\nПоследний ответ ассистента:\n%s\n\nНовое сообщение пользователя:\n%s", reply, strings.TrimSpace(text))
	return b.String()
}

// ParsePlan читает ответ раскладки. Неизвестные слои и пустые ключи
// отбрасываются молча: модель иногда придумывает четвёртый слой, и ход
// из-за этого падать не должен.
func ParsePlan(raw string) (Plan, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "```"), "```")
	var p Plan
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &p); err != nil {
		return Plan{}, fmt.Errorf("раскладка не в JSON: %w", err)
	}
	p.Remember = clean(p.Remember, true)
	p.Forget = clean(p.Forget, false)
	return p, nil
}

func clean(in []Change, needValue bool) []Change {
	var out []Change
	for _, c := range in {
		c.Scope = Scope(strings.ToLower(strings.TrimSpace(string(c.Scope))))
		c.Key = strings.TrimSpace(c.Key)
		c.Value = strings.TrimSpace(c.Value)
		if !c.Scope.Valid() || c.Key == "" || (needValue && c.Value == "") {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Apply переносит план в память. Возвращает описание изменений для ленты:
// раскладка идёт служебным вызовом, и пользователь должен видеть, что она
// решила, — иначе кривая запись незаметно испортит все следующие ответы.
func (m *Memory) Apply(p Plan, src Source) (string, error) {
	var (
		parts []string
		errs  []string
	)
	for _, c := range p.Remember {
		prev, had := m.Layer(c.Scope).Get(c.Key)
		if err := m.Put(c.Scope, Entry{Key: c.Key, Value: c.Value, Source: src}); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		switch {
		case !had:
			parts = append(parts, fmt.Sprintf("+ %s/%s: %s", c.Scope, c.Key, c.Value))
		case prev.Value != c.Value:
			parts = append(parts, fmt.Sprintf("~ %s/%s: %s → %s", c.Scope, c.Key, prev.Value, c.Value))
		}
	}
	for _, c := range p.Forget {
		if _, had := m.Layer(c.Scope).Get(c.Key); !had {
			continue
		}
		if err := m.Delete(c.Scope, c.Key); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		parts = append(parts, fmt.Sprintf("− %s/%s", c.Scope, c.Key))
	}
	var err error
	if len(errs) > 0 {
		err = fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	if len(parts) == 0 {
		return "", err
	}
	return strings.Join(parts, "\n"), err
}
