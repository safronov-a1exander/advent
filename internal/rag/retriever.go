package rag

import (
	"context"
	"sync"
)

// Retriever — индекс, модель эмбеддингов, которой посчитаны его векторы,
// и реранкер: всё, что нужно агенту, чтобы найти фрагменты под вопрос
// (дни 22–23).
//
// Индекс читается с диска при первом вопросе, а не при старте: чат без
// RAG не должен падать оттого, что индекс ещё не собран.
type Retriever struct {
	Path string
	Emb  Embedder
	// Rerank — реранкер второго этапа (день 23); nil — его нет.
	Rerank Reranker

	once sync.Once
	ix   *Index
	err  error
}

// NewRetriever — поиск по индексу из файла.
func NewRetriever(path string, emb Embedder, rr Reranker) *Retriever {
	return &Retriever{Path: path, Emb: emb, Rerank: rr}
}

// Retrieve — фрагменты под вопрос: кандидаты и отбор по o.
func (r *Retriever) Retrieve(ctx context.Context, query string, o Options) (*Result, error) {
	ix, err := r.Index()
	if err != nil {
		return nil, err
	}
	return Search(ctx, ix, r.Emb, r.Rerank, query, o)
}

// Index — индекс, прочитанный при первом обращении.
func (r *Retriever) Index() (*Index, error) {
	r.once.Do(func() { r.ix, r.err = Open(r.Path) })
	return r.ix, r.err
}
