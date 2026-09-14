package agent

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/safronov-a1exander/advent/internal/llm"
)

// Ветки разговора — Branching (день 10).
//
// В обычном чате альтернативы смешиваются: обсудил вариант А, потом вариант Б,
// и к концу модель уже не знает, про какой план её спрашивают, — в истории
// оба. Ветки решают это структурой, а не промптом: снимаешь чекпойнт в точке,
// где разговор разветвляется, и от него продолжаешь в отдельных ветках.
// У каждой ветки своя история после чекпойнта и свои производные — учёт
// ходов, сводка, факты, — так что стратегия контекста работает внутри ветки
// как обычно.
//
// Устройство простое: активная ветка живёт в тех же полях агента, что и
// раньше (history, turns, summary, facts), а неактивные «паркуются» рядом.
// Поэтому ответ, стратегии и учёт токенов о ветках вообще не знают.

// MainBranch — ветка, с которой начинается любой разговор.
const MainBranch = "основная"

// ErrUnknownBranch и ErrUnknownCheckpoint — ошибки переключения.
var (
	ErrUnknownBranch     = errors.New("такой ветки нет")
	ErrUnknownCheckpoint = errors.New("такого чекпойнта нет")
)

// thread — состояние одной ветки разговора.
type thread struct {
	History []llm.Message `json:"history"`
	Turns   []Turn        `json:"turns,omitempty"`
	Summary *summaryState `json:"summary,omitempty"`
	Facts   []Fact        `json:"facts,omitempty"`
}

func (t thread) clone() thread {
	out := thread{
		History: append([]llm.Message(nil), t.History...),
		Turns:   append([]Turn(nil), t.Turns...),
		Facts:   append([]Fact(nil), t.Facts...),
	}
	if t.Summary != nil {
		s := *t.Summary
		out.Summary = &s
	}
	return out
}

// checkpoint — снятое состояние разговора, от которого можно ветвиться.
type checkpoint struct {
	Name   string    `json:"name"`
	Branch string    `json:"branch"` // в какой ветке снят
	At     time.Time `json:"at"`
	State  thread    `json:"state"`
}

// BranchInfo — ветка для списков.
type BranchInfo struct {
	Name     string
	Active   bool
	Messages int
	// From — чекпойнт, от которого ветка отошла; пусто у основной.
	From string
	// Last — последняя реплика пользователя в ветке: по ней ветку узнают.
	Last string
}

// CheckpointInfo — чекпойнт для списков.
type CheckpointInfo struct {
	Name     string
	Branch   string
	Messages int
	At       time.Time
}

// current — активная ветка как thread; вызывать под a.mu.
func (a *Agent) current() thread {
	t := thread{History: a.history, Turns: a.turns, Facts: a.facts}
	if a.summary.Text != "" {
		s := a.summary
		t.Summary = &s
	}
	return t.clone()
}

// load делает thread активной веткой; вызывать под a.mu.
func (a *Agent) load(t thread) {
	t = t.clone()
	a.history, a.turns, a.facts = t.History, t.Turns, t.Facts
	a.summary = summaryState{}
	if t.Summary != nil {
		a.summary = *t.Summary
	}
	// Ответ, начатый в другой ветке, не должен дописаться в эту.
	a.gen++
}

func (a *Agent) activeBranch() string {
	if a.branch == "" {
		return MainBranch
	}
	return a.branch
}

// ActiveBranch — имя активной ветки.
func (a *Agent) ActiveBranch() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.activeBranch()
}

// Checkpoint снимает чекпойнт в текущей точке активной ветки. Пустое имя —
// «точка-N». Ответ в полёте ветвить нельзя: непонятно, куда он допишется.
func (a *Agent) Checkpoint(name string) (string, error) {
	if a.Busy() {
		return "", ErrBusy
	}
	a.mu.Lock()
	name = strings.TrimSpace(name)
	if name == "" {
		name = fmt.Sprintf("точка-%d", len(a.checkpoints)+1)
	}
	for _, c := range a.checkpoints {
		if c.Name == name {
			a.mu.Unlock()
			return "", fmt.Errorf("чекпойнт %q уже есть", name)
		}
	}
	a.checkpoints = append(a.checkpoints, checkpoint{
		Name: name, Branch: a.activeBranch(), At: time.Now(), State: a.current(),
	})
	a.touch()
	a.mu.Unlock()
	a.changed()
	return name, nil
}

