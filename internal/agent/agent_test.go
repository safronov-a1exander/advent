package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/store"
)

// fakeLLM отвечает, как настоящая модель без памяти: знает только то,
// что пришло в запросе. На «как меня зовут» ищет имя в присланной истории.
type fakeLLM struct {
	mu       sync.Mutex
	requests []llm.Request
	fail     error
	delay    time.Duration
	inFlight atomic.Int32
	peak     atomic.Int32
}

func (f *fakeLLM) Name() string                                 { return "fake" }
func (f *fakeLLM) ListModels(context.Context) ([]string, error) { return nil, nil }

func (f *fakeLLM) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	f.requests = append(f.requests, req)
	fail := f.fail
	f.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	return &llm.Response{
		Model:        req.Model,
		Content:      answer(req.Messages),
		FinishReason: "stop",
		Usage:        llm.Usage{PromptTokens: 10 * len(req.Messages), CompletionTokens: 5},
		CostUSD:      0.001,
	}, nil
}

func (f *fakeLLM) ChatStream(ctx context.Context, req llm.Request, on func(llm.Chunk) error) (*llm.Response, error) {
	resp, err := f.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	for _, w := range strings.SplitAfter(resp.Content, " ") {
		_ = on(llm.Chunk{Content: w})
	}
	_ = on(llm.Chunk{Done: true})
	return resp, nil
}

func (f *fakeLLM) last() llm.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func answer(msgs []llm.Message) string {
	q := msgs[len(msgs)-1].Content
	if !strings.Contains(q, "как меня зовут") {
		return "ответ на: " + q
	}
	for _, m := range msgs[:len(msgs)-1] {
		if m.Role == llm.RoleUser {
			if _, name, ok := strings.Cut(m.Content, "меня зовут "); ok {
				return "тебя зовут " + name
			}
		}
	}
	return "не знаю"
}

type memJournal struct {
	mu   sync.Mutex
	recs []store.Record
}

func (j *memJournal) Append(r store.Record) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.recs = append(j.recs, r)
	return nil
}

func ask(t *testing.T, a *Agent, q string) string {
	t.Helper()
	r, err := a.Ask(context.Background(), q, nil)
	if err != nil {
		t.Fatalf("Ask(%q): %v", q, err)
	}
	return r.Final.Content
}

func TestAgentKeepsDialogueStack(t *testing.T) {
	f := &fakeLLM{}
	p := NewPool(f, "fake", nil)
	a := p.Spawn(Config{Name: "бюджет", Model: "m", System: "ты ассистент"})

	ask(t, a, "привет, меня зовут Саша")
	if got := ask(t, a, "как меня зовут?"); got != "тебя зовут Саша" {
		t.Fatalf("агент не передал историю: %q", got)
	}

	req := f.last()
	roles := make([]string, len(req.Messages))
	for i, m := range req.Messages {
		roles[i] = string(m.Role)
	}
	if want := "system user assistant user"; strings.Join(roles, " ") != want {
		t.Fatalf("стек сообщений %v, ожидали %s", roles, want)
	}
	if n := len(a.History()); n != 4 {
		t.Fatalf("в истории %d сообщений, ожидали 4", n)
	}
	if got := a.Title(); got != "привет, меня зовут Саша" {
		t.Fatalf("название разговора: %q", got)
	}
	if got := p.Spawn(Config{Model: "m"}).Title(); got != "" {
		t.Fatalf("у нового агента название должно быть пустым: %q", got)
	}
}

func TestAgentsAreIsolated(t *testing.T) {
	p := NewPool(&fakeLLM{}, "fake", nil)
	left := p.Spawn(Config{Name: "левый", Model: "m"})
	right := p.Spawn(Config{Name: "правый", Model: "m"})

	ask(t, left, "меня зовут Влад")
	if got := ask(t, right, "как меня зовут?"); got != "не знаю" {
		t.Fatalf("история просочилась между агентами: %q", got)
	}
	if got := ask(t, left, "как меня зовут?"); got != "тебя зовут Влад" {
		t.Fatalf("левый забыл: %q", got)
	}
}

