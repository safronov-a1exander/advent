package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/memory"
	"github.com/safronov-a1exander/advent/internal/rag"
)

// База знаний (день 22).
//
// Ход с RAG устроен как в лекции: вопрос → поиск ближайших фрагментов →
// фрагменты вместе с вопросом → запрос к модели. Поиск идёт до запроса
// и по каждому вопросу; модель не решает, искать ли, — в отличие от
// инструментов MCP, это делает агент.
//
// Фрагменты уходят в сообщение пользователя этого хода, а в историю
// разговора ложится сам вопрос. Иначе каждый следующий ход тащил бы
// с собой фрагменты всех прошлых — и платил бы за них снова и снова,
// хотя к новому вопросу они чаще всего отношения не имеют. Нужны снова —
// найдутся снова.

// Knowledge — откуда агент берёт фрагменты. *rag.Retriever подходит как есть.
type Knowledge interface {
	Retrieve(ctx context.Context, query string, o rag.Options) (*rag.Result, error)
}

// DefaultRAGTopK — сколько фрагментов уходит в запрос по умолчанию. Три —
// по замеру дня 21: у нарезки по разделам факт попадает в top-3
// в пятнадцати вопросах из шестнадцати.
const DefaultRAGTopK = 3

// RAGModes — значения поля rag для панели и флагов.
var RAGModes = []string{"", "on"}

func (c Config) ragTopK() int {
	if c.RAGTopK != nil && *c.RAGTopK > 0 {
		return *c.RAGTopK
	}
	return DefaultRAGTopK
}

// SetKnowledge подключает базу знаний. Пользоваться ли ею, решает конфиг
// агента (Config.RAG).
func (p *Pool) SetKnowledge(k Knowledge) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.knowledge = k
	for _, a := range p.agents {
		a.mu.Lock()
		a.knowledge = k
		a.mu.Unlock()
	}
}

// retrieve — фрагменты под вопрос. Недоступная база не ломает ход: агент
// говорит об этом в ленте и отвечает без неё, как и при недоступном MCP.
// nil — поиска не было (режим выключен или база недоступна); пустой
// Kept — поиск был, но ничего не прошло порог (день 24).
//
// С rag_rewrite искать идут не по вопросу, а по его переписанной форме
// (день 23): служебный вызов модели раскрывает отсылки к прошлым репликам
// и называет термины, которыми это могло быть записано в документах.
//
// funcs — инструменты этого хода (день 31): если они есть, rewrite
// работает и роутером — вопрос о текущем состоянии, которое знают
// инструменты, в документации не ищется.
func (a *Agent) retrieve(ctx context.Context, cfg Config, mem *memory.Memory, hist []llm.Message, text string, funcs []llm.Tool, turn *Turn, on func(Event)) (res *rag.Result, skip Skip) {
	if cfg.RAG == "" {
		return nil, ""
	}
	a.mu.Lock()
	kb := a.knowledge
	a.mu.Unlock()
	if kb == nil {
		on(Event{Kind: EventRetrieval, Label: "база знаний не подключена", Failed: true})
		return nil, ""
	}
	query := text
	if cfg.RAGRewrite {
		query = a.rewriteQuery(ctx, cfg, taskMemo(mem), hist, text, funcs, turn, on)
		// День 25: вопрос о самом разговоре — «напомни цель», «что мы
		// решили», «спасибо». В базе его ответа нет и быть не может,
		// а «не знаю» дня 24 на него было бы неправдой: ответ — в памяти.
		if query == NoSearch {
			on(Event{Kind: EventRetrieval, Label: "поиск не нужен: вопрос о самом разговоре — отвечаю по памяти и истории"})
			return nil, SkipConversation
		}
		// День 31: вопрос о том, что знают инструменты, а не документы.
		if len(funcs) > 0 && query == ToolsRoute {
			on(Event{Kind: EventRetrieval, Label: "поиск не нужен: вопрос о текущем состоянии — отвечают инструменты"})
			return nil, SkipTools
		}
	}
	o := cfg.ragOptions()
	if cfg.RAGRerankQuestion && query != text {
		o.RerankQuery = text + " (" + query + ")"
	}
	res, err := kb.Retrieve(ctx, query, o)
	if err != nil {
		on(Event{Kind: EventRetrieval, Label: "база знаний недоступна", Content: err.Error(), Failed: true})
		return nil, ""
	}
	on(Event{Kind: EventRetrieval, Label: retrievalLabel(cfg, res), Content: HitTrace(res.Kept)})
	return res, ""
}

