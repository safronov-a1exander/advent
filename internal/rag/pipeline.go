package rag

import (
	"context"
	"fmt"
	"sort"
)

// Options — как отбирать фрагменты для запроса (день 23).
//
// Поиск в два этапа: сначала быстрый и грубый — Candidates ближайших
// по косинусу, потом отсев — порог косинуса, реранкер и его порог.
// В запрос уходят первые TopK из того, что осталось. Осталось ноль —
// значит, в базе ответа нет, и честнее сказать это, чем кормить модель
// посторонним текстом.
type Options struct {
	TopK int
	// Candidates — сколько берёт первый этап; 0 — столько же, сколько TopK
	// (поиск в один этап, как на двадцать втором дне).
	Candidates int
	// MinScore — порог косинуса: кандидат ниже него отбрасывается до
	// реранкера. 0 — без порога.
	MinScore float32
	// Rerank — переоценить кандидатов кросс-энкодером и упорядочить
	// по его оценке.
	Rerank bool
	// MinRerank — порог реранкера (вероятность 0…1). 0 — без порога.
	MinRerank float32
}

// Result — что нашлось и что из этого отобрано.
type Result struct {
	// Query — по какому тексту искали: вопрос или его переписанная форма.
	Query string
	// Candidates — первый этап по порядку косинуса.
	Candidates []Hit
	// Kept — то, что уйдёт в запрос, в итоговом порядке.
	Kept []Hit
	// BelowScore и BelowRerank — сколько кандидатов срезал каждый порог.
	BelowScore, BelowRerank int
}

// Select — второй этап над уже найденными кандидатами: пороги, реранк,
// top-K. Кандидаты должны идти по убыванию косинуса.
func Select(ctx context.Context, rr Reranker, query string, cands []Hit, o Options) (*Result, error) {
	res := &Result{Query: query, Candidates: cands}
	var kept []Hit
	for i, h := range cands {
		h.Rank = i + 1
		res.Candidates[i].Rank = i + 1
		if o.MinScore > 0 && h.Score < o.MinScore {
			res.BelowScore++
			continue
		}
		kept = append(kept, h)
	}
	if o.Rerank && len(kept) > 0 {
		if rr == nil {
			return nil, fmt.Errorf("реранкер не подключён — в config.yaml нужен rag.reranker")
		}
		docs := make([]string, len(kept))
		for i, h := range kept {
			docs[i] = h.EmbedText()
		}
		scores, err := rr.Rerank(ctx, query, docs)
		if err != nil {
			return nil, err
		}
		for i := range kept {
			kept[i].Rerank = scores[i]
		}
		sort.SliceStable(kept, func(i, j int) bool { return kept[i].Rerank > kept[j].Rerank })
		n := 0
		for _, h := range kept {
			if o.MinRerank > 0 && h.Rerank < o.MinRerank {
				res.BelowRerank++
				continue
			}
			kept[n] = h
			n++
		}
		kept = kept[:n]
	}
	if o.TopK > 0 && len(kept) > o.TopK {
		kept = kept[:o.TopK]
	}
	res.Kept = kept
	return res, nil
}

// Search — оба этапа: поиск кандидатов по индексу и отбор.
func Search(ctx context.Context, ix *Index, emb Embedder, rr Reranker, query string, o Options) (*Result, error) {
	n := o.Candidates
	if n < o.TopK {
		n = o.TopK
	}
	cands, err := ix.Query(ctx, emb, query, n)
	if err != nil {
		return nil, err
	}
	return Select(ctx, rr, query, cands, o)
}
