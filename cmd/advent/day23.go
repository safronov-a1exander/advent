package main

// День 23 — второй этап поиска: порог, реранкер, переписывание запроса.
//   advent retrieval -probes scenarios/day23-retrieval.yaml
//
// Режимы поиска из YAML рядом на одних эталонных вопросах — без генерации
// ответа: проверяется только то, что уйдёт в запрос. Попал ли в него
// фрагмент с фактом, на каком месте, сколько рядом лишних фрагментов
// и что режим отдал на вопросы, ответа на которые в базе нет.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/rag"
)

func cmdRetrieval(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("retrieval", flag.ExitOnError)
	c := bindCommon(fs)
	probes := fs.String("probes", "scenarios/day23-retrieval.yaml", "YAML с эталонными вопросами и режимами")
	embName := fs.String("embedder", "", "модель эмбеддингов (пусто — rag.embedder; на провайдере mock — mock)")
	model := fs.String("model", "", "модель для переписывания запроса (пусто — default_model провайдера)")
	verbose := fs.Bool("v", false, "печатать переписанные запросы и отобранные фрагменты")
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
	ps, err := rag.LoadProbes(*probes)
	if err != nil {
		return err
	}
	if len(ps.Modes) == 0 {
		return fmt.Errorf("%s: нет modes — сравнивать нечего", *probes)
	}
	prov := c.provider
	if prov == "" {
		prov = cfg.DefaultProvider
	}
	kb, err := knowledgeFor(cfg, *embName, prov)
	if err != nil {
		return err
	}
	ix, err := kb.Index()
	if err != nil {
		return err
	}
	fmt.Printf("индекс %s · %d чанков · эмбеддинги %s", cfg.RAG.Strategy, len(ix.Chunks), ix.Embedder)
	if kb.Rerank != nil {
		fmt.Printf(" · реранкер %s", kb.Rerank.Name())
	}
	fmt.Printf("\nвопросов %d, из них без ответа в базе %d · режимов %d\n", len(ps.Probes), countNone(ps), len(ps.Modes))

	// Переписанные запросы считаются один раз на вопрос: режимы с rewrite
	// отличаются отбором, а не запросом.
	rewritten := map[int]string{}
	if needsRewrite(ps) {
		client, p, err := cfg.Client(c.provider)
		if err != nil {
			return err
		}
		m := *model
		if m == "" {
			m = p.DefaultMod
		}
		var in, out int
		var cost float64
		for i, pr := range ps.Probes {
			q, _, resp, err := agent.RewriteQuery(ctx, client, m, nil, pr.Q)
			if err != nil {
				return fmt.Errorf("переписать запрос: %w", err)
			}
			rewritten[i] = q
			in += resp.Usage.PromptTokens
			out += resp.Usage.CompletionTokens
			cost += resp.CostUSD
		}
		fmt.Printf("переписывание запросов: %s, вход %d · выход %d токенов · $%.5f\n", m, in, out, cost)
	}

	results := make([][]rag.ModeResult, len(ps.Modes))
	elapsed := make([]time.Duration, len(ps.Modes))
	for mi, mode := range ps.Modes {
		started := time.Now()
		for i, pr := range ps.Probes {
			q := pr.Q
			if mode.Rewrite {
				q = rewritten[i]
			}
			res, err := rag.Search(ctx, ix, kb.Emb, kb.Rerank, q, mode.Options())
			if err != nil {
				return fmt.Errorf("режим «%s»: %w", mode.Name, err)
			}
			r := rag.ModeResult{Probe: pr, Query: q, Kept: res.Kept}
			for j, h := range res.Kept {
				if !pr.None && pr.Contains(h.Chunk) {
					r.Rank = j + 1
					break
				}
			}
			results[mi] = append(results[mi], r)
		}
		elapsed[mi] = time.Since(started)
	}

	fmt.Println("\n== место фрагмента с фактом среди отобранных; для вопросов без ответа — сколько отдано ==")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	head := "#\tвопрос"
	for _, m := range ps.Modes {
		head += "\t" + m.Name
	}
	fmt.Fprintln(w, head+"\t")
	for i, pr := range ps.Probes {
		q := clip(pr.Q, 50)
		if pr.None {
			q = "∅ " + q
		}
		line := fmt.Sprintf("%d\t%s", i+1, q)
		for mi := range ps.Modes {
			line += "\t" + modeCell(results[mi][i])
		}
		fmt.Fprintln(w, line+"\t")
	}
	w.Flush()

	fmt.Println()
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "режим\tфакт в запросе\tна 1-м месте\tMRR\tлишних фрагм.\tбез ответа → пусто\tфрагм. в запросе\tвремя\t")
	for mi, m := range ps.Modes {
		s := rag.ScoreModes(results[mi])
		fmt.Fprintf(w, "%s\t%d/%d\t%d/%d\t%.2f\t%.1f\t%d/%d\t%.1f\t%s\t\n", m.Name,
			s.InPrompt, s.Answerable, s.At1, s.Answerable, s.MRR, s.Noise,
			s.Refused, s.OffBase, s.Kept, elapsed[mi].Round(10*time.Millisecond))
	}
	w.Flush()

	if *verbose {
		for mi, m := range ps.Modes {
			fmt.Printf("\n== %s ==\n", m.Name)
			for i, r := range results[mi] {
				fmt.Printf("%d. %s\n", i+1, r.Query)
				fmt.Println("   " + strings.ReplaceAll(agent.HitTrace(r.Kept), "\n", "\n   "))
			}
		}
	}
	return nil
}

func modeCell(r rag.ModeResult) string {
	if r.Probe.None {
		if len(r.Kept) == 0 {
			return "0 ✓"
		}
		return fmt.Sprintf("%d ✗", len(r.Kept))
	}
	if r.Rank == 0 {
		return "—"
	}
	return fmt.Sprintf("%d из %d", r.Rank, len(r.Kept))
}

func countNone(ps *rag.ProbeSet) int {
	n := 0
	for _, p := range ps.Probes {
		if p.None {
			n++
		}
	}
	return n
}

func needsRewrite(ps *rag.ProbeSet) bool {
	for _, m := range ps.Modes {
		if m.Rewrite {
			return true
		}
	}
	return false
}
