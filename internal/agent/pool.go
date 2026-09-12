package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/safronov-a1exander/advent/internal/llm"
)

// Pool порождает агентов и держит их в одном процессе.
//
// Агент — обычный объект с конфигом и историей, а не отдельный процесс
// или экземпляр приложения, поэтому порождение — это Spawn в цикле, а сто
// агентов с разными конфигами живут в одном процессе (юнит-тест пула
// проверяет ровно сотню). HTTP-клиент у всех общий.
type Pool struct {
	client   llm.Provider
	provider string
	journal  Journal

	mu     sync.Mutex
	seq    int
	agents []*Agent
	spent  Stats // расход всех агентов, включая уже удалённых
}

// NewPool — пул поверх одного клиента провайдера. journal может быть nil.
func NewPool(client llm.Provider, provider string, journal Journal) *Pool {
	return &Pool{client: client, provider: provider, journal: journal}
}

// Spawn создаёт агента с копией конфига. У нового агента пустая история.
func (p *Pool) Spawn(cfg Config) *Agent {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		name = "agent"
	}
	a := &Agent{
		id:       fmt.Sprintf("%s-%03d", slug(name), p.seq),
		provider: p.provider,
		client:   p.client,
		journal:  p.journal,
		cfg:      cfg.Clone(),
		created:  time.Now(),
	}
	a.onCall = p.account
	p.agents = append(p.agents, a)
	return a
}

// Get — агент по id.
func (p *Pool) Get(id string) (*Agent, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.agents {
		if a.id == id {
			return a, true
		}
	}
	return nil, false
}

// List — агенты в порядке создания.
func (p *Pool) List() []*Agent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*Agent(nil), p.agents...)
}

// Len — сколько агентов сейчас в пуле.
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.agents)
}

// Remove убирает агента из пула. Его расход остаётся в общем счётчике.
func (p *Pool) Remove(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, a := range p.agents {
		if a.id == id {
			p.agents = append(p.agents[:i], p.agents[i+1:]...)
			return
		}
	}
}

// Spent — суммарный расход всех агентов пула за время жизни.
func (p *Pool) Spent() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.spent
}

func (p *Pool) account(r *llm.Response, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.spent.Errors++
		return
	}
	p.spent.add(r)
}

// Result — ответ одного агента в групповом опросе.
type Result struct {
	Agent   *Agent
	Reply   *Reply
	Err     error
	Elapsed time.Duration
}

// AskAll задаёт один вопрос нескольким агентам, не больше parallel
// одновременно (0 — все сразу). done вызывается по мере готовности,
// из разных горутин. Результаты возвращаются в порядке agents.
func (p *Pool) AskAll(ctx context.Context, agents []*Agent, text string, parallel int, done func(Result)) []Result {
	if parallel <= 0 || parallel > len(agents) {
		parallel = len(agents)
	}
	out := make([]Result, len(agents))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, a := range agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				out[i] = Result{Agent: a, Err: ctx.Err()}
				if done != nil {
					done(out[i])
				}
				return
			}
			defer func() { <-sem }()
			start := time.Now()
			reply, err := a.Ask(ctx, text, nil)
			out[i] = Result{Agent: a, Reply: reply, Err: err, Elapsed: time.Since(start)}
			if done != nil {
				done(out[i])
			}
		}()
	}
	wg.Wait()
	return out
}

// slug — имя агента, пригодное для id: латиница и кириллица остаются,
// всё прочее превращается в дефис.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 'а' && r <= 'я' || r == 'ё'
		if ok {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}
