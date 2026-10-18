package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/rag"
)

// Input — что ревьюим: метаданные PR и diff.
type Input struct {
	PR    int    `json:"pr,omitempty"`
	URL   string `json:"url,omitempty"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Base  string `json:"base"`
	Head  string `json:"head"`
	SHA   string `json:"sha"`
	Diff  string `json:"-"`
	Files []File `json:"files"`
}

// Knowledge — откуда брать документацию и код. *rag.Retriever подходит как есть.
type Knowledge interface {
	Retrieve(ctx context.Context, query string, o rag.Options) (*rag.Result, error)
}

// Chatter — модель. *llm.Client подходит как есть.
type Chatter interface {
	Chat(ctx context.Context, r llm.Request) (*llm.Response, error)
}

// Finding — одно замечание ревью.
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity,omitempty"` // high | medium | low — только у багов
	Problem  string `json:"problem"`
	Fix      string `json:"fix,omitempty"`
	// Note — пометка сверки кодом: строка вне изменений и т. п.
	Note string `json:"note,omitempty"`
}

// Verdict — итог ревью.
const (
	Approve        = "approve"
	Comment        = "comment"
	RequestChanges = "request_changes"
)

// Answer — то, что вернула модель, после разбора.
type Answer struct {
	Summary         string    `json:"summary"`
	Verdict         string    `json:"verdict"`
	Bugs            []Finding `json:"bugs"`
	Architecture    []Finding `json:"architecture"`
	Recommendations []Finding `json:"recommendations"`
}

// Attempt — одна попытка вызова модели: для отчёта и метрик.
type Attempt struct {
	Model   string        `json:"model"`
	Error   string        `json:"error,omitempty"`
	Latency time.Duration `json:"latency_ns"`
	Prompt  int           `json:"prompt_tokens"`
	Output  int           `json:"completion_tokens"`
	CostUSD float64       `json:"cost_usd"`
}

// Result — итог пайплайна.
type Result struct {
	Input   Input     `json:"input"`
	Checks  []Check   `json:"checks"`
	Context []rag.Hit `json:"-"`
	// Rules — сколько фрагментов правил и сколько связанного кода нашлось.
	Rules, Related int
	Answer         *Answer   `json:"answer,omitempty"`
	Dropped        []Finding `json:"dropped,omitempty"` // файла нет в diff — отброшено кодом
	Attempts       []Attempt `json:"attempts"`
	// Model — чей ответ принят; Fallback — это была запасная модель.
	Model    string `json:"model,omitempty"`
	Fallback bool   `json:"fallback,omitempty"`
	// Failed — модель не дала разбираемого ответа ни с одной попытки:
	// в ревью только проверки кодом и просьба к человеку.
	Failed bool          `json:"failed,omitempty"`
	Error  string        `json:"error,omitempty"`
	Took   time.Duration `json:"took_ns"`
}

// Cost — сколько стоили все попытки вместе.
func (r *Result) Cost() (cost float64, prompt, output int) {
	for _, a := range r.Attempts {
		cost += a.CostUSD
		prompt += a.Prompt
		output += a.Output
	}
	return
}

// Model — модель с подписью для отчёта.
type Model struct {
	Client Chatter
	ID     string
}

// Pipeline — настройки прогона.
type Pipeline struct {
	KB      Knowledge // nil — ревью без документации, с пометкой в отчёте
	Primary Model
	// Fallback — запасная модель: другая цепочка рассуждений и другая цена.
	// Пустой ID — запасной нет.
	Fallback Model
	// Tries — попыток на модель; Timeout — на один вызов; Backoff — пауза
	// перед повтором, удваивается.
	Tries   int
	Timeout time.Duration
	Backoff time.Duration
	// Checks — проверки кодом; nil — без них.
	Checks func(ctx context.Context, in Input) []Check
	// Log — что происходит по ходу: для консоли и записи видео.
	Log func(format string, a ...any)
	// MaxDiff — сколько символов diff отдаётся модели.
	MaxDiff int
}

func (p *Pipeline) logf(format string, a ...any) {
	if p.Log != nil {
		p.Log(format, a...)
	}
}