// NoSearch — ответ rewrite, когда искать в базе нечего (день 25).
const NoSearch = "NONE"

// ToolsRoute — ответ rewrite, когда на вопрос отвечают инструменты, а не
// документы (день 31): «что изменилось на ветке» — это git, а не заметка,
// в которой однажды описали, что на ветке менялось.
const ToolsRoute = "TOOLS"

// Skip — почему в базе не искали.
type Skip string

const (
	SkipConversation Skip = "conversation" // вопрос о самом разговоре (день 25)
	SkipTools        Skip = "tools"        // вопрос о том, что знают инструменты (день 31)
)

// ToolsSource — подпись источника, когда в документации не искали,
// потому что спрашивали о текущем состоянии (день 31).
const ToolsSource = "Источник: инструменты — вопрос о текущем состоянии, в документации не искали."

// ConversationSource — подпись источника у ответа, для которого в базе
// не искали: «всегда выводить источники» значит и здесь сказать, откуда
// ответ (день 25).
const ConversationSource = "Источник: разговор и память задачи — в базе знаний не искали."

// NoDocsNote и NoDocsSource — ход с RAGOpen, когда в документации ничего
// не нашлось (день 31): модель знает, что фрагментов нет, и ответ
// подписан тем, откуда он на самом деле.
const (
	NoDocsNote   = "В документации проекта по этому вопросу ничего не нашлось. Отвечай по данным инструментов — вызови их, если нужно. Если данных инструментов не хватает, так и скажи, не придумывай."
	NoDocsSource = "Источник: в документации не нашлось — ответ по инструментам и разговору."
)

// taskMemo — рабочий слой памяти строками «ключ: значение»: с ним rewrite
// раскрывает «а сколько это стоит?» через цель задачи, а не только через
// четыре последние реплики (день 25).
func taskMemo(m *memory.Memory) string {
	var lines []string
	for _, e := range m.Layer(memory.ScopeTask).Entries() {
		lines = append(lines, e.Key+": "+e.Value)
	}
	return strings.Join(lines, "\n")
}

// retrievalLabel — что сделали этапы поиска: «база знаний: 10 кандидатов →
// порог −4 → реранк −3 → 3 фрагм.».
func retrievalLabel(cfg Config, res *rag.Result) string {
	o := cfg.ragOptions()
	if o.Candidates <= o.TopK && o.MinScore == 0 && !o.Rerank {
		return fmt.Sprintf("база знаний: %d фрагм.", len(res.Kept))
	}
	parts := []string{fmt.Sprintf("база знаний: %d кандидатов", len(res.Candidates))}
	if o.MinScore > 0 {
		parts = append(parts, fmt.Sprintf("порог %.2f −%d", o.MinScore, res.BelowScore))
	}
	if o.Rerank {
		r := "реранк"
		if o.MinRerank > 0 {
			r += fmt.Sprintf(" ≥%.2f −%d", o.MinRerank, res.BelowRerank)
		}
		parts = append(parts, r)
	}
	parts = append(parts, fmt.Sprintf("%d фрагм.", len(res.Kept)))
	return strings.Join(parts, " → ")
}

// HitTrace — найденное по строке на фрагмент: косинус, оценка реранкера
// и место до него, id, раздел.
func HitTrace(hits []rag.Hit) string {
	lines := make([]string, len(hits))
	for i, h := range hits {
		s := fmt.Sprintf("%.3f", h.Score)
		if h.Rerank > 0 {
			s += fmt.Sprintf(" · реранк %.2f", h.Rerank)
			if h.Rank != i+1 {
				s += fmt.Sprintf(" (был %d-м)", h.Rank)
			}
		}
		s += " " + h.ID
		if len(h.Parts) > 1 {
			s += fmt.Sprintf(" (+%d части раздела)", len(h.Parts)-1)
		}
		if h.Section != "" {
			s += " · " + h.Section
		}
		lines[i] = s
	}
	return strings.Join(lines, "\n")
}

// augment — вопрос вместе с фрагментами: то, что уйдёт в модель вместо
// голого вопроса. Номер фрагмента — чтобы на него можно было сослаться.
func augment(question string, hits []rag.Hit) string {
	if len(hits) == 0 {
		return question
	}
	var b strings.Builder
	b.WriteString("Фрагменты базы знаний, найденные по вопросу (ближайшие — первыми):\n\n")
	for i, h := range hits {
		fmt.Fprintf(&b, "[%d] %s — %s\n%s\n\n", i+1, h.Source, h.Header(), strings.TrimSpace(h.Text))
	}
	b.WriteString("---\nОтветь на вопрос по этим фрагментам. Если ответа в них нет — так и скажи, не придумывай.\n\nВопрос: ")
	b.WriteString(question)
	return b.String()
}

