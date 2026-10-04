package main

// День 21 — индексация документов.
//   advent index                         собрать оба индекса (fixed и structure) и сравнить нарезку
//   advent index -strategy structure     только один
//   advent index -probes scenarios/day21-chunking.yaml   плюс эталонные вопросы: где нашёлся факт
//   advent index -dry                    только нарезка, без эмбеддингов — посмотреть чанки
//   advent search "вопрос"               ближайшие чанки из индекса

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/rag"
)

// charsPerPage — «страница» для оценки объёма базы: 1800 знаков
// с пробелами, машинописный стандарт.
const charsPerPage = 1800

func cmdIndex(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	c := bindCommon(fs)
	strategy := fs.String("strategy", "all", "fixed | structure | all")
	embName := fs.String("embedder", "", "модель эмбеддингов из config.yaml (пусто — rag.embedder)")
	probes := fs.String("probes", "", "YAML с эталонными вопросами — сравнить, где находится факт")
	dry := fs.Bool("dry", false, "только нарезать и показать чанки, без эмбеддингов")
	show := fs.Int("show", 0, "с -dry: напечатать первые N чанков каждой стратегии")
	only := fs.String("source", "", "с -dry: только этот файл, например docs/days/day20.md")
	hold := fs.Duration("hold", 0, "подержать итог на экране перед выходом — для записи видео")
	if err := fs.Parse(args); err != nil {
		return err
	}
	defer func() {
		if *hold > 0 {
			time.Sleep(*hold)
		}
	}()

	cfg, err := config.Load(c.dir)
	if err != nil {
		return err
	}
	docs, err := rag.Load(c.dir, cfg.RAG.Sources)
	if err != nil {
		return err
	}
	strategies := rag.Strategies
	if *strategy != "all" {
		st, err := rag.ParseStrategy(*strategy)
		if err != nil {
			return err
		}
		strategies = []rag.Strategy{st}
	}

	printCorpus(docs)

	if *dry {
		if *only != "" {
			var picked []rag.Document
			for _, d := range docs {
				if d.Source == *only {
					picked = append(picked, d)
				}
			}
			if len(picked) == 0 {
				return fmt.Errorf("файла %s нет в базе знаний", *only)
			}
			docs = picked
		}
		for _, st := range strategies {
			ix := &rag.Index{Strategy: st}
			for _, d := range docs {
				for _, ch := range rag.Split(d, st, cfg.RAG.Chunking) {
					ix.Chunks = append(ix.Chunks, rag.Entry{Chunk: ch})
				}
			}
			fmt.Printf("\n== %s ==\n", st)
			printStats([]statRow{{st, rag.StatsOf(docs, ix)}})
			for i, e := range ix.Chunks {
				if i >= *show {
					break
				}
				fmt.Printf("\n--- %s  [%s]  %d симв.\n%s\n", e.ID, e.Header(), utf8.RuneCountInString(e.Text), e.Text)
			}
		}
		return nil
	}

	emb, err := cfg.Embedder(*embName)
	if err != nil {
		return err
	}
	var rows []statRow
	built := map[rag.Strategy]*rag.Index{}
	for _, st := range strategies {
		started := time.Now()
		fmt.Printf("\n== %s: нарезка и эмбеддинги (%s) ==\n", st, emb.Name())
		before := emb.Tokens
		ix, err := rag.Build(ctx, docs, st, cfg.RAG.Chunking, emb, func(done, total int) {
			fmt.Printf("\r  %d / %d чанков", done, total)
		})
		if err != nil {
			fmt.Println()
			return err
		}
		ix.Tokens = emb.Tokens - before
		path := rag.Path(cfg.RAG.IndexDir, emb.Name(), st)
		if err := ix.Save(path); err != nil {
			return err
		}
		size := int64(0)
		if fi, err := os.Stat(path); err == nil {
			size = fi.Size()
		}
		fmt.Printf("\n  вектор: %d чисел · токенов на эмбеддинги: %d · %.1f с · %s (%.1f МБ)\n",
			ix.Dim, ix.Tokens, time.Since(started).Seconds(), filepath.ToSlash(path), float64(size)/1e6)
		rows = append(rows, statRow{st, rag.StatsOf(docs, ix)})
		built[st] = ix
	}

	fmt.Println("\n== как нарезано ==")
	printStats(rows)

	if *probes == "" {
		return nil
	}
	ps, err := rag.LoadProbes(*probes)
	if err != nil {
		return err
	}
	return compareProbes(ctx, ps, strategies, built, emb)
}

type statRow struct {
	st rag.Strategy
	s  rag.Stats
}

func printCorpus(docs []rag.Document) {
	total := 0
	kinds := map[rag.Kind]int{}
	for _, d := range docs {
		total += utf8.RuneCountInString(d.Text)
		kinds[d.Kind]++
	}
	fmt.Printf("база знаний: %d файлов (markdown %d, Go %d) · %d знаков ≈ %d страниц\n",
		len(docs), kinds[rag.KindMarkdown], kinds[rag.KindGo], total, total/charsPerPage)
}

