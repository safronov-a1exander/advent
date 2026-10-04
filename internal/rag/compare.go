package rag

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Stats — как нарезан индекс: сколько кусков, какой длины и сколько
// из них порваны посреди смысла.
type Stats struct {
	Chunks int
	Avg    int
	Min    int
	Max    int
	// Overhead — на сколько текста в индексе больше, чем в документах:
	// цена перекрытия. Эмбеддинги считаются и хранятся и для повторов.
	Overhead float64
	// MixedSections — чанки, в которых два раздела со своим текстом:
	// у такого чанка в метаданных один раздел, а текст — про два.
	MixedSections int
	// CutMidSentence — чанки, которые кончаются не на конце предложения,
	// строки таблицы или блока кода: следующий кусок начнётся с полуслова
	// чужой мысли.
	CutMidSentence int
}

// StatsOf считает нарезку по документам, из которых собран индекс.
func StatsOf(docs []Document, ix *Index) Stats {
	var st Stats
	text := map[string]Document{}
	docRunes := 0
	for _, d := range docs {
		text[d.Source] = d
		docRunes += utf8.RuneCountInString(d.Text)
	}
	secStarts := map[string][]section{}
	for _, d := range docs {
		secStarts[d.Source] = sections(d)
	}
	total := 0
	for _, e := range ix.Chunks {
		n := utf8.RuneCountInString(e.Text)
		total += n
		if st.Chunks == 0 || n < st.Min {
			st.Min = n
		}
		if n > st.Max {
			st.Max = n
		}
		st.Chunks++
		// начало текста чанка без ведущих пробелов
		d := text[e.Source]
		raw := d.Text[e.Start:e.End]
		from := e.Start + len(raw) - len(strings.TrimLeft(raw, " \t\n"))
		if mixed(d.Text, secStarts[e.Source], from, e.End) {
			st.MixedSections++
		}
		if !cleanEnd(e.Text) {
			st.CutMidSentence++
		}
	}
	if st.Chunks > 0 {
		st.Avg = total / st.Chunks
	}
	if docRunes > 0 {
		st.Overhead = float64(total)/float64(docRunes) - 1
	}
	return st
}

// mixed — есть ли в куске [from, end) два раздела, у каждого из которых
// хотя бы ownText знаков своего текста. Голый заголовок перед подразделом
// или введение документа разделом не считаются: они только предваряют.
func mixed(text string, secs []section, from, end int) bool {
	cuts := []int{from}
	for _, s := range secs {
		if s.start > from && s.start < end {
			cuts = append(cuts, s.start)
		}
	}
	cuts = append(cuts, end)
	parts := 0
	for i := 0; i+1 < len(cuts); i++ {
		if utf8.RuneCountInString(strings.TrimSpace(text[cuts[i]:cuts[i+1]])) >= ownText {
			parts++
		}
	}
	return parts >= 2
}

// ownText — сколько своего текста делает часть куска разделом.
const ownText = 100

// cleanEnd — кончается ли кусок на естественной границе.
func cleanEnd(s string) bool {
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	if s == "" {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s)
	return strings.ContainsRune(".!?:;|)»\"`}*", r)
}

// Probe — эталонный вопрос: где в базе лежит ответ. Проверка по тексту,
// а не по метаданным: у fixed-чанка раздел в метаданных — тот, на котором
// он начался, а факт мог уехать в его хвост. Попаданием считается чанк
// из нужного файла, в котором есть все подстроки Expect — факт целиком.
type Probe struct {
	Q      string   `yaml:"q"`
	Source string   `yaml:"source"`
	Expect []string `yaml:"expect"`
	Note   string   `yaml:"note"`
	// None — ответа в базе нет (день 23): хороший поиск не должен
	// отдавать в запрос ничего.
	None bool `yaml:"none"`
}

// ProbeSet — набор эталонных вопросов из YAML.
type ProbeSet struct {
	Name        string  `yaml:"name"`
	Description string  `yaml:"description"`
	K           int     `yaml:"k"`
	Probes      []Probe `yaml:"probes"`
	// Modes — режимы поиска для сравнения (день 23).
	Modes []Mode `yaml:"modes"`
}

// LoadProbes читает набор. Строго: неизвестное поле — ошибка.
func LoadProbes(path string) (*ProbeSet, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ps ProbeSet
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&ps); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i, p := range ps.Probes {
		if p.None == (len(p.Expect) > 0) {
			return nil, fmt.Errorf("%s: вопрос %d — нужен либо expect, либо none: true", path, i+1)
		}
	}
	if len(ps.Probes) == 0 {
		return nil, fmt.Errorf("%s: нет probes", path)
	}
	if ps.K <= 0 {
		ps.K = 5
	}
	return &ps, nil
}