// RAGLabel — подпись режима в панели.
func RAGLabel(mode string) string {
	if mode == "" {
		return "выкл"
	}
	return "вкл"
}

// RAGHint — подсказка режима в панели.
func RAGHint(mode string) string {
	if mode == "" {
		return "модель отвечает тем, что знает сама; про стенд она не знает ничего"
	}
	return "перед ответом агент ищет фрагменты в базе знаний и отправляет их вместе с вопросом"
}

// ragOptions — как отбирать фрагменты по конфигу.
func (c Config) ragOptions() rag.Options {
	o := rag.Options{TopK: c.ragTopK(), Rerank: c.RAGRerank, Expand: c.RAGExpand}
	if c.RAGCandidates != nil {
		o.Candidates = *c.RAGCandidates
	}
	if c.RAGMinScore != nil {
		o.MinScore = float32(*c.RAGMinScore)
	}
	if c.RAGMinRerank != nil {
		o.MinRerank = float32(*c.RAGMinRerank)
	}
	return o
}

// RAGSummary — подпись режима поиска для шапки и отчётов.
func RAGSummary(c Config) string {
	o := c.ragOptions()
	s := fmt.Sprintf("RAG: %d фрагм.", o.TopK)
	if o.Candidates > o.TopK {
		s += fmt.Sprintf(" из %d", o.Candidates)
	}
	if o.MinScore > 0 {
		s += fmt.Sprintf(", cos ≥ %.2f", o.MinScore)
	}
	if o.Rerank {
		s += ", реранк"
		if o.MinRerank > 0 {
			s += fmt.Sprintf(" ≥ %.2f", o.MinRerank)
		}
	}
	if c.RAGRewrite {
		s += ", rewrite"
	}
	if c.RAGExpand {
		s += ", раздел целиком"
	}
	if c.RAGCite {
		s += ", цитаты"
	}
	return s
}

// rewriteSystem — инструкция служебного вызова rewrite (день 23).
//
// Вопрос пользователя бывает плохим поисковым запросом: в нём есть отсылки
// («а сколько это стоило?»), которых нет ни в одном документе. Их модель,
// которая видит разговор, раскрыть может, а эмбеддинг — нет.
//
// Первая версия инструкции просила «добавить термины» и перечисляла темы
// базы — и модель стала дописывать эти темы в каждый запрос, выдумывать
// факты («номинал 100 единиц» вместо 10) и путать смысл («стоят одинаково» —
// «одинаковое время запуска»). Поиск от этого стал хуже, чем по самому
// вопросу. Поэтому теперь главное правило — ничего не добавлять.
const rewriteSystem = `Ты переписываешь вопрос пользователя в поисковый запрос к базе знаний — заметкам учебного проекта по работе с LLM.

Верни одну строку — запрос, без пояснений и кавычек. Не отвечай на вопрос.
- Сохрани смысл вопроса и все его ключевые слова.
- Раскрой отсылки к прошлым репликам: «это», «там», «а во втором случае» — назови прямо, о чём речь.
- Если вопрос продолжает разговор, а в нём не названо, о чём речь (какой день, сервер, замер), — добавь это из памяти задачи или прошлых реплик. Только предмет вопроса: цель задачи, ограничения и пожелания пользователя в запрос не переноси — по ним в базе ищется не то.
- Можно добавить один-два синонима к ключевому слову, если уверен в них.
- Не добавляй ни фактов, ни чисел, ни тем, которых нет в вопросе и в прошлых репликах.
- Если вопрос понятен сам по себе, верни его почти без изменений.
- Если сообщение не требует поиска в базе — оно о самом разговоре («напомни цель», «что мы уже решили», «какие ограничения я назвал»), приветствие или благодарность, — верни ровно NONE.`

// RewriteQuery — служебный вызов: вопрос → поисковый запрос. Общий для
// агента и для команды сравнения режимов поиска.
func RewriteQuery(ctx context.Context, client llm.Provider, model, memo string, hist []llm.Message, text string) (string, llm.Request, *llm.Response, error) {
	return rewriteWithTools(ctx, client, model, memo, hist, text, nil)
}

