package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/safronov-a1exander/advent/internal/rag"
)

// База знаний (день 22).
//
// Ход с RAG устроен как в лекции: вопрос → поиск ближайших фрагментов →
// фрагменты вместе с вопросом → запрос к модели. Поиск идёт до запроса
// и по каждому вопросу; модель не решает, искать ли, — в отличие от
// инструментов MCP, это делает агент.
//
// Фрагменты уходят в сообщение пользователя этого хода, а в историю
// разговора ложится сам вопрос. Иначе каждый следующий ход тащил бы
// с собой фрагменты всех прошлых — и платил бы за них снова и снова,
// хотя к новому вопросу они чаще всего отношения не имеют. Нужны снова —
// найдутся снова.

// Knowledge — откуда агент берёт фрагменты. *rag.Retriever подходит как есть.
type Knowledge interface {
	Retrieve(ctx context.Context, query string, k int) ([]rag.Hit, error)
}

// DefaultRAGTopK — сколько фрагментов уходит в запрос по умолчанию. Три —
// по замеру дня 21: у нарезки по разделам факт попадает в top-3
// в пятнадцати вопросах из шестнадцати.
const DefaultRAGTopK = 3

// RAGModes — значения поля rag для панели и флагов.
var RAGModes = []string{"", "on"}

func (c Config) ragTopK() int {
	if c.RAGTopK != nil && *c.RAGTopK > 0 {
		return *c.RAGTopK
	}
	return DefaultRAGTopK
}

// SetKnowledge подключает базу знаний. Пользоваться ли ею, решает конфиг
// агента (Config.RAG).
func (p *Pool) SetKnowledge(k Knowledge) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.knowledge = k
	for _, a := range p.agents {
		a.mu.Lock()
		a.knowledge = k
		a.mu.Unlock()
	}
}

// retrieve — фрагменты под вопрос. Недоступная база не ломает ход: агент
// говорит об этом в ленте и отвечает без неё, как и при недоступном MCP.
func (a *Agent) retrieve(ctx context.Context, cfg Config, text string, on func(Event)) []rag.Hit {
	if cfg.RAG == "" {
		return nil
	}
	a.mu.Lock()
	kb := a.knowledge
	a.mu.Unlock()
	if kb == nil {
		on(Event{Kind: EventRetrieval, Label: "база знаний не подключена", Failed: true})
		return nil
	}
	hits, err := kb.Retrieve(ctx, text, cfg.ragTopK())
	if err != nil {
		on(Event{Kind: EventRetrieval, Label: "база знаний недоступна", Content: err.Error(), Failed: true})
		return nil
	}
	on(Event{Kind: EventRetrieval, Label: fmt.Sprintf("база знаний: %d фрагм.", len(hits)), Content: HitTrace(hits)})
	return hits
}

// HitTrace — найденное по строке на фрагмент: сходство, id, раздел.
func HitTrace(hits []rag.Hit) string {
	lines := make([]string, len(hits))
	for i, h := range hits {
		lines[i] = fmt.Sprintf("%.3f %s · %s", h.Score, h.ID, h.Section)
		if h.Section == "" {
			lines[i] = fmt.Sprintf("%.3f %s", h.Score, h.ID)
		}
	}
	return strings.Join(lines, "\n")
}

// augment — вопрос вместе с фрагментами: то, что уйдёт в модель вместо
// голого вопроса. Номер фрагмента — чтобы на него можно было сослаться.
func augment(question string, hits []rag.Hit) string {
	if len(hits) == 0 {
		return question
	}
	var b strings.Builder
	b.WriteString("Фрагменты базы знаний, найденные по вопросу (ближайшие — первыми):\n\n")
	for i, h := range hits {
		fmt.Fprintf(&b, "[%d] %s — %s\n%s\n\n", i+1, h.Source, h.Header(), strings.TrimSpace(h.Text))
	}
	b.WriteString("---\nОтветь на вопрос по этим фрагментам. Если ответа в них нет — так и скажи, не придумывай.\n\nВопрос: ")
	b.WriteString(question)
	return b.String()
}

// RAGLabel — подпись режима в панели.
func RAGLabel(mode string) string {
	if mode == "" {
		return "выкл"
	}
	return "вкл"
}

// RAGHint — подсказка режима в панели.
func RAGHint(mode string) string {
	if mode == "" {
		return "модель отвечает тем, что знает сама; про стенд она не знает ничего"
	}
	return "перед ответом агент ищет фрагменты в базе знаний и отправляет их вместе с вопросом"
}
