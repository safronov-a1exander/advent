package main

// День 1 — первый запрос к LLM через API.
//   advent ask "вопрос"   минимальный путь: запрос → ответ → консоль
//   advent chat           тот же вызов, но в TUI с потоковым выводом
//   advent demo -script   тот же TUI, но управляемый сценарием (для видео)

import (
	"context"
	"flag"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/safronov-a1exander/advent/internal/agent"
	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/invariant"
	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/memory"
	"github.com/safronov-a1exander/advent/internal/profile"
	"github.com/safronov-a1exander/advent/internal/store"
	"github.com/safronov-a1exander/advent/internal/task"
	"github.com/safronov-a1exander/advent/internal/tui"
)

// Сквозной проект стенда — «Бюджет», ассистент по личным тратам.
// Один домен на все шаги: шаг 1 просто спрашивает, шаг 2 достаёт из
// выписки структуру, шаги 3–5 считают по ней и сравнивают модели.
const defaultSystem = "Ты ассистент по личным финансам. Отвечай по-русски, кратко и по делу. " +
	"Суммы указывай в рублях без разделителей разрядов."

type askFlags struct {
	*commonFlags
	model       string
	system      string
	temperature float64
	maxTokens   int
	stream      bool
	raw         bool
	runID       string

	// contextLimit — стартовый лимит контекста агента (день 8); 0 — нет.
	contextLimit int
	// thinking — стартовый режим рассуждений: "", enabled или disabled.
	thinking string
	// contextStrategy, keepLast, summarizeEvery — стратегия контекста (день 9).
	contextStrategy string
	keepLast        int
	summarizeEvery  int

	// Инварианты (день 14).
	invariants    string
	invariantSet  string
	invariantsDir string

	// Состояние задачи (день 13).
	taskState string
	tasksDir  string

	// Профиль пользователя (день 12).
	profileID  string
	profileDir string

	// Модель памяти (день 11): режим раскладки и ключи хранимых слоёв.
	memoryMode string
	memoryUser string
	memoryTask string
	memoryDir  string

	// cfg — загруженный config.yaml; заполняется в setup.
	cfg *config.Config
}

func bindAsk(fs *flag.FlagSet) *askFlags {
	a := &askFlags{commonFlags: bindCommon(fs)}
	fs.StringVar(&a.model, "model", "", "модель (по умолчанию из config.yaml)")
	fs.StringVar(&a.system, "system", defaultSystem, "системный промпт")
	fs.Float64Var(&a.temperature, "temperature", -1, "температура (-1 = не передавать)")
	fs.IntVar(&a.maxTokens, "max-tokens", 0, "лимит токенов ответа (0 = не передавать)")
	fs.BoolVar(&a.stream, "stream", true, "потоковый вывод")
	fs.BoolVar(&a.raw, "raw", false, "печатать только текст ответа, без метрик")
	fs.StringVar(&a.runID, "run", "", "имя файла журнала в runs/ (по умолчанию с меткой времени)")
	fs.IntVar(&a.contextLimit, "context-limit", 0, "лимит контекста агента в токенах для chat/demo (0 — только окно модели)")
	fs.StringVar(&a.thinking, "thinking", "", "режим рассуждений для chat/demo: enabled | disabled (пусто — как у модели)")
	fs.StringVar(&a.contextStrategy, "context", "", "стратегия контекста для chat/demo: пусто — вся история, window — последние N, facts — факты + последние N, summary — сводка + хвост")
	fs.IntVar(&a.keepLast, "keep-last", 0, "для window/facts/summary: сколько последних сообщений идёт как есть (0 — по умолчанию)")
	fs.IntVar(&a.summarizeEvery, "summarize-every", 0, "для summary: сжимать, когда за хвостом накопилось столько сообщений (0 — по умолчанию)")
	fs.StringVar(&a.invariants, "invariants", "", "инварианты для chat/demo: пусто — нет, prompt — только в промпте, check — плюс проверка кодом, judge — плюс внешняя модель")
	fs.StringVar(&a.invariantSet, "invariant-set", "", "набор правил: имя файла из invariants/ без расширения")
	fs.StringVar(&a.invariantsDir, "invariants-dir", "", "каталог наборов инвариантов (по умолчанию invariants_dir из config.yaml)")
	fs.StringVar(&a.taskState, "task-state", "", "состояние задачи для chat/demo: пусто — без стадий, manual — двигает пользователь, auto — плюс служебный вызов")
	fs.StringVar(&a.tasksDir, "tasks-dir", "", "каталог состояний задач (по умолчанию tasks_dir из config.yaml)")
	fs.StringVar(&a.profileID, "profile", "", "профиль пользователя для chat/demo: имя файла из profiles/ без расширения")
	fs.StringVar(&a.profileDir, "profiles-dir", "", "каталог профилей (по умолчанию profiles_dir из config.yaml)")
	fs.StringVar(&a.memoryMode, "memory", "", "слои памяти для chat/demo: пусто — выключены, manual — кладёт пользователь, auto — плюс раскладка агентом")
	fs.StringVar(&a.memoryUser, "user", "", "чей долговременный слой памяти")
	fs.StringVar(&a.memoryTask, "task", "", "какой задачи рабочий слой памяти")
	fs.StringVar(&a.memoryDir, "memory-dir", "", "каталог слоёв памяти (по умолчанию memory_dir из config.yaml)")
	return a
}

