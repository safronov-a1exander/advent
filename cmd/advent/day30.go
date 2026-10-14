package main

// День 30 — локальная модель как приватный сервис.
//   advent serve -keys keys.txt -listen 0.0.0.0:8095     шлюз перед llama-server
//   advent load  -provider private -key2 <ключ>           проверка сервиса по сети
//
// llama-server слушает только 127.0.0.1. Наружу смотрит шлюз
// (internal/gateway): ключи, лимит запросов на ключ, очередь к модели,
// предел контекста и страница чата. API — тот же OpenAI-совместимый,
// поэтому клиент стенда подключается к сервису провайдером private.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/gateway"
)

func cmdServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "0.0.0.0:8095", "адрес сервиса; 0.0.0.0 — все сетевые интерфейсы")
	upstream := fs.String("upstream", "http://127.0.0.1:8080", "llama-server, к которому ходит шлюз")
	model := fs.String("model", "qwen3-4b", "модель, которую шлюз подставляет в каждый запрос")
	keysPath := fs.String("keys", "keys.txt", "файл ключей: строка «имя ключ»; лежит вне репозитория")
	newKey := fs.String("new-key", "", "выдать ключ для этого имени, дописать в файл ключей и выйти")
	rate := fs.Int("rate", 30, "запросов в минуту на ключ")
	burst := fs.Int("burst", 5, "запросов подряд, пока ведро полное")
	parallel := fs.Int("parallel", 2, "запросов в модели одновременно — по числу слотов llama-server (-np)")
	queue := fs.Int("queue", 4, "сколько запросов ждут слота; сверх — 503")
	maxCtx := fs.Int("max-context", 4096, "окно на запрос: промпт + ответ, в токенах (у llama-server — -c / -np)")
	maxTok := fs.Int("max-tokens", 1024, "потолок ответа в токенах")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *newKey != "" {
		key, err := addKey(*keysPath, *newKey)
		if err != nil {
			return err
		}
		fmt.Printf("ключ для %s: %s\n(дописан в %s)\n", *newKey, key, *keysPath)
		return nil
	}
	keys, err := loadKeys(*keysPath)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return fmt.Errorf("в %s нет ни одного ключа — выдай: advent serve -new-key <имя>", *keysPath)
	}

	g := gateway.New(gateway.Config{
		Upstream: *upstream, Model: *model, Keys: keys,
		RatePerMin: *rate, Burst: *burst, Parallel: *parallel, Queue: *queue,
		MaxContext: *maxCtx, MaxTokens: *maxTok, Log: os.Stdout,
	})
	srv := &http.Server{Addr: *listen, Handler: g, ReadHeaderTimeout: 10 * time.Second}

	fmt.Printf("сервис  : %s → модель %s на %s\n", *listen, *model, *upstream)
	fmt.Printf("ключей  : %d (%s)\n", len(keys), *keysPath)
	fmt.Printf("лимиты  : %d/мин на ключ (подряд %d) · в модели %d, очередь %d · окно %d, ответ ≤%d\n",
		*rate, *burst, *parallel, *queue, *maxCtx, *maxTok)
	for _, a := range lanAddrs(*listen) {
		fmt.Printf("открыть : http://%s/\n", a)
	}
	fmt.Println()

	// IPv4-адрес слушаем как tcp4. Иначе Go садится на двойной стек [::],
	// и Windows пускает его даже на порт, который на 0.0.0.0 уже занят
	// чужим процессом, — запросы на IPv4 тогда уходят не к нам.
	network := "tcp"
	if host, _, err := net.SplitHostPort(*listen); err == nil {
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			network = "tcp4"
		}
	}
	ln, err := net.Listen(network, *listen)
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	case err := <-errc:
		return err
	}
}

// loadKeys читает «имя ключ» построчно; # — комментарий.
func loadKeys(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	keys := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return nil, fmt.Errorf("%s:%d: ждал «имя ключ»", path, n)
		}
		keys[parts[1]] = parts[0]
	}
	return keys, sc.Err()
}

