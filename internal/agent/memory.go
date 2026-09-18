package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/memory"
)

// Память агента (день 11).
//
// Дни 9–10 разводили память и контекст, но «памятью» там была одна история
// одного разговора. Здесь память разложена на три слоя с разным временем
// жизни (см. internal/memory), и агент умеет три вещи:
//
//   - поднять слои задачи и пользователя при создании и подмешать их
//     в системный промпт перед каждым вопросом;
//   - разложить новую реплику по слоям служебным вызовом (режим auto);
//   - принять запись руками — из интерфейса или из сценария.
//
// Краткосрочный слой — это по-прежнему история разговора: стратегии контекста
// дней 9–10 решают, как её ужать. Слои задачи и пользователя в промпт идут
// отдельными блоками и стратегии не касаются: их незачем сжимать, они и так
// короткие, и ужимать их — значит терять ровно то, ради чего они заведены.

// Режимы памяти — поле Config.Memory.
const (
	// MemoryOff — слоёв нет, всё как на десятом дне.
	MemoryOff = ""
	// MemoryManual — слои есть и уходят в промпт, но кладёт в них только
	// пользователь. Служебных вызовов нет, лишних токенов тоже.
	MemoryManual = "manual"
	// MemoryAuto — то же плюс служебный вызов после каждой реплики:
	// раскладывает новое по слоям сам.
	MemoryAuto = "auto"
)

// MemoryModes — порядок перебора в панели.
var MemoryModes = []string{MemoryOff, MemoryManual, MemoryAuto}

// MemoryLabel — имя режима для показа.
func MemoryLabel(mode string) string {
	switch mode {
	case MemoryManual:
		return "слои, раскладка руками"
	case MemoryAuto:
		return "слои, раскладка агентом"
	}
	return "без слоёв"
}

// MemoryHint — пояснение под панелью.
func MemoryHint(mode string) string {
	switch mode {
	case MemoryManual:
		return "рабочая и долговременная память уходят в системный промпт; кладёт в них только пользователь (Ctrl+M) — служебных вызовов нет"
	case MemoryAuto:
		return "то же плюс служебный вызов после каждой реплики: раскладывает новое по слоям сам; возвращает только изменения, поэтому обычно почти нулевой выход"
	}
	return "память — только история разговора, как на десятом дне"
}

// Memory — слои памяти агента; nil, если память выключена.
func (a *Agent) Memory() *memory.Memory {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mem
}

// SetMemory подключает агенту память. Зовётся пулом при создании и подъёме.
func (a *Agent) SetMemory(m *memory.Memory) {
	a.mu.Lock()
	a.mem = m
	a.mu.Unlock()
}

// Remember кладёт запись в слой руками. Ошибка сохранения возвращается:
// молча потерянная запись хуже, чем отказ её принять.
func (a *Agent) Remember(scope memory.Scope, key, value string) error {
	a.mu.Lock()
	m := a.mem
	a.mu.Unlock()
	if m == nil {
		return fmt.Errorf("у агента нет памяти: включи режим памяти в настройках")
	}
	if err := m.Put(scope, memory.Entry{Key: key, Value: value, Source: memory.SourceManual}); err != nil {
		return err
	}
	a.mu.Lock()
	a.touch()
	a.mu.Unlock()
	a.changed()
	return nil
}

// Forget убирает ключ из слоя.
func (a *Agent) Forget(scope memory.Scope, key string) error {
	a.mu.Lock()
	m := a.mem
	a.mu.Unlock()
	if m == nil {
		return fmt.Errorf("у агента нет памяти")
	}
	if err := m.Delete(scope, key); err != nil {
		return err
	}
	a.mu.Lock()
	a.touch()
	a.mu.Unlock()
	a.changed()
	return nil
}

// memoryBlocks — блоки слоёв для системного промпта с учётом того, какие
// слои конфиг разрешил отправлять.
func (a *Agent) memoryBlocks(cfg Config, m *memory.Memory) []string {
	if m == nil || cfg.Memory == MemoryOff {
		return nil
	}
	return m.Blocks(cfg.memoryScopes()...)
}