func TestFailedTurnDoesNotEnterHistory(t *testing.T) {
	f := &fakeLLM{fail: errors.New("HTTP 400")}
	p := NewPool(f, "fake", nil)
	a := p.Spawn(Config{Model: "m"})

	if _, err := a.Ask(context.Background(), "вопрос", nil); err == nil {
		t.Fatal("ожидали ошибку")
	}
	if len(a.History()) != 0 {
		t.Fatal("неотвеченный вопрос попал в историю")
	}
	if s := a.Stats(); s.Errors != 1 || s.Calls != 0 {
		t.Fatalf("счётчики после ошибки: %+v", s)
	}
}

func TestBusyAgentRejectsSecondQuestion(t *testing.T) {
	f := &fakeLLM{delay: 50 * time.Millisecond}
	a := NewPool(f, "fake", nil).Spawn(Config{Model: "m"})

	errs := make(chan error, 1)
	go func() {
		_, err := a.Ask(context.Background(), "долгий вопрос", nil)
		errs <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if _, err := a.Ask(context.Background(), "второй", nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("ожидали ErrBusy, получили %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestResetDuringAnswerWins(t *testing.T) {
	f := &fakeLLM{delay: 50 * time.Millisecond}
	a := NewPool(f, "fake", nil).Spawn(Config{Model: "m"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = a.Ask(context.Background(), "вопрос", nil)
	}()
	time.Sleep(10 * time.Millisecond)
	a.Reset()
	<-done
	if n := len(a.History()); n != 0 {
		t.Fatalf("ответ, начатый до сброса, вернул %d сообщений в историю", n)
	}
}

func TestStrategyChainAndEvents(t *testing.T) {
	f := &fakeLLM{}
	j := &memJournal{}
	a := NewPool(f, "fake", j).Spawn(Config{Model: "m", Strategy: StrategyExperts, Stream: true})

	var kinds []EventKind
	r, err := a.Ask(context.Background(), "задача", func(e Event) { kinds = append(kinds, e.Kind) })
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Steps) != 4 {
		t.Fatalf("эксперты — четыре вызова, получили %d", len(r.Steps))
	}
	steps := 0
	for _, k := range kinds {
		if k == EventStep {
			steps++
		}
	}
	if steps != 3 || kinds[3] != EventFinalStart {
		t.Fatalf("события: %v", kinds)
	}
	// в историю попадает только сам вопрос и финальный ответ
	if n := len(a.History()); n != 2 {
		t.Fatalf("история после цепочки: %d сообщений", n)
	}
	if len(j.recs) != 4 || j.recs[0].Agent != a.ID() || j.recs[0].Variant != "аналитик" {
		t.Fatalf("журнал: %d записей, первая %+v", len(j.recs), j.recs[0])
	}
	if u, cost := r.Usage(); u.CompletionTokens != 20 || cost < 0.0039 {
		t.Fatalf("суммарный расход: %+v $%f", u, cost)
	}
}

func TestConfigIsCopiedNotShared(t *testing.T) {
	p := NewPool(&fakeLLM{}, "fake", nil)
	base := Config{Model: "m", Temperature: llm.F(0.2), Stop: []string{"###"}}
	a := p.Spawn(base)
	b := p.Spawn(base)

	*base.Temperature = 1.9
	base.Stop[0] = "!!!"
	c := a.Config()
	*c.Temperature = 1.5
	b.SetConfig(c)

	if t0 := *a.Config().Temperature; t0 != 0.2 {
		t.Fatalf("температура агента a изменилась снаружи: %g", t0)
	}
	if a.Config().Stop[0] != "###" {
		t.Fatal("stop агента a изменился снаружи")
	}
	if *b.Config().Temperature != 1.5 {
		t.Fatal("SetConfig не применился")
	}
}

func TestPoolSpawnsHundredDifferentAgents(t *testing.T) {
	f := &fakeLLM{delay: 20 * time.Millisecond}
	p := NewPool(f, "fake", nil)

	var agents []*Agent
	for i := 0; i < 100; i++ {
		agents = append(agents, p.Spawn(Config{
			Name:        fmt.Sprintf("t%d", i%5),
			Model:       fmt.Sprintf("model-%d", i%3),
			Temperature: llm.F(float64(i%5) * 0.3),
		}))
	}
	if p.Len() != 100 {
		t.Fatalf("в пуле %d агентов", p.Len())
	}

	var finished atomic.Int32
	start := time.Now()
	res := p.AskAll(context.Background(), agents, "сколько осталось?", 25, func(Result) { finished.Add(1) })
	elapsed := time.Since(start)

	if finished.Load() != 100 {
		t.Fatalf("колбэк вызван %d раз", finished.Load())
	}
	ids := map[string]bool{}
	for i, r := range res {
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		if r.Agent != agents[i] {
			t.Fatal("порядок результатов не совпадает с порядком агентов")
		}
		if r.Reply.Final.Model != agents[i].Config().Model {
			t.Fatalf("агент %s ответил моделью %s", r.Agent.ID(), r.Reply.Final.Model)
		}
		if len(r.Agent.History()) != 2 {
			t.Fatalf("у агента %s чужая история", r.Agent.ID())
		}
		ids[r.Agent.ID()] = true
	}
	if len(ids) != 100 {
		t.Fatalf("уникальных id %d", len(ids))
	}
	if peak := f.peak.Load(); peak > 25 {
		t.Fatalf("параллельность %d превысила лимит 25", peak)
	}
	// последовательно было бы 2 с; с лимитом 25 — порядка 80 мс
	if elapsed > time.Second {
		t.Fatalf("пул не распараллелил вызовы: %s", elapsed)
	}
	if s := p.Spent(); s.Calls != 100 {
		t.Fatalf("общий счётчик пула: %+v", s)
	}
	p.Remove(agents[0].ID())
	if p.Len() != 99 || p.Spent().Calls != 100 {
		t.Fatal("удаление агента должно сохранять общий расход")
	}
}

func TestFleetExpandsGroups(t *testing.T) {
	f := &Fleet{
		Defaults: Config{System: "общий", Tier: "weak", Temperature: llm.F(0.7)},
		Agents: []FleetGroup{
			{Config: Config{Name: "точный", Temperature: llm.F(0)}, Replicas: 3},
			{Config: Config{Name: "сильный", Tier: "strong"}, Replicas: 2},
			{Config: Config{Model: "explicit"}},
		},
	}
	catalog := []llm.ModelInfo{{ID: "flash", Tier: "weak"}, {ID: "pro", Tier: "strong"}}
	cfgs, err := f.Configs(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 6 {
		t.Fatalf("развернули %d конфигов", len(cfgs))
	}
	if cfgs[0].Model != "flash" || *cfgs[0].Temperature != 0 || cfgs[0].System != "общий" {
		t.Fatalf("точный: %+v", cfgs[0])
	}
	if cfgs[3].Model != "pro" || *cfgs[3].Temperature != 0.7 {
		t.Fatalf("сильный: %+v", cfgs[3])
	}
	if cfgs[5].Model != "explicit" || cfgs[5].Name != "group3" {
		t.Fatalf("явная модель: %+v", cfgs[5])
	}

	*cfgs[0].Temperature = 2
	if *cfgs[1].Temperature != 0 {
		t.Fatal("реплики одной группы делят указатель")
	}

	bad := &Fleet{Agents: []FleetGroup{{Config: Config{Tier: "medium"}}}}
	if _, err := bad.Configs(catalog); err == nil {
		t.Fatal("ожидали ошибку: класса medium в каталоге нет")
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Бюджет":          "бюджет",
		"точный / flash!": "точный-flash",
		"  agent 7  ":     "agent-7",
		"":                "",
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, ожидали %q", in, got, want)
		}
	}
}
