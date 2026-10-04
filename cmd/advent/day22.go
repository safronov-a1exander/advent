package main

// День 22 — первый RAG-запрос. Своей команды у шага нет: база знаний
// подключается к агенту в chat/demo (-rag) и в dialog (поле rag варианта).

import (
	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/rag"
)

// knowledgeFor — база знаний для агентов: индекс стратегии rag.strategy,
// посчитанный моделью embName. На заглушке-провайдере и эмбеддинги
// по умолчанию берутся у заглушки: репетиция не должна требовать
// видеокарты.
func knowledgeFor(cfg *config.Config, embName, provider string) (*rag.Retriever, error) {
	if embName == "" && provider == "mock" {
		embName = "mock"
	}
	emb, err := cfg.Embedder(embName)
	if err != nil {
		return nil, err
	}
	return rag.NewRetriever(rag.Path(cfg.RAG.IndexDir, emb.Name(), cfg.RAG.Strategy), emb), nil
}