// Contains — есть ли в чанке факт: все подстроки, без учёта регистра
// и переносов строк.
func (p Probe) Contains(c Chunk) bool {
	if p.Source != "" && c.Source != p.Source {
		return false
	}
	text := squash(c.Text)
	for _, e := range p.Expect {
		if !strings.Contains(text, squash(e)) {
			return false
		}
	}
	return true
}

func squash(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// ProbeResult — где нашёлся факт по одному вопросу.
type ProbeResult struct {
	Probe Probe
	// Rank — место первого чанка с фактом, с единицы; 0 — не нашёлся в top-K.
	Rank int
	Top  []Hit
	// Exists — есть ли такой чанк в индексе вообще. Если нет, факт
	// порван нарезкой, и никакой поиск его целиком не достанет.
	Exists bool
}

// Eval — прогон набора по индексу.
func Eval(ctx context.Context, ix *Index, emb Embedder, ps *ProbeSet) ([]ProbeResult, error) {
	var out []ProbeResult
	for _, p := range ps.Probes {
		if p.None {
			continue // вопросы без ответа в базе — для режимов дня 23
		}
		hits, err := ix.Query(ctx, emb, p.Q, ps.K)
		if err != nil {
			return nil, err
		}
		r := ProbeResult{Probe: p, Top: hits}
		for i, h := range hits {
			if p.Contains(h.Chunk) {
				r.Rank = i + 1
				break
			}
		}
		for _, e := range ix.Chunks {
			if p.Contains(e.Chunk) {
				r.Exists = true
				break
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// Score — сводка по набору: доля вопросов с фактом на первом месте,
// в top-3 и в top-K, и MRR — среднее 1/место (0, если не нашёлся).
type Score struct {
	N, At1, At3, AtK, Broken int
	MRR                      float64
}

func ScoreOf(rs []ProbeResult) Score {
	var s Score
	for _, r := range rs {
		s.N++
		if !r.Exists {
			s.Broken++
		}
		if r.Rank == 0 {
			continue
		}
		s.AtK++
		if r.Rank <= 3 {
			s.At3++
		}
		if r.Rank == 1 {
			s.At1++
		}
		s.MRR += 1 / float64(r.Rank)
	}
	if s.N > 0 {
		s.MRR /= float64(s.N)
	}
	return s
}

// Mode — режим поиска для сравнения (день 23): опции отбора и надо ли
// переписывать вопрос перед поиском.
type Mode struct {
	Name       string  `yaml:"name"`
	TopK       int     `yaml:"k"`
	Candidates int     `yaml:"candidates"`
	MinScore   float32 `yaml:"min_score"`
	Rerank     bool    `yaml:"rerank"`
	MinRerank  float32 `yaml:"min_rerank"`
	Rewrite    bool    `yaml:"rewrite"`
}

// Options — опции отбора режима.
func (m Mode) Options() Options {
	return Options{TopK: m.TopK, Candidates: m.Candidates, MinScore: m.MinScore, Rerank: m.Rerank, MinRerank: m.MinRerank}
}

// ModeResult — как режим отобрал фрагменты по одному вопросу.
type ModeResult struct {
	Probe Probe
	Query string
	Kept  []Hit
	// Rank — место фрагмента с фактом среди отобранных, с единицы; 0 — нет.
	Rank int
}

// Correct — режим поступил правильно: факт в запросе, а на вопрос, ответа
// на который в базе нет, — пустой запрос.
func (r ModeResult) Correct() bool {
	if r.Probe.None {
		return len(r.Kept) == 0
	}
	return r.Rank > 0
}

// ModeScore — сводка режима.
type ModeScore struct {
	Answerable, InPrompt, At1 int
	MRR                       float64
	// Noise — сколько отобранных фрагментов без факта в среднем на вопрос
	// с ответом: их модель читает зря и за них платят.
	Noise float64
	// OffBase и Refused — вопросов без ответа в базе и сколько из них
	// ушли без фрагментов.
	OffBase, Refused int
	// Kept — фрагментов в запросе в среднем.
	Kept float64
}

// ScoreModes — сводка по результатам режима.
func ScoreModes(rs []ModeResult) ModeScore {
	var s ModeScore
	kept, noise := 0, 0
	for _, r := range rs {
		kept += len(r.Kept)
		if r.Probe.None {
			s.OffBase++
			if len(r.Kept) == 0 {
				s.Refused++
			}
			continue
		}
		s.Answerable++
		for _, h := range r.Kept {
			if !r.Probe.Contains(h.Chunk) {
				noise++
			}
		}
		if r.Rank == 0 {
			continue
		}
		s.InPrompt++
		if r.Rank == 1 {
			s.At1++
		}
		s.MRR += 1 / float64(r.Rank)
	}
	if s.Answerable > 0 {
		s.MRR /= float64(s.Answerable)
		s.Noise = float64(noise) / float64(s.Answerable)
	}
	if len(rs) > 0 {
		s.Kept = float64(kept) / float64(len(rs))
	}
	return s
}
