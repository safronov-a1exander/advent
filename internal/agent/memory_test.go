package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/memory"
)

// routeAnswer — ответ «провайдера» на запрос раскладки. Реплика вида
// «слой/ключ = значение» кладётся в названный слой, «забудь слой/ключ»
// удаляет, остальное не меняет ничего — как и на живом API, где обычная
// реплика чаще всего не добавляет памяти.
func routeAnswer(msgs []llm.Message) (string, bool) {
	if !strings.HasPrefix(msgs[0].Content, "Ты раскладываешь новую информацию") {
		return "", false
	}
	_, msg, _ := strings.Cut(msgs[1].Content, "Новое сообщение пользователя:\n")
	msg = strings.TrimSpace(msg)

	remember := []map[string]string{}
	forget := []map[string]string{}
	if rest, ok := strings.CutPrefix(msg, "забудь "); ok {
		if scope, key, ok := strings.Cut(rest, "/"); ok {
			forget = append(forget, map[string]string{
				"scope": strings.TrimSpace(scope), "key": strings.TrimSpace(key)})
		}
	} else if lhs, val, ok := strings.Cut(msg, "="); ok {
		if scope, key, ok := strings.Cut(lhs, "/"); ok {
			remember = append(remember, map[string]string{
				"scope": strings.TrimSpace(scope),
				"key":   strings.TrimSpace(key),
				"value": strings.TrimSpace(val)})
		}
	}
	out, _ := json.Marshal(map[string]any{"remember": remember, "forget": forget})
	return string(out), true
}

// routeProvider отвечает на запросы раскладки через routeAnswer.
type routeProvider struct{ *fakeLLM }

func (p routeProvider) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if out, ok := routeAnswer(req.Messages); ok {
		p.fakeLLM.mu.Lock()
		p.fakeLLM.requests = append(p.fakeLLM.requests, req)
		p.fakeLLM.mu.Unlock()
		return &llm.Response{Model: req.Model, Content: out,
			Usage: llm.Usage{PromptTokens: 60, CompletionTokens: 8}}, nil
	}
	return p.fakeLLM.Chat(ctx, req)
}

func (p routeProvider) ChatStream(ctx context.Context, req llm.Request, on func(llm.Chunk) error) (*llm.Response, error) {
	return p.Chat(ctx, req)
}

// systemOf — системный промпт последнего запроса.
func systemOf(req llm.Request) string {
	if len(req.Messages) > 0 && req.Messages[0].Role == llm.RoleSystem {
		return req.Messages[0].Content
	}
	return ""
}

func TestMemoryOffKeepsSystemPromptClean(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(f, "fake", nil).Spawn(Config{Model: "m", System: "sys"})
	ask(t, a, "привет")
	if got := systemOf(f.last()); got != "sys" {
		t.Fatalf("без памяти системный промпт не должен меняться: %q", got)
	}
	if a.Memory() != nil {
		t.Fatal("выключенная память не должна создавать слоёв")
	}
}

func TestManualMemoryGoesIntoSystemPrompt(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(f, "fake", nil).Spawn(Config{
		Model: "m", System: "sys", Memory: MemoryManual, User: "саша", Task: "барбершоп"})

	if err := a.Remember(memory.ScopeUser, "стек", "Go"); err != nil {
		t.Fatal(err)
	}
	if err := a.Remember(memory.ScopeTask, "срок", "6 недель"); err != nil {
		t.Fatal(err)
	}
	ask(t, a, "привет")

	sys := systemOf(f.last())
	for _, want := range []string{"sys", "стек: Go", "срок: 6 недель"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("в системном промпте нет %q:\n%s", want, sys)
		}
	}
	// Ручной режим — без служебных вызовов: один ход, один вызов API.
	if tr := a.Turns(); tr[len(tr)-1].AuxCalls != 0 {
		t.Fatal("ручная раскладка не должна стоить служебных вызовов")
	}
}

