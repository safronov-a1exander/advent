package invariant

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Смысловая проверка (день 14).
//
// Это основной механизм, а не запасной. Подстрока отвечает на вопрос
// «встретилось ли слово», а правило почти всегда про другое: предложил
// ассистент запрещённое или отказался от него, назвал срок с оговоркой
// или без, уложился в бюджет или нет. Ни один из этих вопросов списком
// слов не выражается.
//
// Поэтому проверяющей модели уходят **все** активные правила, включая
// те, у которых есть быстрый фильтр. Фильтр отработал раньше и бесплатно;
// если он ничего не нашёл, это ещё ничего не значит, и правило всё равно
// проверяется по смыслу.
//
// Цена ровно та, о которой предупреждал курс: один дополнительный вызов
// на каждый ход. Поэтому смысловая проверка — отдельный режим, а не
// умолчание, и сомнения она трактует в пользу ответа: ложное срабатывание
// заставит переписывать хороший ответ, а это хуже пропущенного спорного.

// JudgeSystem — инструкция проверяющему.
const JudgeSystem = `Ты проверяешь ответ ассистента на соблюдение жёстких ограничений. Ты не улучшаешь ответ и не оцениваешь его качество — только смотришь, нарушено ли конкретное правило.

Тебе дают список правил и ответ ассистента. Верни JSON:
{"violations": [{"name": "имя правила", "why": "что именно в ответе его нарушает, одной фразой"}]}

Правила проверки:
- нарушение должно быть в самом ответе, а не в том, что ассистент мог бы сказать дальше;
- отказ ассистента что-то сделать со ссылкой на ограничение — это СОБЛЮДЕНИЕ правила, а не нарушение. Упоминание запрещённого в объяснении отказа тоже не нарушение;
- сомневаешься — НЕ считай нарушением. Ложное срабатывание заставит ассистента переписывать хороший ответ;
- если нарушений нет, верни {"violations": []}.`

// JudgePrompt — тело запроса: все активные правила и ответ.
func JudgePrompt(list []Invariant, stage, answer string) string {
	var b strings.Builder
	b.WriteString("Правила:\n")
	for _, inv := range list {
		if !inv.Active(stage) {
			continue
		}
		ask := strings.TrimSpace(inv.Ask)
		if ask == "" {
			ask = inv.Rule
		}
		fmt.Fprintf(&b, "- %s: %s\n", inv.Name, oneLine(ask))
	}
	fmt.Fprintf(&b, "\nОтвет ассистента:\n%s", strings.TrimSpace(answer))
	return b.String()
}

// NeedsJudge — есть ли что проверять по смыслу на этой стадии.
func NeedsJudge(list []Invariant, stage string) bool {
	for _, inv := range list {
		if inv.Active(stage) {
			return true
		}
	}
	return false
}

type judgeReply struct {
	Violations []struct {
		Name string `json:"name"`
		Why  string `json:"why"`
	} `json:"violations"`
}

// ParseJudge читает ответ проверяющего. Имена, которых нет в списке правил,
// отбрасываются: модель иногда придумывает правила, которых ей не давали,
// и переписывать ответ из-за выдуманного нарушения — худшее, что можно
// сделать.
func ParseJudge(raw string, list []Invariant, stage string) ([]Violation, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "```"), "```")
	var r judgeReply
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &r); err != nil {
		return nil, fmt.Errorf("смысловая проверка ответила не JSON: %w", err)
	}
	known := map[string]Invariant{}
	for _, inv := range list {
		if inv.Active(stage) {
			known[strings.ToLower(strings.TrimSpace(inv.Name))] = inv
		}
	}
	var out []Violation
	for _, v := range r.Violations {
		inv, ok := known[strings.ToLower(strings.TrimSpace(v.Name))]
		if !ok {
			continue
		}
		out = append(out, Violation{Name: inv.Name, Rule: inv.Rule, Found: strings.TrimSpace(v.Why), ByJudge: true})
	}
	return out, nil
}
