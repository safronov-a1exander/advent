package invariant

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Проверка ответа (день 14).
//
// Один вызов на ход: проверяющей модели уходят все активные правила и
// ответ целиком, обратно — список нарушенных. Не по правилу на вызов:
// правила проверяются вместе, потому что дороже всего здесь не токены,
// а раунд-трипы, и потому что нарушение часто видно только в целом
// («уложился в бюджет» зависит от всего ответа, а не от одной фразы).
//
// Проверяющей идёт та же модель, что и отвечает, — с температурой 0,
// без рассуждений и с JSON на выходе. Отдельная слабая модель была бы
// дешевле, но правила написаны прозой на русском, и проверка «отказался
// или предложил» — ровно та задача, на которой слабые модели ошибаются
// в обе стороны. Класс модели для проверки можно задать конфигом агента.

// CheckSystem — инструкция проверяющему.
//
// Главное в ней — третий пункт. Проверяющий, которому не сказано иначе,
// считает нарушением любое упоминание запрещённого, включая отказ от него,
// и тогда агент бесконечно переписывает правильные ответы.
const CheckSystem = `Ты проверяешь ответ ассистента на соблюдение жёстких ограничений. Ты не улучшаешь ответ и не оцениваешь его качество — только смотришь, нарушено ли конкретное правило.

Тебе дают список правил и ответ ассистента. Верни JSON:
{"violations": [{"name": "имя правила", "why": "что именно в ответе его нарушает, одной фразой"}]}

Правила проверки:
- нарушение должно быть в самом ответе, а не в том, что ассистент мог бы сказать дальше;
- отказ ассистента что-то сделать со ссылкой на ограничение — это СОБЛЮДЕНИЕ правила. Упоминание запрещённого внутри отказа («ЮKassa подключать не будем») нарушением НЕ является;
- нарушение — это когда ассистент предлагает, советует или делает запрещённое;
- сомневаешься — НЕ считай нарушением. Ложное срабатывание заставит ассистента переписывать хороший ответ;
- если нарушений нет, верни {"violations": []}.`

// CheckPrompt — тело запроса: все активные правила и ответ.
func CheckPrompt(list []Invariant, stage, answer string) string {
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

// Applies — есть ли что проверять на этой стадии.
func Applies(list []Invariant, stage string) bool {
	for _, inv := range list {
		if inv.Active(stage) {
			return true
		}
	}
	return false
}

type checkReply struct {
	Violations []struct {
		Name string `json:"name"`
		Why  string `json:"why"`
	} `json:"violations"`
}

// ParseCheck читает ответ проверяющего. Имена, которых нет в списке правил,
// отбрасываются: модель иногда придумывает правила, которых ей не давали,
// и переписывать ответ из-за выдуманного нарушения — худшее, что можно
// сделать.
func ParseCheck(raw string, list []Invariant, stage string) ([]Violation, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "```"), "```")
	var r checkReply
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &r); err != nil {
		return nil, fmt.Errorf("проверка ответила не JSON: %w", err)
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
		out = append(out, Violation{Name: inv.Name, Rule: inv.Rule, Why: strings.TrimSpace(v.Why)})
	}
	return out, nil
}