// Run — пайплайн целиком. Ошибку возвращает только то, без чего ревью
// не собрать вообще (пустой diff); всё остальное — в Result.
func (p *Pipeline) Run(ctx context.Context, in Input) (*Result, error) {
	started := time.Now()
	if strings.TrimSpace(in.Diff) == "" {
		return nil, errors.New("diff пустой — ревьюить нечего")
	}
	if len(in.Files) == 0 {
		in.Files = ParseDiff(in.Diff)
	}
	// diff без заголовков файлов: проверки и RAG работали бы с пустым
	// списком, а сверка отбросила бы все замечания — ревью выглядело бы
	// «чистым», ничего не проверив (замечание собственного ревью).
	if len(in.Files) == 0 {
		return nil, errors.New("diff не разобрался на файлы — это не unified diff git")
	}
	res := &Result{Input: in}
	add, del := 0, 0
	for _, f := range in.Files {
		add, del = add+f.Added, del+f.Deleted
	}
	p.logf("вход: %d файлов, +%d −%d, diff %d символов", len(in.Files), add, del, utf8.RuneCountInString(in.Diff))

	if p.Checks != nil {
		res.Checks = p.Checks(ctx, in)
		for _, c := range res.Checks {
			p.logf("проверка кодом · %s: %s", c.Name, c.Status())
		}
	}

	if p.KB != nil {
		res.Context, res.Rules, res.Related = gather(ctx, p.KB, in.Files, p.logf)
		p.logf("RAG: правил %d, связанного кода и документации %d", res.Rules, res.Related)
	} else {
		p.logf("RAG: база знаний не подключена — ревью без документации")
	}

	prompt := buildPrompt(in, res.Checks, res.Context, p.maxDiff())
	models := []Model{p.Primary}
	if p.Fallback.ID != "" && p.Fallback.ID != p.Primary.ID {
		models = append(models, p.Fallback)
	}
	var lastErr error
	for mi, m := range models {
		ans, err := p.ask(ctx, m, prompt, res)
		if err == nil {
			res.Answer, res.Model, res.Fallback = ans, m.ID, mi > 0
			break
		}
		lastErr = err
		if mi+1 < len(models) {
			p.logf("модель %s не справилась (%v) — запасная %s", m.ID, err, models[mi+1].ID)
		}
	}
	if res.Answer == nil {
		res.Failed = true
		if lastErr != nil {
			res.Error = lastErr.Error()
		}
		p.logf("AI-ревью не получилось: %s — в отчёте только проверки кодом, нужен человек", res.Error)
	} else {
		res.Dropped = validate(res.Answer, in.Files)
		if len(res.Dropped) > 0 {
			p.logf("сверка кодом: отброшено замечаний про файлы не из diff — %d", len(res.Dropped))
		}
		p.logf("ответ %s: багов %d, архитектура %d, рекомендаций %d, вердикт %s",
			res.Model, len(res.Answer.Bugs), len(res.Answer.Architecture), len(res.Answer.Recommendations), res.Answer.Verdict)
	}
	res.Took = time.Since(started)
	return res, nil
}

func (p *Pipeline) maxDiff() int {
	if p.MaxDiff > 0 {
		return p.MaxDiff
	}
	return 60000
}