func (a *askFlags) setup() (*llm.Client, *config.Provider, *store.Writer, llm.Request, error) {
	cfg, err := config.Load(a.dir)
	if err != nil {
		return nil, nil, nil, llm.Request{}, err
	}
	client, prov, err := cfg.Client(a.provider)
	if err != nil {
		return nil, nil, nil, llm.Request{}, err
	}
	a.cfg = cfg
	model := a.model
	if model == "" {
		model = prov.DefaultMod
	}
	if model == "" {
		return nil, nil, nil, llm.Request{}, fmt.Errorf("не задана модель: укажи -model или default_model в config.yaml")
	}

	runID := a.runID
	if runID == "" {
		runID = store.NewRunID("ask")
	}
	w, err := store.NewWriter(cfg.RunsDir, runID)
	if err != nil {
		return nil, nil, nil, llm.Request{}, err
	}

	req := llm.Request{Model: model}
	if a.temperature >= 0 {
		req.Temperature = llm.F(a.temperature)
	}
	if a.maxTokens > 0 {
		req.MaxTokens = llm.I(a.maxTokens)
	}
	return client, prov, w, req, nil
}

// invariantsStoreDir — каталог наборов инвариантов: флаг важнее config.yaml.
func (a *askFlags) invariantsStoreDir() string {
	if a.invariantsDir != "" {
		return a.invariantsDir
	}
	return a.cfg.InvariantsDir
}

// tasksStoreDir — каталог состояний задач: флаг важнее config.yaml.
func (a *askFlags) tasksStoreDir() string {
	if a.tasksDir != "" {
		return a.tasksDir
	}
	return a.cfg.TasksDir
}

// profilesStoreDir — каталог профилей: флаг важнее config.yaml.
func (a *askFlags) profilesStoreDir() string {
	if a.profileDir != "" {
		return a.profileDir
	}
	return a.cfg.ProfilesDir
}

// tiersOf — каталог провайдера в том виде, в каком его понимает дорога
// профиля: только id и класс.
func tiersOf(models []llm.ModelInfo) []agent.ModelTier {
	out := make([]agent.ModelTier, 0, len(models))
	for _, m := range models {
		out = append(out, agent.ModelTier{ID: m.ID, Tier: m.Tier})
	}
	return out
}

