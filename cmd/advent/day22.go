package main

// День 22 — первый RAG-запрос. Своей команды у шага нет: база знаний
// подключается к агенту в chat/demo (-rag) и в dialog (поле rag варианта).

import (
	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/rag"
)

// knowledgeFor — база знаний для агентов: индекс стратегии rag.strategy,
// посчитанный моделью embName, и реранкер rag.reranker (день 23). На
// заглушке-провайдере и эмбеддинги, и реранкер по умолчанию берутся
// у заглушки: репетиция не должна требовать видеокарты.
// base — отдельная база из rag.bases (день 31); пусто — основная.
func knowledgeFor(cfg *config.Config, embName, provider, base string) (*rag.Retriever, error) {
	_, indexDir, err := cfg.Base(base)
	if err != nil {
		return nil, err
	}
	rrName := ""
	if embName == "" && provider == "mock" {
		embName, rrName = "mock", "mock"
	}
	emb, err := cfg.Embedder(embName)
	if err != nil {
		return nil, err
	}
	// Реранкер нужен не всем: без него поиск работает в один этап, а
	// вариант с rag_rerank получит понятную ошибку в ленте.
	var rr rag.Reranker
	if r, err := cfg.Reranker(rrName); err == nil {
		rr = r
	}
	return rag.NewRetriever(rag.Path(indexDir, emb.Name(), cfg.RAG.Strategy), emb, rr), nil
}