func addKey(path, name string) (string, error) {
	if strings.ContainsAny(name, " \t") {
		return "", fmt.Errorf("имя без пробелов")
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key := "sk-local-" + hex.EncodeToString(b)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s %s\n", name, key)
	return key, err
}

// lanAddrs — адреса, по которым сервис видно из сети: для 0.0.0.0 —
// IPv4 всех поднятых интерфейсов, кроме loopback.
func lanAddrs(listen string) []string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return nil
	}
	if host != "" && host != "0.0.0.0" {
		return []string{listen}
	}
	var out []string
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if ip, ok := a.(*net.IPNet); ok && ip.IP.To4() != nil {
				out = append(out, net.JoinHostPort(ip.IP.String(), port))
			}
		}
	}
	return out
}

// ---- load: проверка сервиса так, как его видит клиент ----

type loadClient struct {
	base string // без /v1
	http *http.Client
}

type shot struct {
	who    string
	code   int
	took   time.Duration
	answer string
	note   string
}

func (c *loadClient) ask(ctx context.Context, key, who, question string, maxTokens int) shot {
	body, _ := json.Marshal(map[string]any{
		"messages":             []map[string]string{{"role": "user", "content": question}},
		"max_tokens":           maxTokens,
		"temperature":          0,
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	})
	start := time.Now()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := c.http.Do(req)
	s := shot{who: who}
	if err != nil {
		s.note = err.Error()
		s.took = time.Since(start)
		return s
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	s.code, s.took = resp.StatusCode, time.Since(start)
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Choices) > 0 {
		s.answer = strings.Join(strings.Fields(out.Choices[0].Message.Content), " ")
	}
	s.note = out.Error.Message
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		s.note += " · Retry-After " + ra + " с"
	}
	return s
}

func (c *loadClient) get(ctx context.Context, path, key string) (int, []byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

// burst — n запросов одновременно, ключи по очереди.
func (c *loadClient) burst(ctx context.Context, n int, keys []string, names []string, q string, maxTokens int) []shot {
	out := make([]shot, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i] = c.ask(ctx, keys[i%len(keys)], names[i%len(names)], q, maxTokens)
		}(i)
		time.Sleep(20 * time.Millisecond) // порядок прихода = номер в таблице
	}
	wg.Wait()
	return out
}

func printShots(shots []shot) {
	for i, s := range shots {
		code := "—"
		if s.code != 0 {
			code = fmt.Sprint(s.code)
		}
		text := s.answer
		if text == "" {
			text = s.note
		}
		fmt.Printf("    %2d  %-5s %s  %5.1f с  %s\n", i+1, s.who, code, s.took.Seconds(), trunc(text, 70))
	}
}