// memoryStoreDir — каталог слоёв памяти: флаг важнее config.yaml.
func (a *askFlags) memoryStoreDir() string {
	if a.memoryDir != "" {
		return a.memoryDir
	}
	return a.cfg.MemoryDir
}

func cmdAsk(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	a := bindAsk(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		return fmt.Errorf("нечего спрашивать: advent ask \"твой вопрос\"")
	}

	client, prov, w, req, err := a.setup()
	if err != nil {
		return err
	}
	defer w.Close()

	if a.system != "" {
		req.Messages = append(req.Messages, llm.Message{Role: llm.RoleSystem, Content: a.system})
	}
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleUser, Content: prompt})

	if !a.raw {
		fmt.Printf("→ %s / %s\n", prov.Name, req.Model)
		fmt.Printf("→ %s\n\n", prompt)
	}

	var resp *llm.Response
	if a.stream {
		resp, err = client.ChatStream(ctx, req, func(c llm.Chunk) error {
			if !c.Done {
				fmt.Print(c.Content)
			}
			return nil
		})
	} else {
		resp, err = client.Chat(ctx, req)
		if err == nil {
			fmt.Print(resp.Content)
		}
	}

	rec := store.Record{Provider: prov.Name, Model: req.Model, Params: store.ParamsOf(req), Messages: req.Messages}
	if err != nil {
		rec.Error = err.Error()
		_ = w.Append(rec)
		return err
	}
	rec.Content, rec.Reasoning, rec.Finish = resp.Content, resp.Reasoning, resp.FinishReason
	rec.Usage, rec.LatencyMS, rec.CostUSD = resp.Usage, resp.Latency.Milliseconds(), resp.CostUSD
	_ = w.Append(rec)

	fmt.Println()
	if !a.raw {
		fmt.Printf("\nfinish_reason : %s\n", resp.FinishReason)
		fmt.Printf("latency       : %s\n", resp.Latency.Round(time.Millisecond))
		fmt.Printf("токены        : prompt %d (кэш %d) · completion %d · reasoning %d · всего %d\n",
			resp.Usage.PromptTokens, resp.Usage.CachedPromptTokens,
			resp.Usage.CompletionTokens, resp.Usage.ReasoningTokens, resp.Usage.TotalTokens)
		fmt.Printf("стоимость     : $%.6f (по прайсу из config.yaml)\n", resp.CostUSD)
		fmt.Printf("журнал        : %s\n", w.Path())
	}
	return nil
}