// Branch создаёт ветку от чекпойнта и переключается в неё. Текущая ветка
// откладывается как есть. Пустое имя — «ветка-N».
func (a *Agent) Branch(name, from string) (string, error) {
	if a.Busy() {
		return "", ErrBusy
	}
	a.mu.Lock()
	var cp *checkpoint
	for i := range a.checkpoints {
		if a.checkpoints[i].Name == from {
			cp = &a.checkpoints[i]
		}
	}
	if cp == nil {
		a.mu.Unlock()
		return "", fmt.Errorf("%w: %q", ErrUnknownCheckpoint, from)
	}
	a.ensureOrder()
	name = strings.TrimSpace(name)
	if name == "" {
		// основная уже в списке, поэтому первая новая — «ветка-1»
		name = fmt.Sprintf("ветка-%d", len(a.branchOrder))
	}
	if a.hasBranch(name) {
		a.mu.Unlock()
		return "", fmt.Errorf("ветка %q уже есть", name)
	}
	a.park()
	a.load(cp.State)
	a.branch = name
	a.branchOrder = append(a.branchOrder, name)
	if a.branchFrom == nil {
		a.branchFrom = map[string]string{}
	}
	a.branchFrom[name] = from
	a.touch()
	a.mu.Unlock()
	a.changed()
	return name, nil
}

// SwitchBranch переключает разговор на другую ветку.
func (a *Agent) SwitchBranch(name string) error {
	if a.Busy() {
		return ErrBusy
	}
	a.mu.Lock()
	if name == a.activeBranch() {
		a.mu.Unlock()
		return nil
	}
	t, ok := a.parked[name]
	if !ok {
		a.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrUnknownBranch, name)
	}
	a.ensureOrder()
	a.park()
	delete(a.parked, name)
	a.load(t)
	a.branch = name
	a.touch()
	a.mu.Unlock()
	a.changed()
	return nil
}

// Branches — все ветки в порядке появления; активная отмечена.
func (a *Agent) Branches() []BranchInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensureOrder()
	out := make([]BranchInfo, 0, len(a.branchOrder))
	for _, name := range a.branchOrder {
		info := BranchInfo{Name: name, From: a.branchFrom[name]}
		hist := a.history
		if name == a.activeBranch() {
			info.Active = true
		} else {
			hist = a.parked[name].History
		}
		info.Messages = len(hist)
		for i := len(hist) - 1; i >= 0; i-- {
			if hist[i].Role == llm.RoleUser {
				info.Last = strings.Join(strings.Fields(hist[i].Content), " ")
				break
			}
		}
		out = append(out, info)
	}
	return out
}

// Checkpoints — чекпойнты в порядке снятия.
func (a *Agent) Checkpoints() []CheckpointInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]CheckpointInfo, 0, len(a.checkpoints))
	for _, c := range a.checkpoints {
		out = append(out, CheckpointInfo{Name: c.Name, Branch: c.Branch, Messages: len(c.State.History), At: c.At})
	}
	return out
}

// park откладывает активную ветку; вызывать под a.mu.
func (a *Agent) park() {
	if a.parked == nil {
		a.parked = map[string]thread{}
	}
	a.parked[a.activeBranch()] = a.current()
}

// ensureOrder — основная ветка всегда первая в списке; вызывать под a.mu.
func (a *Agent) ensureOrder() {
	if len(a.branchOrder) == 0 {
		a.branchOrder = []string{a.activeBranch()}
	}
}

func (a *Agent) hasBranch(name string) bool {
	if name == a.activeBranch() {
		return true
	}
	_, ok := a.parked[name]
	return ok
}

// resetBranches — новый разговор: все ветки и чекпойнты уходят; под a.mu.
func (a *Agent) resetBranches() {
	a.branch = ""
	a.parked = nil
	a.checkpoints = nil
	a.branchOrder = nil
	a.branchFrom = nil
}