// ask — попытки одной модели: повтор с паузой на ошибку API, повтор
// с просьбой исправить формат — на неразбираемый ответ.
func (p *Pipeline) ask(ctx context.Context, m Model, prompt string, res *Result) (*Answer, error) {
	tries := p.Tries
	if tries <= 0 {
		tries = 2
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	backoff := p.Backoff
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: prompt},
	}
	var lastErr error
	for i := 0; i < tries; i++ {
		if i > 0 && backoff > 0 {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			backoff *= 2
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		t := 0.2
		resp, err := m.Client.Chat(cctx, llm.Request{
			Model:          m.ID,
			Messages:       msgs,
			Temperature:    &t,
			ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
			Thinking:       &llm.Thinking{Type: "disabled"},
		})
		cancel()
		at := Attempt{Model: m.ID}
		if resp != nil {
			at.Latency, at.Prompt, at.Output, at.CostUSD = resp.Latency, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.CostUSD
		}
		if err != nil {
			at.Error = err.Error()
			res.Attempts = append(res.Attempts, at)
			lastErr = err
			p.logf("попытка %d · %s: ошибка API: %v", i+1, m.ID, err)
			continue
		}
		ans, perr := ParseAnswer(resp.Content)
		if perr != nil {
			at.Error = "ответ не разобрать: " + perr.Error()
			res.Attempts = append(res.Attempts, at)
			lastErr = perr
			p.logf("попытка %d · %s: %s — прошу исправить формат", i+1, m.ID, at.Error)
			msgs = append(msgs,
				llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
				llm.Message{Role: llm.RoleUser, Content: "Ответ не разобрался: " + perr.Error() +
					". Верни только JSON-объект в описанном формате, без текста вокруг."})
			continue
		}
		res.Attempts = append(res.Attempts, at)
		p.logf("попытка %d · %s: ответ за %s · in %d / out %d · $%.5f", i+1, m.ID,
			resp.Latency.Round(100*time.Millisecond), at.Prompt, at.Output, at.CostUSD)
		return ans, nil
	}
	return nil, lastErr
}

// ParseAnswer разбирает JSON модели и проверяет обязательное.
func ParseAnswer(s string) (*Answer, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "```json"), "```")
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	var a Answer
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Summary) == "" {
		return nil, errors.New("нет поля summary")
	}
	switch a.Verdict {
	case Approve, Comment, RequestChanges:
	default:
		return nil, fmt.Errorf("verdict %q — ожидали approve, comment или request_changes", a.Verdict)
	}
	return &a, nil
}

// validate — сверка ответа с diff кодом. Замечание про файл, которого
// в изменениях нет, — выдумка или ревью чужого кода: оно отбрасывается
// и показывается отдельно. Строка вне изменений не отбрасывается
// (баг может быть в контексте), но помечается.
func validate(a *Answer, files []File) (dropped []Finding) {
	keep := func(list []Finding) []Finding {
		var out []Finding
		for _, f := range list {
			if strings.TrimSpace(f.File) == "" {
				out = append(out, f) // общее замечание ко всему PR
				continue
			}
			ff, ok := Find(files, f.File)
			if !ok {
				f.Note = "файла нет в diff"
				dropped = append(dropped, f)
				continue
			}
			f.File = ff.Path
			if f.Line > 0 && ff.Status != "deleted" && !ff.Touches(f.Line) {
				f.Note = "строка вне изменений"
			}
			out = append(out, f)
		}
		return out
	}
	a.Bugs = keep(a.Bugs)
	a.Architecture = keep(a.Architecture)
	a.Recommendations = keep(a.Recommendations)
	sort.SliceStable(a.Bugs, func(i, j int) bool { return sevRank(a.Bugs[i].Severity) < sevRank(a.Bugs[j].Severity) })
	return dropped
}

func sevRank(s string) int {
	switch s {
	case "high":
		return 0
	case "medium":
		return 1
	}
	return 2
}

// gather — контекст из базы знаний: правила ревью проекта одним запросом
// и по запросу на каждый изменённый файл — связанный код и документация.
func gather(ctx context.Context, kb Knowledge, files []File, logf func(string, ...any)) (hits []rag.Hit, rules, related int) {
	seen := map[string]bool{}
	take := func(q string, k int) int {
		res, err := kb.Retrieve(ctx, q, rag.Options{TopK: k, Candidates: 10, Rerank: true, MinRerank: 0.05})
		if err != nil {
			logf("RAG: %v", err)
			return 0
		}
		n := 0
		for _, h := range res.Kept {
			if seen[h.ID] {
				continue
			}
			seen[h.ID] = true
			hits = append(hits, h)
			n++
		}
		return n
	}
	rules = take("правила ревью PR в этом проекте: оформление, ветки, тесты, комментарии, секреты", 4)
	for i, f := range files {
		if i >= 8 || f.Status == "deleted" {
			continue
		}
		q := f.Path + "\n" + clipRunes(f.AddedText, 500)
		related += take(q, 2)
	}
	return hits, rules, related
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
