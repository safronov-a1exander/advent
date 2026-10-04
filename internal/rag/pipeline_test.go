package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixedReranker — оценка по подстроке: текст с word получает high,
// остальные — low.
type fixedReranker struct {
	word      string
	high, low float32
}

func (f fixedReranker) Name() string { return "test" }
func (f fixedReranker) Rerank(_ context.Context, _ string, docs []string) ([]float32, error) {
	out := make([]float32, len(docs))
	for i, d := range docs {
		out[i] = f.low
		if strings.Contains(d, f.word) {
			out[i] = f.high
		}
	}
	return out, nil
}

func cands() []Hit {
	return []Hit{
		{Score: 0.66, Chunk: Chunk{ID: "a#0", Text: "сжатие истории"}},
		{Score: 0.61, Chunk: Chunk{ID: "b#0", Text: "сводка по расписанию"}},
		{Score: 0.52, Chunk: Chunk{ID: "c#0", Text: "курс лиры"}},
		{Score: 0.40, Chunk: Chunk{ID: "d#0", Text: "борщ"}},
	}
}

func TestSelectThresholdDropsLowCosine(t *testing.T) {
	res, err := Select(context.Background(), nil, "q", cands(), Options{TopK: 3, MinScore: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Kept) != 3 || res.BelowScore != 1 {
		t.Fatalf("порог 0.5 должен срезать один: %+v", res)
	}
}

func TestSelectRerankReordersAndRemembersRank(t *testing.T) {
	rr := fixedReranker{word: "расписанию", high: 0.9, low: 0.05}
	res, err := Select(context.Background(), rr, "q", cands(), Options{TopK: 3, Rerank: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kept[0].ID != "b#0" || res.Kept[0].Rank != 2 || res.Kept[0].Rerank != 0.9 {
		t.Fatalf("реранкер должен поднять второго на первое место: %+v", res.Kept[0])
	}
	if len(res.Kept) != 3 {
		t.Fatalf("без порога реранкера в запрос идут top-K: %d", len(res.Kept))
	}
}

func TestSelectRerankThresholdCanLeaveNothing(t *testing.T) {
	rr := fixedReranker{word: "нет такого", high: 0.9, low: 0.03}
	res, err := Select(context.Background(), rr, "q", cands(), Options{TopK: 3, Rerank: true, MinRerank: 0.1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Kept) != 0 || res.BelowRerank != 4 {
		t.Fatalf("всё ниже порога — в запрос ничего: %+v", res)
	}
}

func TestSelectRerankWithoutRerankerIsError(t *testing.T) {
	if _, err := Select(context.Background(), nil, "q", cands(), Options{Rerank: true}); err == nil {
		t.Fatal("реранк без реранкера — ошибка, а не молчаливый один этап")
	}
}

func TestHTTPRerankerLogitsBecomeProbabilities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" {
			http.NotFound(w, r)
			return
		}
		// порядок результатов не обязан совпадать с порядком документов
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"index": 1, "relevance_score": -11.0},
			{"index": 0, "relevance_score": 2.0},
		}})
	}))
	defer srv.Close()
	rr := NewHTTPReranker(RerankerSpec{Name: "t", BaseURL: srv.URL + "/v1", Logits: true}, "")
	got, err := rr.Rerank(context.Background(), "q", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] < 0.85 || got[0] > 0.9 || got[1] > 0.001 {
		t.Fatalf("сигмоида логитов: %v", got)
	}
}

func TestScoreModes(t *testing.T) {
	p := Probe{Source: "a.md", Expect: []string{"факт"}}
	rs := []ModeResult{
		{Probe: p, Rank: 1, Kept: []Hit{{Chunk: Chunk{Source: "a.md", Text: "факт"}}, {Chunk: Chunk{Source: "b.md", Text: "шум"}}}},
		{Probe: p, Rank: 0, Kept: []Hit{{Chunk: Chunk{Source: "b.md", Text: "шум"}}}},
		{Probe: Probe{None: true}, Kept: nil},
		{Probe: Probe{None: true}, Kept: []Hit{{}}},
	}
	s := ScoreModes(rs)
	if s.InPrompt != 1 || s.At1 != 1 || s.Refused != 1 || s.OffBase != 2 || s.Noise != 1 {
		t.Fatalf("сводка режима: %+v", s)
	}
}
