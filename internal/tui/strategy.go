package tui

import "strings"

// Стратегии рассуждения, добавленные на шаге 3. Выбираются в панели и применяются
// к обычному вопросу в чате — то есть сравнить способы можно вручную,
// не описывая YAML-сценарий.
const (
	StrategyDirect  = ""
	StrategyCoT     = "пошагово"
	StrategyMeta    = "мета-промпт"
	StrategyExperts = "эксперты"
)

// Strategies — порядок перебора в панели.
var Strategies = []string{StrategyDirect, StrategyCoT, StrategyMeta, StrategyExperts}

// ChainStep — один вызов LLM внутри стратегии.
//
// Prompt строится из вопроса пользователя и ответов предыдущих шагов:
// вместо шаблонизатора здесь просто функция, так проще читать.
type ChainStep struct {
	Label string
	// System пуст — берётся системный промпт из настроек.
	System string
	// Build получает исходный вопрос и карту ответов предыдущих шагов.
	Build func(task string, prev map[string]string) string
	// Capture — под каким именем положить ответ для следующих шагов.
	Capture string
	// Final — последний шаг, его ответ уходит в историю диалога
	// и печатается целиком.
	Final bool
}

// Chain разворачивает выбранную стратегию в цепочку вызовов.
// Для прямого ответа это один шаг, для экспертов — четыре.
func (s *Settings) Chain(task string) []ChainStep {
	switch s.Strategy {
	case StrategyCoT:
		return []ChainStep{{
			Label: "пошаговое решение",
			Build: func(t string, _ map[string]string) string {
				return t + "\n\nРешай пошагово: выпиши каждое рассуждение отдельной " +
					"строкой и только потом дай итог."
			},
			Final: true,
		}}

	case StrategyMeta:
		return []ChainStep{
			{
				Label:   "шаг 1 — модель пишет промпт",
				System:  "Ты инженер промптов. Твой ответ — только текст промпта, без пояснений и без решения задачи.",
				Capture: "prompt",
				Build: func(t string, _ map[string]string) string {
					return "Составь максимально эффективный промпт для точного решения " +
						"этой задачи. Учти типовые ошибки: неверный порядок операций, " +
						"потерю условия, арифметику в уме.\n\nЗадача:\n" + t
				},
			},
			{
				Label: "шаг 2 — решение по нему",
				Build: func(t string, prev map[string]string) string {
					return prev["prompt"] + "\n\nЗадача:\n" + t
				},
				Final: true,
			},
		}

	case StrategyExperts:
		return []ChainStep{
			{
				Label:   "аналитик",
				System:  "Ты аналитик. Разбираешь условие, но ничего не считаешь.",
				Capture: "analysis",
				Build: func(t string, _ map[string]string) string {
					return "Разбери условие: выпиши исходные данные, что именно " +
						"спрашивается и в каком порядке считать. Ответ не давай.\n\n" + t
				},
			},
			{
				Label:   "инженер",
				System:  "Ты инженер-расчётчик. Считаешь аккуратно и по шагам.",
				Capture: "solution",
				Build: func(t string, prev map[string]string) string {
					return "Реши по этому разбору, каждое вычисление отдельной строкой.\n\n" +
						"Разбор:\n" + prev["analysis"] + "\n\nЗадача:\n" + t
				},
			},
			{
				Label:   "критик",
				System:  "Ты критик. Ищешь ошибки, а не хвалишь.",
				Capture: "critique",
				Build: func(t string, prev map[string]string) string {
					return "Проверь решение: сходятся ли числа, не потеряно ли условие, " +
						"верен ли порядок операций. Нашёл ошибку — назови и приведи верное " +
						"вычисление.\n\nРешение:\n" + prev["solution"] + "\n\nЗадача:\n" + t
				},
			},
			{
				Label: "синтез",
				Build: func(t string, prev map[string]string) string {
					return "Сведи всё в финальный ответ. Если критик нашёл ошибку — " +
						"верным считай его вариант.\n\nРешение инженера:\n" + prev["solution"] +
						"\n\nЗамечания критика:\n" + prev["critique"] + "\n\nЗадача:\n" + t
				},
				Final: true,
			},
		}

	default:
		return []ChainStep{{
			Label: "прямой ответ",
			Build: func(t string, _ map[string]string) string { return t },
			Final: true,
		}}
	}
}

// StrategyHint — пояснение под панелью.
func StrategyHint(name string) string {
	switch name {
	case StrategyCoT:
		return "к вопросу добавляется просьба рассуждать вслух: промежуточные шаги попадают в контекст"
	case StrategyMeta:
		return "два вызова: сначала модель пишет промпт для себя, потом решает по нему"
	case StrategyExperts:
		return "четыре вызова: аналитик, инженер, критик, синтез — каждый видит предыдущих"
	default:
		return "один вызов без подсказок о том, как думать"
	}
}

func strategyLabel(name string) string {
	if strings.TrimSpace(name) == "" {
		return "прямой ответ"
	}
	return name
}