func countCodes(shots []shot) string {
	m := map[int]int{}
	for _, s := range shots {
		m[s.code]++
	}
	codes := make([]int, 0, len(m))
	for c := range m {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	var parts []string
	for _, c := range codes {
		parts = append(parts, fmt.Sprintf("%d×%d", m[c], c))
	}
	return strings.Join(parts, ", ")
}

func cmdLoad(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	c := bindCommon(fs)
	addr := fs.String("url", "", "адрес сервиса вместо base_url провайдера: http://192.168.1.10:8095")
	key2 := fs.String("key2", os.Getenv("ADVENT_PRIVATE_KEY2"), "ключ второго пользователя — лимит считается на ключ")
	n := fs.Int("n", 4, "запросов одновременно в проверке стабильности")
	flood := fs.Int("flood", 9, "запросов одновременно в проверке очереди")
	hold := fs.Duration("hold", 0, "подержать итог на экране перед выходом — для записи видео")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if c.provider == "" {
		c.provider = "private"
	}
	cfg, err := config.Load(c.dir)
	if err != nil {
		return err
	}
	prov, err := cfg.Provider(c.provider)
	if err != nil {
		return err
	}
	key1, err := prov.Key()
	if err != nil {
		return err
	}
	keys, names := []string{key1}, []string{"ключ1"}
	if *key2 != "" {
		keys, names = append(keys, *key2), append(names, "ключ2")
	}
	base := prov.BaseURL
	if *addr != "" {
		base = *addr
	}
	base = strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
	cl := &loadClient{base: base, http: &http.Client{Timeout: 5 * time.Minute}}
	// Ответ в пару предложений: запросы должны пересекаться во времени,
	// иначе очередь не успевает набраться.
	q := "Дай один совет, как сократить траты на доставку еды. Два предложения."
	short := "Сколько будет 15% от 60000 рублей? Ответь одним числом."

	// 1. Сеть.
	fmt.Printf("1. Доступ по сети: %s\n", base)
	if u, err := url.Parse(base); err == nil && isLoopback(u.Hostname()) {
		fmt.Println("   адрес — loopback: это та же машина, а не сеть. Для проверки укажи адрес в локальной сети (config.local.yaml).")
	}
	code, body, err := cl.get(ctx, "/health", "")
	if err != nil {
		return fmt.Errorf("сервис недоступен: %w", err)
	}
	fmt.Printf("   /health без ключа          → %d %s\n", code, strings.TrimSpace(string(body)))
	code, _, _ = cl.get(ctx, "/v1/models", "")
	fmt.Printf("   /v1/models без ключа       → %d\n", code)
	code, body, _ = cl.get(ctx, "/v1/models", key1)
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &models)
	var ids []string
	for _, m := range models.Data {
		ids = append(ids, m.ID)
	}
	fmt.Printf("   /v1/models с ключом        → %d %s\n", code, strings.Join(ids, ", "))
	s := cl.ask(ctx, "sk-local-неверный", "чужой", short, 16)
	fmt.Printf("   чат с неверным ключом      → %d %s\n\n", s.code, s.note)

	// 2. Стабильность: несколько запросов сразу.
	fmt.Printf("2. Стабильность: %d запросов одновременно от %d ключей\n", *n, len(keys))
	start := time.Now()
	shots := cl.burst(ctx, *n, keys, names, q, 120)
	printShots(shots)
	fmt.Printf("   итог: %s за %.1f с\n\n", countCodes(shots), time.Since(start).Seconds())

	// 3. Очередь: больше, чем модель и очередь вмещают.
	fmt.Printf("3. Очередь: %d запросов одновременно\n", *flood)
	shots = cl.burst(ctx, *flood, keys, names, q, 120)
	printShots(shots)
	fmt.Printf("   итог: %s\n\n", countCodes(shots))

	// 4. Окно: промпт, который не влезает.
	stmt, _ := os.ReadFile("data/statement.txt")
	long := strings.Repeat(string(stmt), 12) + "\nСколько всего потрачено?"
	fmt.Println("4. Предел контекста: выписка, повторённая 12 раз")
	s = cl.ask(ctx, key1, names[0], long, 256)
	fmt.Printf("   → %d %s\n\n", s.code, s.note)

	// 5. Лимит: подряд с одного ключа, пока не откажут.
	fmt.Println("5. Лимит запросов: подряд с одного ключа")
	for i := 1; i <= 30; i++ {
		s = cl.ask(ctx, key1, names[0], short, 1)
		if s.code != http.StatusOK {
			fmt.Printf("   запрос %d → %d %s\n", i, s.code, s.note)
			break
		}
		fmt.Printf("   запрос %d → %d\n", i, s.code)
	}
	if len(keys) > 1 {
		s = cl.ask(ctx, keys[1], names[1], short, 16)
		fmt.Printf("   тут же с другого ключа → %d %s\n", s.code, s.answer)
	}

	if code, body, err := cl.get(ctx, "/v1/stats", key1); err == nil && code == 200 {
		var st struct {
			Responses map[string]int `json:"responses"`
		}
		_ = json.Unmarshal(body, &st)
		codes := make([]string, 0, len(st.Responses))
		for k := range st.Responses {
			codes = append(codes, k)
		}
		sort.Strings(codes)
		var parts []string
		for _, k := range codes {
			parts = append(parts, fmt.Sprintf("%s: %d", k, st.Responses[k]))
		}
		fmt.Printf("\nсервис с запуска ответил — %s\n", strings.Join(parts, " · "))
	}
	if *hold > 0 {
		time.Sleep(*hold)
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