// memoryScopes — какие слои идут в промпт. Пусто в конфиге — все.
//
// Отдельная настройка нужна из-за антипаттерна «всё в один промпт»:
// в задаче, где долговременный слой ничего не решает, его не надо
// отправлять просто потому, что он есть.
func (c Config) memoryScopes() []memory.Scope {
	if len(c.MemoryScopes) == 0 {
		return nil
	}
	out := make([]memory.Scope, 0, len(c.MemoryScopes))
	for _, s := range c.MemoryScopes {
		sc := memory.Scope(strings.ToLower(strings.TrimSpace(s)))
		if sc.Valid() {
			out = append(out, sc)
		}
	}
	return out
}

// route — служебный вызов раскладки: что из новой реплики в какой слой.
// Ошибка не ломает ход: вопрос уйдёт с прежней памятью, а в ленте
// останется предупреждение.
func (a *Agent) route(ctx context.Context, cfg Config, m *memory.Memory, hist []llm.Message, text string, gen uint64, turn *Turn, on func(Event)) {
	if m == nil || cfg.Memory != MemoryAuto {
		return
	}
	lastReply := ""
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Role == llm.RoleAssistant {
			lastReply = hist[i].Content
			break
		}
	}
	req := llm.Request{
		Model: cfg.Model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: memory.RouterSystem},
			{Role: llm.RoleUser, Content: memory.RouterPrompt(m, lastReply, text)},
		},
		// Раскладка — не творчество: температура 0, без рассуждений, ответ
		// строго JSON. Те же параметры, что у фактов дня 10.
		Temperature:    llm.F(0),
		Thinking:       &llm.Thinking{Type: "disabled"},
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}
	start := time.Now()
	resp, err := a.client.Chat(ctx, req)
	a.record(req, resp, err, "раскладка памяти", true)
	a.accountAux(resp, err)

	var plan memory.Plan
	if err == nil {
		turn.AuxCalls++
		turn.AuxPrompt += resp.Usage.PromptTokens
		turn.AuxCompletion += resp.Usage.CompletionTokens
		plan, err = memory.ParsePlan(resp.Content)
	}
	if err != nil {
		on(Event{Kind: EventContext, Label: "раскладка памяти не удалась — вопрос уйдёт с прежней", Content: err.Error()})
		return
	}

	// Разговор сбросили, пока раскладка была в полёте: краткосрочный слой
	// уже стёрт, и дописывать в него нечего. Задача и пользователь переживают
	// сброс, но реплика, которой больше нет в истории, — сомнительный
	// источник, поэтому план целиком отбрасываем.
	a.mu.Lock()
	stale := a.gen != gen
	a.mu.Unlock()
	if stale {
		return
	}

	changes, applyErr := m.Apply(plan, memory.SourceAuto)
	if applyErr != nil {
		on(Event{Kind: EventContext, Label: "память разложена, но не сохранена", Content: applyErr.Error()})
	}
	if changes == "" {
		on(Event{Kind: EventContext, Label: "память: нового нет",
			Usage: resp.Usage, CostUSD: resp.CostUSD, Latency: time.Since(start)})
		return
	}
	on(Event{Kind: EventContext, Label: memoryLabel(plan), Content: changes,
		Usage: resp.Usage, CostUSD: resp.CostUSD, Latency: time.Since(start)})

	a.mu.Lock()
	a.touch()
	a.mu.Unlock()
	a.changed()
}

func memoryLabel(p memory.Plan) string {
	byScope := map[memory.Scope]int{}
	for _, c := range p.Remember {
		byScope[c.Scope]++
	}
	var parts []string
	for _, s := range memory.Scopes {
		if n := byScope[s]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s +%d", s, n))
		}
	}
	if len(p.Forget) > 0 {
		parts = append(parts, fmt.Sprintf("забыто %d", len(p.Forget)))
	}
	if len(parts) == 0 {
		return "память: нового нет"
	}
	return "память разложена: " + strings.Join(parts, ", ")
}