func TestAutoRoutingSortsRepliesIntoLayers(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(routeProvider{f}, "fake", nil).Spawn(Config{
		Model: "m", System: "sys", Memory: MemoryAuto, User: "саша", Task: "барбершоп"})

	for _, q := range []string{"user/стек = Go", "task/срок = 6 недель", "просто реплика"} {
		ask(t, a, q)
	}

	m := a.Memory()
	if e, ok := m.Layer(memory.ScopeUser).Get("стек"); !ok || e.Value != "Go" {
		t.Fatalf("долговременный слой: %+v", m.Layer(memory.ScopeUser).Entries())
	}
	if e, ok := m.Layer(memory.ScopeTask).Get("срок"); !ok || e.Source != memory.SourceAuto {
		t.Fatalf("рабочий слой: %+v", e)
	}
	// Реплика, которая ничего не добавила, не должна плодить записей —
	// раскладка возвращает разницу, а не весь набор.
	if n := m.Layer(memory.ScopeUser).Len() + m.Layer(memory.ScopeTask).Len(); n != 2 {
		t.Fatalf("в памяти %d записей, ожидали 2", n)
	}
	// Каждый ход — один служебный вызов раскладки.
	for i, tr := range a.Turns() {
		if tr.AuxCalls != 1 {
			t.Fatalf("ход %d: служебных вызовов %d, ожидали 1", i+1, tr.AuxCalls)
		}
	}
}

func TestAutoRoutingForgets(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(routeProvider{f}, "fake", nil).Spawn(Config{
		Model: "m", Memory: MemoryAuto, User: "саша", Task: "барбершоп"})
	ask(t, a, "task/бюджет = 150000")
	ask(t, a, "забудь task/бюджет")

	if _, ok := a.Memory().Layer(memory.ScopeTask).Get("бюджет"); ok {
		t.Fatal("отменённый ключ должен уходить из слоя")
	}
}

func TestRoutingFailureDoesNotBreakTurn(t *testing.T) {
	// Раскладка возвращает не-JSON: ход должен пройти с прежней памятью
	// и предупреждением в ленте — как сжатие дня 9 и факты дня 10.
	f := &fakeLLM{}
	a := NewPool(brokenRouter{f}, "fake", nil).Spawn(Config{
		Model: "m", Memory: MemoryAuto, User: "саша", Task: "барбершоп"})
	_ = a.Remember(memory.ScopeTask, "срок", "6 недель")

	var warned bool
	r, err := a.Ask(t.Context(), "привет", func(e Event) {
		if e.Kind == EventContext && strings.Contains(e.Label, "не удалась") {
			warned = true
		}
	})
	if err != nil {
		t.Fatalf("сбой раскладки не должен ломать ход: %v", err)
	}
	if r.Final.Content == "" {
		t.Fatal("ответа нет")
	}
	if !warned {
		t.Fatal("о сбое раскладки должно быть предупреждение")
	}
	if _, ok := a.Memory().Layer(memory.ScopeTask).Get("срок"); !ok {
		t.Fatal("прежняя память должна остаться")
	}
}

type brokenRouter struct{ *fakeLLM }

func (p brokenRouter) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if strings.HasPrefix(req.Messages[0].Content, "Ты раскладываешь новую информацию") {
		return &llm.Response{Model: req.Model, Content: "конечно! вот ваша память:"}, nil
	}
	return p.fakeLLM.Chat(ctx, req)
}

func (p brokenRouter) ChatStream(ctx context.Context, req llm.Request, on func(llm.Chunk) error) (*llm.Response, error) {
	return p.Chat(ctx, req)
}

func TestResetClearsOnlyChatLayer(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(f, "fake", nil).Spawn(Config{
		Model: "m", Memory: MemoryManual, User: "саша", Task: "барбершоп"})
	_ = a.Remember(memory.ScopeChat, "на чём остановились", "на оплате")
	_ = a.Remember(memory.ScopeTask, "срок", "6 недель")
	_ = a.Remember(memory.ScopeUser, "имя", "Саша")
	ask(t, a, "привет")

	a.Reset()

	m := a.Memory()
	if m.Layer(memory.ScopeChat).Len() != 0 {
		t.Fatal("Ctrl+R должен стирать краткосрочный слой вместе с историей")
	}
	if m.Layer(memory.ScopeTask).Len() != 1 || m.Layer(memory.ScopeUser).Len() != 1 {
		t.Fatal("задача не кончилась и собеседник не сменился — эти слои остаются")
	}
}