// rewriteToolsRoute — роутер перед rewrite, когда у хода есть инструменты
// (день 31). Только тогда: агентам без инструментов маршрут TOOLS не
// нужен, и замеры дней 23–25 остаются как были.
//
// Первая версия была ещё одним пунктом в конце списка правил rewrite,
// и модель его пропускала: «что сделано на этой ветке?» переписывалось
// в запрос к базе. Решение о маршруте стоит первым и с примерами.
const rewriteToolsRoute = `Сначала реши, куда идёт вопрос.
У ассистента есть инструменты — они показывают текущее состояние:
%s
Если ответ на вопрос — это текущее состояние, которое показывают инструменты (что сейчас, что изменилось, какие файлы есть или изменены, какая ветка, последние коммиты), верни ровно TOOLS и больше ничего.
Если вопрос о том, как что устроено, зачем сделано, что написано в документации, — переписывай его в запрос к базе по правилам ниже.
Примеры: «что сделано на этой ветке?» → TOOLS; «какие файлы я поменял?» → TOOLS; «на какой мы ветке?» → TOOLS; «как устроен поиск по базе?» → запрос к базе; «зачем флаг -handoff?» → запрос к базе.

`

func rewriteWithTools(ctx context.Context, client llm.Provider, model, memo string, hist []llm.Message, text string, funcs []llm.Tool) (string, llm.Request, *llm.Response, error) {
	system := rewriteSystem
	if len(funcs) > 0 {
		var t strings.Builder
		for _, f := range funcs {
			fmt.Fprintf(&t, "- %s: %s\n", displayName(f.Function.Name), clipRunes(f.Function.Description, 160))
		}
		system = fmt.Sprintf(rewriteToolsRoute, strings.TrimRight(t.String(), "\n")) + rewriteSystem
	}
	var b strings.Builder
	if memo != "" {
		b.WriteString("Память задачи:\n" + memo + "\n\n")
	}
	if tail := lastMessages(hist, 4); len(tail) > 0 {
		b.WriteString("Предыдущие реплики:\n")
		for _, m := range tail {
			who := "пользователь"
			if m.Role == llm.RoleAssistant {
				who = "ассистент"
			}
			fmt.Fprintf(&b, "%s: %s\n", who, clipRunes(oneLineText(m.Content), 300))
		}
		b.WriteString("\n")
	}
	b.WriteString("Вопрос: " + text)
	req := llm.Request{
		Model: model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: system},
			{Role: llm.RoleUser, Content: b.String()},
		},
		Temperature: llm.F(0),
		Thinking:    &llm.Thinking{Type: "disabled"},
		MaxTokens:   llm.I(120),
	}
	resp, err := client.Chat(ctx, req)
	if err != nil {
		return text, req, resp, err
	}
	q := strings.Trim(strings.TrimSpace(oneLineText(resp.Content)), "«»\".")
	if q == "" {
		q = text
	}
	if strings.EqualFold(q, NoSearch) {
		q = NoSearch
	}
	if len(funcs) > 0 && strings.EqualFold(q, ToolsRoute) {
		q = ToolsRoute
	}
	return q, req, resp, nil
}

// rewriteQuery — то же внутри хода: в журнал, в учёт и в ленту. Сбой
// не ломает ход — ищем по самому вопросу.
func (a *Agent) rewriteQuery(ctx context.Context, cfg Config, memo string, hist []llm.Message, text string, funcs []llm.Tool, turn *Turn, on func(Event)) string {
	q, req, resp, err := rewriteWithTools(ctx, a.client, cfg.Model, memo, hist, text, funcs)
	a.record(req, resp, err, "переписать запрос", true)
	a.accountAux(resp, err)
	if err != nil {
		on(Event{Kind: EventRetrieval, Label: "переписать запрос не удалось — ищем по вопросу", Content: err.Error(), Failed: true})
		return text
	}
	turn.AuxCalls++
	turn.AuxPrompt += resp.Usage.PromptTokens
	turn.AuxCompletion += resp.Usage.CompletionTokens
	on(Event{Kind: EventRetrieval, Label: "запрос к базе", Content: q, Usage: resp.Usage, Latency: resp.Latency})
	return q
}

func lastMessages(h []llm.Message, n int) []llm.Message {
	if len(h) > n {
		return h[len(h)-n:]
	}
	return h
}

func oneLineText(s string) string { return strings.Join(strings.Fields(s), " ") }

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
