package main

// День 33 — ассистент поддержки пользователей: `advent support`.
//
// Продукт — приватная модель дня 30: Qwen3-4B за шлюзом с ключами
// и лимитами. Её пользователи пишут в поддержку «почему не работает
// авторизация?», и ответ на это зависит не только от документации
// (что значит 401), но и от человека (его ключ отозван позавчера, и вот
// почему). Поэтому у ассистента две опоры: FAQ в базе знаний и CRM
// по MCP — обращение, ключи автора, его запросы к шлюзу.
//
// Два входа к одному и тому же агенту: чат в терминале по одному
// обращению (-ticket, для записи демо) и мини-сервис со страницей
// поддержки (-serve): список обращений и чат по каждому.

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/invariant"
	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/mcp"
	"github.com/safronov-a1exander/advent/internal/servers/crm"
	"github.com/safronov-a1exander/advent/internal/tui"
)

const supportSystem = `Ты — первая линия поддержки «Приватной модели»: языковая модель на домашнем сервере, доступная по локальной сети через шлюз с ключами доступа и лимитами.
Ты отвечаешь в чате автору обращения. Ниже — карточка его обращения из CRM.
Прежде чем объяснять причину, проверь факты: ключи автора — в карточке, его запросы к шлюзу — инструментом user_requests. Объясняй, что произошло именно у этого человека, а не что бывает вообще.
Отвечай по шагам: что случилось, почему, что сделать самому, что сделает администратор. Опирайся на фрагменты FAQ и данные CRM, не придумывай.
Если нужен администратор — выдать или вернуть ключ, поменять лимит, починить сервер, — переведи обращение в escalated и оставь в нём заметку для администратора с тем, что выяснил.
Сначала сделай все нужные вызовы инструментов, а ответ пользователю пиши одним сообщением в самом конце, после них: текст рядом с вызовами пользователь не увидит.
Пиши просто, без технического жаргона, на «вы», коротко.`

//go:embed support.html
var supportPage string

func cmdSupport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("support", flag.ExitOnError)
	a := bindAsk(fs)
	sf := bindSessions(fs)
	ticket := fs.String("ticket", "", "чат по обращению в терминале, например T-1042")
	serve := fs.String("serve", "", "мини-сервис со страницей поддержки на адресе, например 127.0.0.1:8096")
	script := fs.String("script", "", "сценарий демо (.demo) — для записи видео")
	title := fs.String("title", "", "заголовок экрана")
	seed := fs.String("crm", "data/support/crm.json", "исходные данные CRM")
	state := fs.String("crm-state", "runs/support/crm.json", "рабочая копия CRM: заметки и статусы")
	if err := fs.Parse(args); err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if !set["kb"] {
		a.kb = "support"
	}
	if !set["mcp"] {
		a.mcp = "crm"
	}
	if !set["thinking"] {
		a.thinking = "disabled"
	}
	if !set["invariants"] {
		a.invariants, a.invariantSet = "check", "поддержка"
	}
	// Поиск — реранк и раздел целиком. Без rewrite: вопрос пользователя
	// и так написан словами, а роутер TOOLS дня 31 отправил бы «почему 401»
	// мимо FAQ — к CRM, хотя нужны оба.
	a.ragRerank, a.ragExpand, a.rag = true, true, true

	switch {
	case *serve != "":
		return serveSupport(ctx, a, *serve, *seed, *state)
	case *ticket == "":
		return errors.New("укажи -ticket T-1042 (чат в терминале) или -serve 127.0.0.1:8096 (страница поддержки)")
	}

	id := strings.ToUpper(*ticket)
	cfg, err := config.Load(a.dir)
	if err != nil {
		return err
	}
	card, err := ticketCard(ctx, cfg, id)
	if err != nil {
		return err
	}
	if !set["system"] {
		a.system = supportSystem
	}
	a.system += "\n\nОбращение (получено из CRM через MCP):\n" + card
	a.toolWrap = func(tb agent.Toolbox) agent.Toolbox { return &ticketScope{Toolbox: tb, ticket: id} }
	if *title == "" {
		*title = "Приватная модель · поддержка · " + id
	}
	var acts []tui.Action
	if *script != "" {
		if acts, err = tui.ParseDemo(*script); err != nil {
			return err
		}
	}
	return runTUI(ctx, a, sf, acts, *title)
}

// ticketCard — карточка обращения тем же инструментом get_ticket, что
// доступен модели: код берёт её до первого вопроса, как ветку в /help
// дня 31. Модели не нужно догадываться, с кем она говорит.
func ticketCard(ctx context.Context, cfg *config.Config, id string) (string, error) {
	hub := mcp.NewHub(cfg.MCPServers)
	defer hub.Close()
	return cardFrom(ctx, hub, id)
}

func cardFrom(ctx context.Context, tb agent.Toolbox, id string) (string, error) {
	if _, err := tb.Functions(ctx, []string{"crm"}); err != nil {
		return "", err
	}
	out := tb.Call(ctx, "crm"+mcp.Sep+"get_ticket", fmt.Sprintf(`{"ticket_id":%q}`, id), []string{"crm"})
	if out.IsError {
		return "", fmt.Errorf("обращение %s: %s", id, strings.TrimSpace(out.Text))
	}
	return out.Text, nil
}

// ticketScope — инструменты CRM только по своему обращению. Ограничение
// в коде, а не в промпте: модель, которую попросили «посмотреть, что
// с ключом брата», получит отказ инструмента, даже если решит помочь.
type ticketScope struct {
	agent.Toolbox
	ticket string
}