func TestMemorySurvivesNewConversation(t *testing.T) {
	// Главная проверка дня: краткосрочная память умирает вместе с разговором,
	// рабочая и долговременная — переживают его.
	f := &fakeLLM{}
	p := NewPool(f, "fake", nil)
	p.SetMemoryStore(memory.NewFileStore(t.TempDir()))
	cfg := Config{Model: "m", System: "sys", Memory: MemoryManual, User: "саша", Task: "барбершоп"}

	first := p.Spawn(cfg)
	_ = first.Remember(memory.ScopeChat, "на чём остановились", "на оплате")
	_ = first.Remember(memory.ScopeTask, "срок", "6 недель")
	_ = first.Remember(memory.ScopeUser, "стек", "Go")

	second := p.Spawn(cfg) // новый разговор о той же задаче
	ask(t, second, "продолжим")

	sys := systemOf(f.last())
	for _, want := range []string{"стек: Go", "срок: 6 недель"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("новый разговор не увидел память задачи и пользователя:\n%s", sys)
		}
	}
	if strings.Contains(sys, "на чём остановились") {
		t.Fatal("краткосрочный слой чужого разговора подтягиваться не должен")
	}
}

func TestMemoryScopesLimitWhatGoesIntoPrompt(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(f, "fake", nil).Spawn(Config{
		Model: "m", System: "sys", Memory: MemoryManual, User: "саша", Task: "барбершоп",
		MemoryScopes: []string{"task"}})
	_ = a.Remember(memory.ScopeUser, "стек", "Go")
	_ = a.Remember(memory.ScopeTask, "срок", "6 недель")
	ask(t, a, "привет")

	sys := systemOf(f.last())
	if !strings.Contains(sys, "срок: 6 недель") {
		t.Fatalf("разрешённый слой не попал в промпт:\n%s", sys)
	}
	if strings.Contains(sys, "стек: Go") {
		t.Fatalf("запрещённый слой всё равно ушёл в промпт:\n%s", sys)
	}
	// В самой памяти запись при этом осталась — фильтр про промпт, не про память.
	if a.Memory().Layer(memory.ScopeUser).Len() != 1 {
		t.Fatal("фильтр слоёв не должен трогать саму память")
	}
}

func TestChangingTaskSwapsWorkingMemory(t *testing.T) {
	f := &fakeLLM{}
	p := NewPool(f, "fake", nil)
	p.SetMemoryStore(memory.NewFileStore(t.TempDir()))
	cfg := Config{Model: "m", Memory: MemoryManual, User: "саша", Task: "барбершоп"}
	a := p.Spawn(cfg)
	_ = a.Remember(memory.ScopeUser, "стек", "Go")
	_ = a.Remember(memory.ScopeTask, "срок", "6 недель")
	_ = a.Remember(memory.ScopeChat, "заметка", "этого разговора")

	next := cfg
	next.Task = "другая задача"
	a.SetConfig(next)

	m := a.Memory()
	if m.Layer(memory.ScopeTask).Len() != 0 {
		t.Fatal("рабочая память прежней задачи в новой задаче неверна")
	}
	if m.Layer(memory.ScopeUser).Len() != 1 {
		t.Fatal("долговременный слой переживает смену задачи")
	}
	if m.Layer(memory.ScopeChat).Len() != 1 {
		t.Fatal("разговор тот же — краткосрочный слой остаётся")
	}
}

func TestMemorySurvivesRestart(t *testing.T) {
	sessions := t.TempDir()
	mem := t.TempDir()
	cfg := Config{Model: "m", System: "sys", Memory: MemoryManual, User: "саша", Task: "барбершоп"}

	f := &fakeLLM{}
	p := NewPool(f, "fake", nil)
	p.SetStore(NewFileStore(sessions))
	p.SetMemoryStore(memory.NewFileStore(mem))
	a := p.Spawn(cfg)
	ask(t, a, "привет") // без реплики разговор на диск не пишется
	_ = a.Remember(memory.ScopeChat, "заметка", "этого разговора")
	_ = a.Remember(memory.ScopeTask, "срок", "6 недель")
	_ = a.Remember(memory.ScopeUser, "стек", "Go")

	p2 := NewPool(f, "fake", nil)
	p2.SetStore(NewFileStore(sessions))
	p2.SetMemoryStore(memory.NewFileStore(mem))
	restored, err := p2.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 {
		t.Fatalf("поднялось разговоров: %d", len(restored))
	}
	m := restored[0].Memory()
	if m == nil {
		t.Fatal("после перезапуска у агента нет памяти")
	}
	for _, c := range []struct {
		scope memory.Scope
		key   string
	}{{memory.ScopeChat, "заметка"}, {memory.ScopeTask, "срок"}, {memory.ScopeUser, "стек"}} {
		if _, ok := m.Layer(c.scope).Get(c.key); !ok {
			t.Fatalf("слой %s не пережил перезапуск", c.scope)
		}
	}
}
