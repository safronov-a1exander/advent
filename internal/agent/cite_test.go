package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/rag"
)

var citeHits = []rag.Hit{
	{Score: 0.64, Chunk: rag.Chunk{ID: "docs/days/day09.md#5", Source: "docs/days/day09.md", Title: "День 9", Section: "Проверено › Что видно",
		Text: "- **Сводка портит факты.** Во втором прогоне четвёртая сводка сама пересчитала\n  остаток и записала **21335 вместо 21135** — при инструкции «не считай»."}},
	{Score: 0.60, Chunk: rag.Chunk{ID: "docs/days/day08.md#4", Source: "docs/days/day08.md", Title: "День 8", Section: "Проверено",
		Text: "Расход разговора — 8694 токена на пять ходов."}},
}

func TestParseCitationVerbatimIgnoresMarkupAndLineBreaks(t *testing.T) {
	c := ParseCitation(`{"known": true, "answer": "Сводка записала 21 135 как 21335.",
		"sources": ["docs/days/day09.md#5"],
		"quotes": [{"id": "docs/days/day09.md#5", "text": "четвёртая сводка сама пересчитала остаток и записала 21335 вместо 21135"}]}`,
		"какую ошибку внесла сводка?", citeHits)
	if !c.OK() {
		t.Fatalf("дословная цитата через перенос строки и **жирный** должна пройти: %v", c.Problems())
	}
	if !strings.Contains(c.Render(citeHits), "✓ Проверка") {
		t.Fatalf("в ответе нет итога проверки:\n%s", c.Render(citeHits))
	}
}

func TestParseCitationCatchesParaphraseForeignAndInventedNumbers(t *testing.T) {
	c := ParseCitation(`{"known": true, "answer": "Сводка ошиблась на 200 рублей, а потом ещё на 999.",
		"sources": ["docs/days/day09.md#5", "docs/days/day42.md#1"],
		"quotes": [{"id": "docs/days/day09.md#5", "text": "сводка неверно посчитала остаток"}]}`,
		"какую ошибку внесла сводка?", citeHits)
	if c.OK() {
		t.Fatal("пересказ вместо цитаты, чужой источник и выдуманное число — не OK")
	}
	p := strings.Join(c.Problems(), " | ")
	for _, want := range []string{"цитата не из фрагмента", "не из найденных: docs/days/day42.md#1", "числа не из источников: 200, 999"} {
		if !strings.Contains(p, want) {
			t.Fatalf("в проблемах нет %q: %s", want, p)
		}
	}
	if !strings.Contains(c.Render(citeHits), "✗ нет во фрагменте") {
		t.Fatal("непроверенная цитата должна быть помечена в ответе")
	}
}

func TestParseCitationNumbersFromCitedFragmentAndQuestionAreFine(t *testing.T) {
	c := ParseCitation(`{"known": true, "answer": "За пять ходов ушло 8694 токена — это замер дня 8 из 60 000 возможных.",
		"sources": ["docs/days/day08.md#4"],
		"quotes": [{"id": "docs/days/day08.md#4", "text": "Расход разговора — 8694 токена на пять ходов"}]}`,
		"сколько токенов из 60 000?", citeHits)
	if len(c.Ungrounded) > 0 {
		t.Fatalf("числа из фрагмента и вопроса — не выдумка: %v", c.Ungrounded)
	}
}

func TestParseCitationKnownFalseIsHonest(t *testing.T) {
	c := ParseCitation(`{"known": false, "answer": "Не знаю. Уточните, о каком дне речь?", "sources": [], "quotes": []}`, "?", citeHits)
	if !c.OK() || c.Known {
		t.Fatal("честное «не знаю» без источников — это OK")
	}
}

func TestParseCitationMalformed(t *testing.T) {
	c := ParseCitation("просто текст", "?", citeHits)
	if !c.Malformed || c.OK() || c.Render(citeHits) != "просто текст" {
		t.Fatalf("не JSON — показать как есть и не засчитать: %+v", c)
	}
}

func TestNumbersInNormalizes(t *testing.T) {
	got := strings.Join(numbersIn("21 135 и 0,0046, день 9, 37–52%"), " ")
	if got != "21135 0.0046 37 52" {
		t.Fatalf("числа: %q", got)
	}
}

// countingLLM считает вызовы.
type countingLLM struct{ calls atomic.Int32 }

func (c *countingLLM) Name() string                                 { return "count" }
func (c *countingLLM) ListModels(context.Context) ([]string, error) { return nil, nil }
func (c *countingLLM) ChatStream(ctx context.Context, r llm.Request, _ func(llm.Chunk) error) (*llm.Response, error) {
	return c.Chat(ctx, r)
}
func (c *countingLLM) Chat(_ context.Context, r llm.Request) (*llm.Response, error) {
	c.calls.Add(1)
	return &llm.Response{Content: `{"known": false, "answer": "Не знаю", "sources": [], "quotes": []}`}, nil
}

