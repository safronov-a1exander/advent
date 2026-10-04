package rag

import (
	"context"
	"sync"
)

// Retriever — индекс и модель эмбеддингов, которой посчитаны его векторы:
// всё, что нужно агенту, чтобы найти фрагменты под вопрос (день 22).
//
// Индекс читается с диска при первом вопросе, а не при старте: чат без
// RAG не должен падать оттого, что индекс ещё не собран.
type Retriever struct {
	Path string
	Emb  Embedder

	once sync.Once
	ix   *Index
	err  error
}

// NewRetriever — поиск по индексу из файла.
func NewRetriever(path string, emb Embedder) *Retriever {
	return &Retriever{Path: path, Emb: emb}
}

// Retrieve — k ближайших к вопросу фрагментов.
func (r *Retriever) Retrieve(ctx context.Context, query string, k int) ([]Hit, error) {
	ix, err := r.Index()
	if err != nil {
		return nil, err
	}
	return ix.Query(ctx, r.Emb, query, k)
}

// Index — индекс, прочитанный при первом обращении.
func (r *Retriever) Index() (*Index, error) {
	r.once.Do(func() { r.ix, r.err = Open(r.Path) })
	return r.ix, r.err
}