func cmdChat(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	a := bindAsk(fs)
	sf := bindSessions(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runTUI(ctx, a, sf, nil, "")
}

func cmdDemo(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	a := bindAsk(fs)
	sf := bindSessions(fs)
	script := fs.String("script", "", "файл сценария (.demo)")
	title := fs.String("title", "", "заголовок экрана")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *script == "" {
		return fmt.Errorf("укажи -script scripts/day01.demo")
	}
	acts, err := tui.ParseDemo(*script)
	if err != nil {
		return err
	}
	return runTUI(ctx, a, sf, acts, *title)
}

func runTUI(ctx context.Context, a *askFlags, sf *sessionFlags, acts []tui.Action, title string) error {
	client, prov, w, req, err := a.setup()
	if err != nil {
		return err
	}
	defer w.Close()

	// Флаги задают лишь стартовые значения — дальше всё крутится в панели.
	set := tui.NewSettings(prov.Models, req.Model, a.system)
	set.Stream = a.stream
	if a.contextLimit > 0 {
		set.ContextLimit = llm.I(a.contextLimit)
	}
	switch a.thinking {
	case "", "enabled", "disabled":
		set.Thinking = a.thinking
	default:
		return fmt.Errorf("-thinking: ожидали enabled или disabled, получили %q", a.thinking)
	}
	if !slices.Contains(agent.ContextStrategies, a.contextStrategy) {
		return fmt.Errorf("-context: ожидали пусто, window, facts или summary, получили %q", a.contextStrategy)
	}
	set.Context = a.contextStrategy
	if a.keepLast > 0 {
		set.KeepLast = llm.I(a.keepLast)
	}
	if a.summarizeEvery > 0 {
		set.SummarizeEvery = llm.I(a.summarizeEvery)
	}
	if !slices.Contains(agent.MemoryModes, a.memoryMode) {
		return fmt.Errorf("-memory: ожидали пусто, manual или auto, получили %q", a.memoryMode)
	}
	set.Memory = a.memoryMode
	set.User = a.memoryUser
	set.Task = a.memoryTask

	// Экран не ходит в API сам: он говорит с агентами из пула, а пул
	// пишет каждый вызов в тот же журнал runs/*.jsonl.
	pool := agent.NewPool(client, prov.Name, w)
	opts := tui.Options{
		Pool:     pool,
		Provider: prov.Name,
		Settings: set,
		Title:    title,
		Resume:   sf.resume,
		Fresh:    sf.fresh,
	}
	// День 12: профили лежат в репозитории и читаются при каждом обращении,
	// поэтому правка файла действует со следующего запроса.
	profiles := profile.NewFileStore(a.profilesStoreDir())
	pool.SetProfileStore(profiles)
	pool.SetCatalog(tiersOf(prov.Models))
	if ids, err := profiles.List(); err == nil {
		set.Profiles = ids
	}
	if a.profileID != "" {
		if _, err := profiles.Load(a.profileID); err != nil {
			return fmt.Errorf("-profile: %w", err)
		}
	}
	set.Profile = a.profileID

	// День 13: состояние задачи переживает и разговор, и перезапуск —
	// в этом весь смысл паузы и продолжения.
	if !slices.Contains(agent.TaskModes, a.taskState) {
		return fmt.Errorf("-task-state: ожидали пусто, manual или auto, получили %q", a.taskState)
	}
	set.TaskState = a.taskState
	pool.SetTaskStore(task.NewFileStore(a.tasksStoreDir()))

	// День 14: инварианты — рамки проекта; они в репозитории рядом с кодом.
	if !slices.Contains(agent.InvariantModes, a.invariants) {
		return fmt.Errorf("-invariants: ожидали пусто, prompt, check или judge, получили %q", a.invariants)
	}
	invs := invariant.NewFileStore(a.invariantsStoreDir())
	pool.SetInvariantStore(invs)
	if ids, err := invs.List(); err == nil {
		set.InvariantIDs = ids
	}
	if a.invariantSet != "" {
		if _, err := invs.Load(a.invariantSet); err != nil {
			return fmt.Errorf("-invariant-set: %w", err)
		}
	}
	set.Invariants = a.invariants
	set.InvariantSet = a.invariantSet

	// День 11: слои задачи и пользователя переживают и разговор, и перезапуск,
	// поэтому лежат в своём каталоге, а не в файле разговора. Включаются
	// всегда: агент без памяти их просто не заведёт.
	pool.SetMemoryStore(memory.NewFileStore(a.memoryStoreDir()))

	// День 7: разговоры переживают перезапуск. Пул сохраняет агентов
	// в каталог сессий и при старте поднимает всех обратно.
	if !sf.noSave {
		pool.SetStore(agent.NewFileStore(sf.dir(a.cfg)))
		if _, err := pool.Restore(); err != nil {
			opts.Notice = "часть сохранённых разговоров не прочиталась: " + err.Error()
		}
	}
	m := tui.NewModel(opts)
	p := tea.NewProgram(m, tea.WithContext(ctx))

	if len(acts) > 0 {
		go tui.RunDemo(p, m, acts)
	}
	if _, err := p.Run(); err != nil {
		return err
	}
	// Путь журнала не печатаем: после выхода из TUI строка повисает
	// в пустом терминале и попадает в запись. Он и так виден в интерфейсе.
	return nil
}