func printStats(rows []statRow) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "стратегия\tчанков\tсредний\tмин\tмакс\tповтор текста\tдва раздела в чанке\tоборван посреди фразы\t")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%+.0f%%\t%d (%.0f%%)\t%d (%.0f%%)\t\n",
			r.st, r.s.Chunks, r.s.Avg, r.s.Min, r.s.Max, 100*r.s.Overhead,
			r.s.MixedSections, pct(r.s.MixedSections, r.s.Chunks),
			r.s.CutMidSentence, pct(r.s.CutMidSentence, r.s.Chunks))
	}
	w.Flush()
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

// compareProbes гоняет эталонные вопросы по каждому индексу и печатает,
// на каком месте нашёлся чанк с фактом.
func compareProbes(ctx context.Context, ps *rag.ProbeSet, strategies []rag.Strategy, built map[rag.Strategy]*rag.Index, emb rag.Embedder) error {
	results := map[rag.Strategy][]rag.ProbeResult{}
	for _, st := range strategies {
		rs, err := rag.Eval(ctx, built[st], emb, ps)
		if err != nil {
			return err
		}
		results[st] = rs
	}

	fmt.Printf("\n== эталонные вопросы: %d, место чанка с фактом в top-%d ==\n", len(results[strategies[0]]), ps.K)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	head := "#\tвопрос"
	for _, st := range strategies {
		head += "\t" + string(st)
	}
	fmt.Fprintln(w, head+"\t")
	for i, r := range results[strategies[0]] {
		line := fmt.Sprintf("%d\t%s", i+1, clip(r.Probe.Q, 58))
		for _, st := range strategies {
			line += "\t" + rankCell(results[st][i])
		}
		fmt.Fprintln(w, line+"\t")
	}
	w.Flush()

	fmt.Println()
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintf(w, "стратегия\tна 1-м месте\tв top-3\tв top-%d\tMRR\tфакт порван нарезкой\t\n", ps.K)
	for _, st := range strategies {
		s := rag.ScoreOf(results[st])
		fmt.Fprintf(w, "%s\t%d/%d\t%d/%d\t%d/%d\t%.2f\t%d\t\n", st, s.At1, s.N, s.At3, s.N, s.AtK, s.N, s.MRR, s.Broken)
	}
	w.Flush()
	return nil
}

func rankCell(r rag.ProbeResult) string {
	switch {
	case r.Rank > 0:
		return fmt.Sprintf("%d", r.Rank)
	case !r.Exists:
		return "порван"
	default:
		return "—"
	}
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func cmdSearch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	c := bindCommon(fs)
	strategy := fs.String("strategy", "", "fixed | structure (пусто — rag.strategy)")
	embName := fs.String("embedder", "", "модель эмбеддингов из config.yaml")
	k := fs.Int("k", 5, "сколько чанков показать")
	full := fs.Bool("full", false, "печатать чанк целиком")
	rerank := fs.Bool("rerank", false, "переоценить найденное реранкером и упорядочить по его оценке (день 23)")
	hold := fs.Duration("hold", 0, "подержать итог на экране перед выходом")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return err
	}
	defer func() {
		if *hold > 0 {
			time.Sleep(*hold)
		}
	}()
	q := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if q == "" {
		return fmt.Errorf("нужен вопрос: advent search \"…\"")
	}
	cfg, err := config.Load(c.dir)
	if err != nil {
		return err
	}
	st := cfg.RAG.Strategy
	if *strategy != "" {
		if st, err = rag.ParseStrategy(*strategy); err != nil {
			return err
		}
	}
	emb, err := cfg.Embedder(*embName)
	if err != nil {
		return err
	}
	ix, err := rag.Open(rag.Path(cfg.RAG.IndexDir, emb.Name(), st))
	if err != nil {
		return err
	}
	hits, err := ix.Query(ctx, emb, q, *k)
	if err != nil {
		return err
	}
	if *rerank {
		rr, err := cfg.Reranker("")
		if err != nil {
			return err
		}
		res, err := rag.Select(ctx, rr, q, hits, rag.Options{Rerank: true})
		if err != nil {
			return err
		}
		hits = res.Kept
	}
	fmt.Printf("вопрос: %s\nиндекс: %s, %d чанков, %s\n", q, st, len(ix.Chunks), ix.Embedder)
	for i, h := range hits {
		score := fmt.Sprintf("%.3f", h.Score)
		if *rerank {
			score += fmt.Sprintf(" · реранк %.2f (был %d-м)", h.Rerank, h.Rank)
		}
		fmt.Printf("\n%d. %s  %s\n   %s\n", i+1, score, h.ID, h.Header())
		text := h.Text
		if !*full {
			text = clip(text, 300)
		}
		fmt.Println("   " + strings.ReplaceAll(text, "\n", "\n   "))
	}
	return nil
}

// reorderFlags переносит флаги вперёд: «search вопрос -k 3» читается
// так же, как «search -k 3 вопрос». Пакет flag останавливается на первом
// позиционном аргументе.
func reorderFlags(args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !isBoolFlag(a) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		rest = append(rest, a)
	}
	return append(flags, rest...)
}

func isBoolFlag(a string) bool {
	switch strings.TrimLeft(a, "-") {
	case "full", "rerank":
		return true
	}
	return false
}
