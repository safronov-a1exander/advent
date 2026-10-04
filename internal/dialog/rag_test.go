package dialog

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/rag"
)

// fakeKB — база из одного фрагмента про лиру; на остальное — пусто.
type fakeKB struct{ err error }

func (f fakeKB) Retrieve(_ context.Context, q string, _ rag.Options) (*rag.Result, error) {
	if f.err != nil {
		return nil, f.err
	}
	res := &rag.Result{Query: q}
	if !strings.Contains(strings.ToLower(q), "лир") {
		return res, nil
	}
	res.Kept = []rag.Hit{{Score: 0.71, Chunk: rag.Chunk{
		ID: "docs/days/day17.md#3", Source: "docs/days/day17.md", Title: "День 17", Section: "Номинал",
		Text: "ЦБ публикует курс лиры за 10 единиц.",
	}}}
	res.Candidates = res.Kept
	return res, nil
}

func TestRAGVariantAnswersFromFragments(t *testing.T) {
	s := &Scenario{
		Defaults: agent.Config{Tier: "weak", System: "ассистент"},
		Variants: []Variant{
			{Config: agent.Config{Name: "без RAG"}},
			{Config: agent.Config{Name: "с RAG", RAG: "on"}},
		},
		Dialog: []Line{
			{Say: "почему курс лиры нельзя брать как есть?", Expect: []string{"10 единиц"}, Sources: []string{"docs/days/day17.md"}},
		},
	}
	pool := agent.NewPool(&echoLLM{}, "echo", nil)
	pool.SetKnowledge(fakeKB{})
	res, err := Run(context.Background(), pool, []llm.ModelInfo{{ID: "m", Tier: "weak"}}, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, withRAG := res[0].Steps[0], res[1].Steps[0]
	if plain.Passed || len(plain.Sources) != 0 {
		t.Fatalf("без RAG фрагментов быть не должно: %+v", plain)
	}
	// у варианта без базы проверяется только ответ — источников он не ищет
	for _, m := range plain.Missing {
		if strings.Contains(m, "источник") {
			t.Fatalf("варианту без RAG засчитали отсутствие источника: %v", plain.Missing)
		}
	}
	if !withRAG.Passed || len(withRAG.Sources) != 1 {
		t.Fatalf("с RAG ответ должен прийти из фрагмента: %+v", withRAG)
	}
}

func TestRAGSourcesCheckFailsOnWrongFile(t *testing.T) {
	miss := checkSources([]rag.Hit{{Chunk: rag.Chunk{Source: "a.md"}}}, []string{"a.md", "b.md"})
	if len(miss) != 1 || !strings.Contains(miss[0], "b.md") {
		t.Fatalf("ждали промах по b.md: %v", miss)
	}
}

func TestRAGHistoryKeepsBareQuestion(t *testing.T) {
	pool := agent.NewPool(&echoLLM{}, "echo", nil)
	pool.SetKnowledge(fakeKB{})
	a := pool.SpawnTemp(agent.Config{Model: "m", RAG: "on"})
	reply, err := a.Ask(context.Background(), "что с лирой?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply.Final.Content, "10 единиц") {
		t.Fatalf("фрагмент не дошёл до модели: %q", reply.Final.Content)
	}
	h := a.History()
	if len(h) != 2 || h[0].Content != "что с лирой?" {
		t.Fatalf("в историю должен лечь голый вопрос, а не фрагменты: %q", h[0].Content)
	}
}

func TestRAGUnavailableDoesNotBreakTurn(t *testing.T) {
	pool := agent.NewPool(&echoLLM{}, "echo", nil)
	pool.SetKnowledge(fakeKB{err: errors.New("сервер эмбеддингов не отвечает")})
	a := pool.SpawnTemp(agent.Config{Model: "m", RAG: "on"})
	var failed bool
	_, err := a.Ask(context.Background(), "что с лирой?", func(e agent.Event) {
		if e.Kind == agent.EventRetrieval && e.Failed {
			failed = true
		}
	})
	if err != nil || !failed {
		t.Fatalf("ход должен пройти без базы, а сбой — попасть в ленту: err=%v, failed=%v", err, failed)
	}
}

func TestCheckReadsDecimalComma(t *testing.T) {
	if ok, miss := check("стадии стоят 0,0046 против 0,0024", Check{Expect: []string{"0.0046", "0.0024"}}); !ok {
		t.Fatalf("десятичная запятая: %v", miss)
	}
}
