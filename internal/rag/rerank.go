package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Реранкинг (день 23).
//
// Эмбеддинг считает вектор вопроса и вектор чанка по отдельности и
// сравнивает их косинусом. Это быстро — векторы чанков посчитаны заранее, —
// но грубо: «сводка» дня 9 (сжатие истории) и «сводка» дня 18 (курс по
// расписанию) лежат рядом, а вопрос, близкий к базе по теме, но без ответа
// в ней («курс биткоина в 2017»), получает почти тот же косинус, что
// настоящий.
//
// Кросс-энкодер читает вопрос и чанк вместе, одной моделью, и отвечает
// числом «насколько этот текст отвечает на этот вопрос». Это медленнее —
// по прогону модели на каждую пару, — поэтому реранкер смотрит не на всю
// базу, а на первые N кандидатов поиска.

// Reranker оценивает пары «вопрос — текст».
type Reranker interface {
	// Rerank — оценка каждого текста, в том же порядке. Оценка — вероятность
	// от 0 до 1, что текст отвечает на вопрос.
	Rerank(ctx context.Context, query string, docs []string) ([]float32, error)
	Name() string
}

// HTTPReranker — клиент к /v1/rerank в формате Jina/Cohere, который отдаёт
// llama.cpp (llama-server --reranking).
type HTTPReranker struct {
	spec RerankerSpec
	key  string
	http *http.Client
}

// RerankerSpec — реранкер из config.yaml.
type RerankerSpec struct {
	Name      string `yaml:"name"`
	BaseURL   string `yaml:"base_url"`
	Model     string `yaml:"model"`
	APIKeyEnv string `yaml:"api_key_env"`
	// Logits — сервер отдаёт сырой логит кросс-энкодера (llama.cpp: −11 … +8),
	// а не вероятность (Jina, Cohere). Порог в конфиге — вероятность,
	// поэтому логит прогоняется через сигмоиду.
	Logits      bool   `yaml:"logits"`
	Description string `yaml:"description"`
}

// NewHTTPReranker — клиент по описанию из конфига.
func NewHTTPReranker(spec RerankerSpec, key string) *HTTPReranker {
	return &HTTPReranker{spec: spec, key: key, http: &http.Client{Timeout: 2 * time.Minute}}
}

func (r *HTTPReranker) Name() string { return r.spec.Name }

type rerankReq struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

type rerankResp struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

func (r *HTTPReranker) Rerank(ctx context.Context, query string, docs []string) ([]float32, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	body, _ := json.Marshal(rerankReq{Model: r.spec.Model, Query: query, Documents: docs})
	url := strings.TrimRight(r.spec.BaseURL, "/") + "/rerank"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.key != "" {
		req.Header.Set("Authorization", "Bearer "+r.key)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("реранкер %s: %w (сервер запущен? scripts/rag-servers.ps1)", r.spec.Name, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("реранкер %s: %d %s", r.spec.Name, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var rr rerankResp
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, fmt.Errorf("реранкер %s: %w", r.spec.Name, err)
	}
	if len(rr.Results) != len(docs) {
		return nil, fmt.Errorf("реранкер %s: прислал %d оценок на %d текстов", r.spec.Name, len(rr.Results), len(docs))
	}
	out := make([]float32, len(docs))
	for _, x := range rr.Results {
		if x.Index < 0 || x.Index >= len(out) {
			return nil, fmt.Errorf("реранкер %s: индекс %d вне списка", r.spec.Name, x.Index)
		}
		out[x.Index] = float32(x.RelevanceScore)
		if r.spec.Logits {
			out[x.Index] = Sigmoid(x.RelevanceScore)
		}
	}
	return out, nil
}

// Sigmoid — логит в вероятность.
func Sigmoid(x float64) float32 { return float32(1 / (1 + math.Exp(-x))) }
