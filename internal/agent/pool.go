package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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

	// store — куда сохранять разговоры; nil — пул живёт только в памяти.
	store   Store
	saveErr error // последняя ошибка сохранения, забирается SaveErr
}

// SetStore включает сохранение: каждый постоянный агент пишется в store
// при создании и после каждого изменения разговора или конфига.
func (p *Pool) SetStore(s Store) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.store = s
}

// Store — хранилище пула или nil.
func (p *Pool) Store() Store {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.store
}

// SaveErr возвращает и сбрасывает последнюю ошибку сохранения. Интерфейс
// показывает её пользователю: молча потерянный разговор хуже любой ошибки.
func (p *Pool) SaveErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.saveErr
	p.saveErr = nil
	return err
}

func (p *Pool) persist(a *Agent) {
	p.mu.Lock()
	st := p.store
	p.mu.Unlock()
	if st == nil || a.temp {
		return
	}
	snap := a.snapshot()
	// Пустой разговор, которого ещё нет на диске, не сохраняем: иначе каждый
	// запуск с новым разговором оставлял бы в списке пустышку. Файл
	// появляется с первой репликой, а однажды сохранённый пишется всегда —
	// в том числе после сброса, когда история снова пуста.
	if len(snap.History) == 0 && !a.saved.Load() {
		return
	}
	if err := st.Save(snap); err != nil {
		p.mu.Lock()
		p.saveErr = fmt.Errorf("не сохранил разговор %s: %w", a.id, err)
		p.mu.Unlock()
		return
	}
	a.saved.Store(true)
}

// NewPool — пул поверх одного клиента провайдера. journal может быть nil.
func NewPool(client llm.Provider, provider string, journal Journal) *Pool {
	return &Pool{client: client, provider: provider, journal: journal}
}

// Spawn создаёт агента с копией конфига. У нового агента пустая история.
// Если у пула есть хранилище, агент сохранится с первой репликой.
func (p *Pool) Spawn(cfg Config) *Agent {
	return p.spawn(cfg, false)
}

// SpawnTemp — временный агент: живёт в пуле, но на диск не пишется.
// Для служебных прогонов вроде сравнения моделей, после которых агентов
// убирают, — иначе каждый такой прогон оставлял бы в списке разговоров мусор.
func (p *Pool) SpawnTemp(cfg Config) *Agent {
	return p.spawn(cfg, true)
}

func (p *Pool) spawn(cfg Config, temp bool) *Agent {
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
		temp:     temp,
	}
	a.created = time.Now()
	a.updated = a.created
	a.rev = 1
	p.wire(a)
	return a
}

// wire подключает агента к пулу; вызывать под p.mu.
func (p *Pool) wire(a *Agent) {
	a.onCall = p.account
	a.onChange = p.persist
	p.agents = append(p.agents, a)
}

// idSeq — числовой хвост id: «бюджет-012» → 12.
var idSeq = regexp.MustCompile(`-(\d+)$`)

// Restore поднимает сохранённые разговоры. Агенты встают в пул с прежними
// id, конфигом, историей и счётчиками, а нумерация новых продолжается
// после самого старшего — id не повторятся и не перезапишут чужой файл.
func (p *Pool) Restore() ([]*Agent, error) {
	p.mu.Lock()
	st := p.store
	p.mu.Unlock()
	if st == nil {
		return nil, errors.New("у пула нет хранилища")
	}
	snaps, loadErr := st.LoadAll()

	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*Agent
	for _, snap := range snaps {
		// Разговор принадлежит провайдеру, на котором начат: модели у разных
		// провайдеров называются по-разному, и чужой разговор здесь не
		// продолжить. Он остаётся на диске и поднимется под своим провайдером.
		if snap.Provider != "" && p.provider != "" && snap.Provider != p.provider {
			continue
		}
		dup := false
		for _, a := range p.agents {
			if a.id == snap.ID {
				dup = true
			}
		}
		if dup {
			continue
		}
		a := &Agent{
			id:       snap.ID,
			provider: p.provider,
			client:   p.client,
			journal:  p.journal,
			cfg:      snap.Config.Clone(),
			history:  append([]llm.Message(nil), snap.History...),
			stats:    snap.Stats,
			created:  snap.Created,
			updated:  snap.Updated,
			rev:      snap.Rev,
		}
		a.saved.Store(true)
		p.wire(a)
		out = append(out, a)
		if m := idSeq.FindStringSubmatch(snap.ID); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > p.seq {
				p.seq = n
			}
		}
	}
	return out, loadErr
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
// Сохранённый разговор переносится в архив хранилища, а не удаляется.
func (p *Pool) Remove(id string) error {
	p.mu.Lock()
	var gone *Agent
	for i, a := range p.agents {
		if a.id == id {
			gone = a
			p.agents = append(p.agents[:i], p.agents[i+1:]...)
			break
		}
	}
	st := p.store
	p.mu.Unlock()
	if gone == nil || gone.temp || st == nil {
		return nil
	}
	return st.Archive(id)
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
