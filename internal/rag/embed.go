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

// Embedder превращает тексты в векторы. Один вызов — пачка текстов,
// на выходе векторы в том же порядке.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Name — что за модель: индекс помнит, чем он посчитан, и не даст
	// искать в нём векторами другой модели — их пространства несравнимы.
	Name() string
}

// EmbedderSpec — модель эмбеддингов из config.yaml.
type EmbedderSpec struct {
	Name    string `yaml:"name"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
	// APIKeyEnv — переменная с ключом; локальным серверам ключ не нужен.
	APIKeyEnv string `yaml:"api_key_env"`
	// Batch — сколько текстов в одном запросе. llama.cpp кладёт пачку
	// в один батч, и слишком большая не влезет в его окно.
	Batch       int    `yaml:"batch"`
	Description string `yaml:"description"`
}

// HTTPEmbedder — клиент к OpenAI-совместимому /v1/embeddings: llama.cpp
// (llama-server --embedding), Ollama, облачные API, заглушка стенда.
type HTTPEmbedder struct {
	spec EmbedderSpec
	key  string
	http *http.Client
	// Tokens — сколько токенов насчитал сервер за всё время: эмбеддинги
	// тоже стоят токенов, просто у локальной модели они бесплатные.
	Tokens int
}

// NewHTTPEmbedder — клиент по описанию из конфига.
func NewHTTPEmbedder(spec EmbedderSpec, key string) *HTTPEmbedder {
	if spec.Batch <= 0 {
		spec.Batch = 8
	}
	return &HTTPEmbedder{spec: spec, key: key, http: &http.Client{Timeout: 2 * time.Minute}}
}

func (e *HTTPEmbedder) Name() string {
	if e.spec.Model == "" || e.spec.Model == e.spec.Name {
		return e.spec.Name
	}
	return e.spec.Name + "/" + e.spec.Model
}

type embedReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResp struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
	} `json:"usage"`
}

func (e *HTTPEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += e.spec.Batch {
		end := min(i+e.spec.Batch, len(texts))
		vecs, err := e.batch(ctx, texts[i:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (e *HTTPEmbedder) batch(ctx context.Context, texts []string) ([][]float32, error) {
	body, _ := json.Marshal(embedReq{Model: e.spec.Model, Input: texts})
	url := strings.TrimRight(e.spec.BaseURL, "/") + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.key != "" {
		req.Header.Set("Authorization", "Bearer "+e.key)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("эмбеддинги %s: %w (сервер запущен? scripts/rag-servers.ps1)", e.spec.Name, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("эмбеддинги %s: %d %s", e.spec.Name, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var r embedResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("эмбеддинги %s: %w", e.spec.Name, err)
	}
	if len(r.Data) != len(texts) {
		return nil, fmt.Errorf("эмбеддинги %s: прислал %d векторов на %d текстов", e.spec.Name, len(r.Data), len(texts))
	}
	e.Tokens += r.Usage.PromptTokens
	out := make([][]float32, len(texts))
	for _, d := range r.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("эмбеддинги %s: индекс %d вне пачки", e.spec.Name, d.Index)
		}
		out[d.Index] = Normalize(d.Embedding)
	}
	return out, nil
}

// Normalize приводит вектор к длине 1. После этого косинусное сходство —
// просто скалярное произведение, и сравнивать можно векторы любой модели,
// даже той, что сама не нормирует.
func Normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	n := float32(1 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * n
	}
	return out
}

// Cosine — косинусное сходство нормированных векторов: 1 — один смысл,
// 0 — о разном.
func Cosine(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
