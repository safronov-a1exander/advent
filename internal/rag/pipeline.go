package rag

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
	// Expand — отдать в запрос раздел целиком, а не найденный кусок (день 25).
	Expand bool
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
			// и в кандидатах: по ним видно, насколько близко к порогу было
			// лучшее, когда в запрос не прошло ничего (день 24)
			res.Candidates[kept[i].Rank-1].Rerank = scores[i]
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
	res, err := Select(ctx, rr, query, cands, o)
	if err != nil {
		return nil, err
	}
	if o.Expand {
		res.Kept = expandAll(ix, res.Kept)
	}
	return res, nil
}

// Expand — фрагмент вместе с соседними кусками того же раздела (день 25).
//
// Длинный раздел нарезка делит по абзацам, и таблица с числами может
// оказаться в одном чанке, а выводы по ней — в соседнем. Поиск находит
// выводы (их текст ближе к вопросу), а числа остаются за бортом: на
// двадцать пятом дне так потерялась цена прогона из таблицы дня 20 —
// реранкер дал таблице 0.07, абзацу под ней 0.58. Ищем по маленьким
// кускам, в запрос отдаём раздел целиком — «small-to-big».
//
// Только для нарезки по структуре: у fixed соседние окна перекрываются
// и раздела в метаданных честно не знают.
func (ix *Index) Expand(h Hit) Hit {
	if ix.Strategy != Structure {
		return h
	}
	i := ix.position(h.ID)
	if i < 0 {
		return h
	}
	same := func(j int) bool {
		c := ix.Chunks[j]
		return c.Source == h.Source && c.Section == h.Section
	}
	lo, hi := i, i
	for lo > 0 && same(lo-1) {
		lo--
	}
	for hi+1 < len(ix.Chunks) && same(hi+1) {
		hi++
	}
	if lo == hi {
		return h
	}
	parts := make([]string, 0, hi-lo+1)
	for j := lo; j <= hi; j++ {
		parts = append(parts, ix.Chunks[j].Text)
		h.Parts = append(h.Parts, ix.Chunks[j].ID)
	}
	h.Text = strings.Join(parts, "\n\n")
	h.Start, h.End = ix.Chunks[lo].Start, ix.Chunks[hi].End
	return h
}

func (ix *Index) position(id string) int {
	ix.posOnce.Do(func() {
		ix.pos = make(map[string]int, len(ix.Chunks))
		for i, c := range ix.Chunks {
			ix.pos[c.ID] = i
		}
	})
	if i, ok := ix.pos[id]; ok {
		return i
	}
	return -1
}

// expandAll расширяет отобранные фрагменты; два куска одного раздела
// становятся одним фрагментом — на месте первого.
func expandAll(ix *Index, kept []Hit) []Hit {
	var out []Hit
	seen := map[string]bool{}
	for _, h := range kept {
		e := ix.Expand(h)
		key := e.Source + "\x00" + e.Section + "\x00" + fmt.Sprint(e.Start)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}