func (s *ticketScope) Call(ctx context.Context, fn, args string, allowed []string) mcp.Outcome {
	var a struct {
		Ticket string `json:"ticket_id"`
	}
	if err := json.Unmarshal([]byte(args), &a); err == nil && a.Ticket != "" &&
		!strings.EqualFold(strings.TrimSpace(a.Ticket), s.ticket) {
		return mcp.Outcome{Func: fn, IsError: true,
			Text: fmt.Sprintf("доступ только к обращению %s: данные других обращений и пользователей этой сессии не выдаются", s.ticket)}
	}
	return s.Toolbox.Call(ctx, fn, args, allowed)
}

// supportConfig — конфиг агента сессии поддержки для страницы: то же,
// что набирают флаги терминального чата.
func supportConfig(model, card string) agent.Config {
	return agent.Config{
		Name:          "поддержка",
		Model:         model,
		System:        supportSystem + "\n\nОбращение (получено из CRM через MCP):\n" + card,
		Thinking:      "disabled",
		MCP:           []string{"crm"},
		RAG:           "on",
		RAGRerank:     true,
		RAGCandidates: llm.I(10),
		RAGMinRerank:  llm.F(0.1),
		RAGExpand:     true,
		Invariants:    "check",
		InvariantSet:  "поддержка",
	}
}

// serveSupport — мини-сервис: страница со списком обращений и чат по
// каждому. У каждого обращения свой агент: своя история, свой доступ
// к CRM. Слушает только этот адрес — наружу его никто не выставляет
// (VPS в этом задании нет, см. заметку дня 33).
func serveSupport(ctx context.Context, a *askFlags, addr, seed, state string) error {
	client, prov, w, req, err := a.setup()
	if err != nil {
		return err
	}
	defer w.Close()
	kb, err := knowledgeFor(a.cfg, a.embedder, prov.Name, a.kb)
	if err != nil {
		return err
	}
	hub := mcp.NewHub(a.cfg.MCPServers)
	defer hub.Close()
	invs := invariant.NewFileStore(a.invariantsStoreDir())

	var mu sync.Mutex
	sessions := map[string]*agent.Agent{}
	session := func(ctx context.Context, id string) (*agent.Agent, error) {
		mu.Lock()
		defer mu.Unlock()
		if ag, ok := sessions[id]; ok {
			return ag, nil
		}
		scope := &ticketScope{Toolbox: hub, ticket: id}
		card, err := cardFrom(ctx, scope, id)
		if err != nil {
			return nil, err
		}
		pool := agent.NewPool(client, prov.Name, w)
		pool.SetToolbox(scope)
		pool.SetKnowledge(kb)
		pool.SetInvariantStore(invs)
		ag := pool.SpawnTemp(supportConfig(req.Model, card))
		sessions[id] = ag
		return ag, nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(rw, supportPage)
	})
	mux.HandleFunc("GET /api/tickets", func(rw http.ResponseWriter, r *http.Request) {
		st, err := crm.Open(seed, state, nil)
		if err != nil {
			httpErr(rw, http.StatusInternalServerError, err)
			return
		}
		type row struct{ ID, Subject, Status, Author, Created string }
		var rows []row
		for _, t := range st.Tickets() {
			_, u, _ := st.Ticket(t.ID)
			rows = append(rows, row{t.ID, t.Subject, t.Status, u.Name, t.Created})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		writeJSONResp(rw, rows)
	})
	mux.HandleFunc("POST /api/chat", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Ticket, Text string }
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 1<<16)).Decode(&in); err != nil || strings.TrimSpace(in.Text) == "" {
			httpErr(rw, http.StatusBadRequest, errors.New("нужны ticket и text"))
			return
		}
		ag, err := session(r.Context(), strings.ToUpper(in.Ticket))
		if err != nil {
			httpErr(rw, http.StatusNotFound, err)
			return
		}
		started := time.Now()
		reply, err := ag.Ask(r.Context(), in.Text, nil)
		if errors.Is(err, agent.ErrBusy) {
			httpErr(rw, http.StatusConflict, err)
			return
		}
		if err != nil {
			httpErr(rw, http.StatusBadGateway, err)
			return
		}
		type tool struct {
			Name, Args string
			Failed     bool
		}
		out := struct {
			Answer  string
			Tools   []tool
			Sources []string
			Took    string
		}{Answer: reply.Final.Content, Took: time.Since(started).Round(100 * time.Millisecond).String()}
		for _, t := range reply.Tools {
			out.Tools = append(out.Tools, tool{t.Server + "." + t.Tool, t.Args, t.IsError})
		}
		for _, h := range reply.Sources {
			out.Sources = append(out.Sources, h.Source+" › "+h.Section)
		}
		logLine("%s · %q → %d символов, инструментов %d, %s", in.Ticket, clip(in.Text, 60), len([]rune(out.Answer)), len(out.Tools), out.Took)
		writeJSONResp(rw, out)
	})

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	logLine("поддержка: http://%s/ · модель %s · CRM %s · база %s", ln.Addr(), req.Model, filepath.ToSlash(state), a.kb)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func writeJSONResp(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(rw).Encode(v)
}

func httpErr(rw http.ResponseWriter, code int, err error) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.WriteHeader(code)
	json.NewEncoder(rw).Encode(map[string]string{"error": err.Error()})
}
