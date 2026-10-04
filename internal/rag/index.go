package rag

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Index — чанки с векторами и всё, что нужно, чтобы им честно пользоваться:
// какой моделью посчитаны векторы, как резали, из каких файлов.
//
// Хранится одним JSON-файлом. Для базы в сотни чанков этого достаточно:
// поиск — перебор с косинусом, на тысяче векторов это доли миллисекунды.
// FAISS и прочие нужны там, где чанков миллионы.
type Index struct {
	Strategy Strategy `json:"strategy"`
	Chunking Chunking `json:"chunking"`
	Embedder string   `json:"embedder"`
	Dim      int      `json:"dim"`
	Built    string   `json:"built"`
	// Docs — сколько файлов и каких: по ним видно, что индекс устарел.
	Docs   []string `json:"docs"`
	Chunks []Entry  `json:"chunks"`
	// Tokens — сколько токенов ушло на эмбеддинги при сборке.
	Tokens int `json:"tokens"`
}

// Entry — чанк и его вектор.
type Entry struct {
	Chunk
	Vector []float32 `json:"-"`
	// Vec — вектор в base64 (float32, little endian). Текстом 1024 числа
	// занимали бы 20 КБ на чанк; так — 5,5 КБ, а метаданные остаются
	// читаемыми глазами.
	Vec string `json:"vec"`
}

// Build режет документы и считает векторы.
func Build(ctx context.Context, docs []Document, st Strategy, c Chunking, emb Embedder, progress func(done, total int)) (*Index, error) {
	c = c.withDefaults()
	ix := &Index{Strategy: st, Chunking: c, Embedder: emb.Name(), Built: time.Now().Format(time.RFC3339)}
	var chunks []Chunk
	for _, d := range docs {
		ix.Docs = append(ix.Docs, d.Source)
		chunks = append(chunks, Split(d, st, c)...)
	}
	texts := make([]string, len(chunks))
	for i, ch := range chunks {
		texts[i] = ch.EmbedText()
	}
	// пачками, чтобы показывать прогресс: на CPU индекс считается минуты
	const step = 32
	for i := 0; i < len(texts); i += step {
		end := min(i+step, len(texts))
		vecs, err := emb.Embed(ctx, texts[i:end])
		if err != nil {
			return nil, err
		}
		for j, v := range vecs {
			if ix.Dim == 0 {
				ix.Dim = len(v)
			}
			if len(v) != ix.Dim {
				return nil, fmt.Errorf("модель вернула вектор длины %d, а до этого %d", len(v), ix.Dim)
			}
			ix.Chunks = append(ix.Chunks, Entry{Chunk: chunks[i+j], Vector: v})
		}
		if progress != nil {
			progress(end, len(texts))
		}
	}
	if h, ok := emb.(*HTTPEmbedder); ok {
		ix.Tokens = h.Tokens
	}
	return ix, nil
}

// Hit — найденный чанк и его сходство с вопросом.
type Hit struct {
	Chunk
	Score float32 `json:"score"`
	// Rank — место на первом этапе поиска, с единицы (день 23): по нему
	// видно, откуда реранкер поднял фрагмент.
	Rank int `json:"rank,omitempty"`
	// Rerank — оценка реранкера, 0…1; 0 — реранкер не смотрел (день 23).
	Rerank float32 `json:"rerank,omitempty"`
}

// Search — k ближайших по косинусу чанков.
func (ix *Index) Search(q []float32, k int) []Hit {
	hits := make([]Hit, 0, len(ix.Chunks))
	for _, e := range ix.Chunks {
		hits = append(hits, Hit{Chunk: e.Chunk, Score: Cosine(q, e.Vector)})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// Query — вопрос в вектор и поиск: всё, что нужно снаружи.
func (ix *Index) Query(ctx context.Context, emb Embedder, question string, k int) ([]Hit, error) {
	if emb.Name() != ix.Embedder {
		return nil, fmt.Errorf("индекс посчитан моделью %s, а вопрос — %s: векторы разных моделей несравнимы, пересобери индекс", ix.Embedder, emb.Name())
	}
	vecs, err := emb.Embed(ctx, []string{question})
	if err != nil {
		return nil, err
	}
	if len(vecs[0]) != ix.Dim {
		return nil, fmt.Errorf("вектор вопроса длины %d, в индексе %d", len(vecs[0]), ix.Dim)
	}
	return ix.Search(vecs[0], k), nil
}

// Path — где лежит индекс: <dir>/<модель>/<стратегия>.json. Модель
// в пути, потому что индекс одной модели бесполезен для другой.
func Path(dir, embedder string, st Strategy) string {
	return filepath.Join(dir, safeName(embedder), string(st)+".json")
}

func safeName(s string) string {
	b := []rune(s)
	for i, r := range b {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			b[i] = '_'
		}
	}
	return string(b)
}

// Save пишет индекс атомарно: сначала во временный файл, потом
// переименование, — прерванная сборка не оставит полфайла.
func (ix *Index) Save(path string) error {
	for i := range ix.Chunks {
		ix.Chunks[i].Vec = encodeVec(ix.Chunks[i].Vector)
	}
	b, err := json.MarshalIndent(ix, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Open читает индекс.
func Open(path string) (*Index, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("индекса %s нет — собери: advent index", path)
		}
		return nil, err
	}
	var ix Index
	if err := json.Unmarshal(b, &ix); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i := range ix.Chunks {
		v, err := decodeVec(ix.Chunks[i].Vec)
		if err != nil {
			return nil, fmt.Errorf("%s: чанк %s: %w", path, ix.Chunks[i].ID, err)
		}
		if len(v) != ix.Dim {
			return nil, fmt.Errorf("%s: чанк %s: вектор длины %d, а dim %d", path, ix.Chunks[i].ID, len(v), ix.Dim)
		}
		ix.Chunks[i].Vector = v
	}
	return &ix, nil
}

func encodeVec(v []float32) string {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func decodeVec(s string) ([]float32, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("длина %d не кратна 4", len(b))
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}