// emptyKB — поиск был, но порог не прошёл никто.
type emptyKB struct{}

func (emptyKB) Retrieve(_ context.Context, q string, _ rag.Options) (*rag.Result, error) {
	return &rag.Result{Query: q, Candidates: []rag.Hit{
		{Score: 0.53, Rerank: 0.06, Chunk: rag.Chunk{ID: "day18#2", Title: "День 18", Section: "Откуда курс"}},
		{Score: 0.41, Rerank: 0.00, Chunk: rag.Chunk{ID: "server.go#3", Title: "пакет budget", Section: "func New"}},
	}}, nil
}

func TestCiteWithNothingAboveThresholdSaysIDontKnowWithoutModel(t *testing.T) {
	llmc := &countingLLM{}
	p := NewPool(llmc, "count", nil)
	p.SetKnowledge(emptyKB{})
	a := p.SpawnTemp(Config{Model: "m", RAG: "on", RAGCite: true, RAGRerank: true, RAGMinRerank: llm.F(0.1)})
	reply, err := a.Ask(context.Background(), "какой курс биткоина был в 2017?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := llmc.calls.Load(); n != 0 {
		t.Fatalf("при пустом поиске модель спрашивать нельзя, а вызовов %d", n)
	}
	got := reply.Final.Content
	if !strings.HasPrefix(got, "Не знаю") || !strings.Contains(got, "Уточните") {
		t.Fatalf("ответ должен быть «не знаю» с просьбой уточнить: %q", got)
	}
	// 0.06 — больше половины порога: подсказка уместна; 0.00 — нет
	if !strings.Contains(got, "Откуда курс") || strings.Contains(got, "func New") {
		t.Fatalf("подсказывать только близкие разделы: %q", got)
	}
	if reply.Citation == nil || !reply.Citation.Refused {
		t.Fatal("ответ должен быть помечен как отказ кода")
	}
	if h := a.History(); len(h) != 2 || h[1].Content != got {
		t.Fatal("«не знаю» — обычный ход разговора и ложится в историю")
	}
}

// routeLLM: на rewrite отвечает NONE, на остальное — «цель: X».
type routeLLM struct{ rewrites atomic.Int32 }

func (r *routeLLM) Name() string                                 { return "route" }
func (r *routeLLM) ListModels(context.Context) ([]string, error) { return nil, nil }
func (r *routeLLM) ChatStream(ctx context.Context, q llm.Request, _ func(llm.Chunk) error) (*llm.Response, error) {
	return r.Chat(ctx, q)
}
func (r *routeLLM) Chat(_ context.Context, q llm.Request) (*llm.Response, error) {
	if strings.HasPrefix(q.Messages[0].Content, "Ты переписываешь вопрос") {
		r.rewrites.Add(1)
		if !strings.Contains(q.Messages[1].Content, "Память задачи:") {
			return &llm.Response{Content: "память задачи не дошла до rewrite"}, nil
		}
		return &llm.Response{Content: "NONE."}, nil
	}
	return &llm.Response{Content: "Цель — повторить замер дня 20."}, nil
}

type countKB struct{ calls atomic.Int32 }

func (c *countKB) Retrieve(_ context.Context, q string, _ rag.Options) (*rag.Result, error) {
	c.calls.Add(1)
	return &rag.Result{Query: q}, nil
}

func TestRewriteNoneSkipsSearchAndNamesConversationAsSource(t *testing.T) {
	llmc, kb := &routeLLM{}, &countKB{}
	p := NewPool(llmc, "route", nil)
	p.SetKnowledge(kb)
	a := p.SpawnTemp(Config{Model: "m", RAG: "on", RAGRewrite: true, RAGCite: true, Memory: "manual", Task: "t"})
	if err := a.Remember("task", "цель", "повторить замер дня 20"); err != nil {
		t.Fatal(err)
	}
	reply, err := a.Ask(context.Background(), "напомни, какая у нас цель?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if kb.calls.Load() != 0 {
		t.Fatal("вопрос о разговоре не должен идти в базу")
	}
	if !strings.Contains(reply.Final.Content, "повторить замер") || !strings.HasSuffix(reply.Final.Content, ConversationSource) {
		t.Fatalf("ответ по памяти с подписью источника, а не «не знаю»: %q", reply.Final.Content)
	}
	if reply.Citation != nil {
		t.Fatal("без поиска нечего цитировать — режим цитат не включается")
	}
}
