package main

// День 29 — ресурсы локальной модели.
//   advent bench -provider local -scenario scenarios/day29-tuning.yaml -variant template \
//       -models qwen3-4b,qwen3-4b-c4k,qwen3-4b-q8
//
// lab меряет качество и задержку, а bench — то, чего из /chat/completions
// не видно: сколько видеопамяти занимает конфигурация сервера, сколько
// она грузится и с какой скоростью разбирает промпт и пишет ответ.
// Конфигурации — пресеты llama-server в режиме роутера
// (scripts/llama-presets.ini): квантовка и окно задаются при загрузке
// модели, поэтому каждая из них — отдельное имя модели.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/safronov-a1exander/advent/internal/config"
	"github.com/safronov-a1exander/advent/internal/runner"
	"github.com/safronov-a1exander/advent/internal/scenario"
)

type benchRow struct {
	model    string
	label    string
	load     time.Duration
	vramMB   int // -1 — nvidia-smi недоступен
	promptN  int
	promptPS float64
	genN     int
	genPS    float64
	finish   string
	ok       bool
	err      error
}

func cmdBench(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	c := bindCommon(fs)
	scPath := fs.String("scenario", "", "сценарий, из которого берётся запрос")
	variant := fs.String("variant", "", "вариант сценария (по умолчанию первый)")
	models := fs.String("models", "", "модели-пресеты через запятую (по умолчанию все модели провайдера)")
	hold := fs.Duration("hold", 0, "подержать итог на экране перед выходом — для записи видео")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *scPath == "" {
		return fmt.Errorf("укажи -scenario scenarios/....yaml")
	}
	sc, err := scenario.Load(*scPath)
	if err != nil {
		return err
	}
	v := sc.Variants[0]
	if *variant != "" {
		found := false
		for _, x := range sc.Variants {
			if x.ID == *variant {
				v, found = x, true
			}
		}
		if !found {
			return fmt.Errorf("в %s нет варианта %q", *scPath, *variant)
		}
	}

	cfg, err := config.Load(c.dir)
	if err != nil {
		return err
	}
	prov, err := cfg.Provider(c.provider)
	if err != nil {
		return err
	}
	var list []string
	if *models != "" {
		list = strings.Split(*models, ",")
	} else {
		for _, m := range prov.Models {
			list = append(list, m.ID)
		}
	}
	labels := map[string]string{}
	for _, m := range prov.Models {
		labels[m.ID] = m.Label
	}

	body, err := benchRequest(sc, v, prov)
	if err != nil {
		return err
	}
	r := &router{root: strings.TrimSuffix(strings.TrimRight(prov.BaseURL, "/"), "/v1"), http: &http.Client{Timeout: 10 * time.Minute}}
	if _, err := r.status(ctx); err != nil {
		return fmt.Errorf("%s не похож на llama-server в режиме роутера (scripts/local-llm.ps1 -Router): %w", r.root, err)
	}

	fmt.Printf("запрос   : %s / %s\n", sc.Name, v.ID)
	fmt.Printf("сервер   : %s\n\n", r.root)
	var rows []benchRow
	for _, m := range list {
		m = strings.TrimSpace(m)
		fmt.Printf("  → %-16s ", m)
		row := r.bench(ctx, m, body, sc.EffectiveChecks(v))
		row.label = labels[m]
		if row.err != nil {
			fmt.Printf("ошибка: %v\n", row.err)
		} else {
			fmt.Printf("загрузка %.1f с, ответ %d токенов\n", row.load.Seconds(), row.genN)
		}
		rows = append(rows, row)
	}

	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "модель\tконфигурация\tзагрузка, с\tвидеопамять, МБ\tразбор, ток/с\tгенерация, ток/с\tответ\tпроверка\t")
	for _, x := range rows {
		if x.err != nil {
			fmt.Fprintf(tw, "%s\t%s\t\t\t\t\t%s\t\t\n", x.model, x.label, trunc(x.err.Error(), 40))
			continue
		}
		vram := "—"
		if x.vramMB >= 0 {
			vram = strconv.Itoa(x.vramMB)
		}
		mark := "✔"
		if !x.ok {
			mark = "✘"
		}
		fmt.Fprintf(tw, "%s\t%s\t%.1f\t%s\t%.0f\t%.1f\t%d ток · %s\t%s\t\n",
			x.model, x.label, x.load.Seconds(), vram, x.promptPS, x.genPS, x.genN, x.finish, mark)
	}
	tw.Flush()
	fmt.Println("\nвидеопамять — прирост занятой памяти GPU после загрузки модели (nvidia-smi);")
	fmt.Println("разбор и генерация — timings самого llama-server, без сети и клиента.")
	if *hold > 0 {
		time.Sleep(*hold)
	}
	return nil
}

// benchRequest — первый шаг варианта как готовое тело /chat/completions.
func benchRequest(sc *scenario.Scenario, v scenario.Variant, prov *config.Provider) (map[string]any, error) {
	st := v.Steps[0]
	user, err := scenario.Render(st.User, sc.Vars)
	if err != nil {
		return nil, err
	}
	sys := st.System
	if sys == "" {
		sys = v.System
	}
	if sys == "" {
		sys = sc.System
	}
	if sys, err = scenario.Render(sys, sc.Vars); err != nil {
		return nil, err
	}
	var msgs []map[string]string
	if sys != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": sys})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": user})
	body := map[string]any{"messages": msgs}
	p := sc.EffectiveParams(v, st)
	if p.Temperature != nil {
		body["temperature"] = *p.Temperature
	}
	if p.MaxTokens != nil {
		body["max_tokens"] = *p.MaxTokens
	}
	if p.Thinking != "" {
		on := p.Thinking == "enabled"
		if prov.ThinkingVia == "chat_template_kwargs" {
			body["chat_template_kwargs"] = map[string]any{"enable_thinking": on}
		} else {
			body["thinking"] = map[string]string{"type": p.Thinking}
		}
	}
	return body, nil
}

// router — управление моделями llama-server в режиме роутера:
// GET /models, POST /models/load, POST /models/unload.
type router struct {
	root string
	http *http.Client
}

func (r *router) bench(ctx context.Context, model string, body map[string]any, checks scenario.Checks) benchRow {
	row := benchRow{model: model, vramMB: -1}
	// Память меряем от пустой видеокарты: всё, что роутер держал, выгружаем.
	if err := r.unloadAll(ctx); err != nil {
		row.err = err
		return row
	}
	time.Sleep(time.Second)
	before := vramUsedMB()

	start := time.Now()
	if err := r.post(ctx, "/models/load", map[string]string{"model": model}, nil); err != nil {
		row.err = err
		return row
	}
	if err := r.waitStatus(ctx, model, "loaded", 3*time.Minute); err != nil {
		row.err = err
		return row
	}
	row.load = time.Since(start)
	time.Sleep(500 * time.Millisecond)
	if after := vramUsedMB(); before >= 0 && after >= 0 {
		row.vramMB = after - before
	}

	req := map[string]any{"model": model}
	for k, v := range body {
		req[k] = v
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Timings struct {
			PromptN  int     `json:"prompt_n"`
			PromptPS float64 `json:"prompt_per_second"`
			GenN     int     `json:"predicted_n"`
			GenPS    float64 `json:"predicted_per_second"`
		} `json:"timings"`
	}
	if err := r.post(ctx, "/v1/chat/completions", req, &resp); err != nil {
		row.err = err
		return row
	}
	t := resp.Timings
	row.promptN, row.promptPS, row.genN, row.genPS = t.PromptN, t.PromptPS, t.GenN, t.GenPS
	if len(resp.Choices) > 0 {
		ch := resp.Choices[0]
		row.finish = ch.FinishReason
		row.ok = true
		for _, cr := range runner.Verify(checks, ch.Message.Content, ch.FinishReason) {
			row.ok = row.ok && cr.OK
		}
	}
	return row
}

type routerModel struct {
	ID     string `json:"id"`
	Status struct {
		Value string `json:"value"`
	} `json:"status"`
}

func (r *router) status(ctx context.Context) ([]routerModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.root+"/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []routerModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 || out.Data[0].Status.Value == "" {
		return nil, fmt.Errorf("в ответе /models нет статусов моделей")
	}
	return out.Data, nil
}

func (r *router) unloadAll(ctx context.Context) error {
	ms, err := r.status(ctx)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.Status.Value != "unloaded" {
			if err := r.post(ctx, "/models/unload", map[string]string{"model": m.ID}, nil); err != nil {
				return err
			}
			if err := r.waitStatus(ctx, m.ID, "unloaded", time.Minute); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *router) waitStatus(ctx context.Context, model, want string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		ms, err := r.status(ctx)
		if err != nil {
			return err
		}
		for _, m := range ms {
			if m.ID != model {
				continue
			}
			if m.Status.Value == want {
				return nil
			}
			if strings.Contains(m.Status.Value, "fail") {
				return fmt.Errorf("%s: %s — смотри лог роутера", model, m.Status.Value)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("%s: не дождался статуса %s за %s", model, want, limit)
}

func (r *router) post(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.root+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, trunc(string(raw), 200))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// vramUsedMB — занятая память первой видеокарты по nvidia-smi.
// По процессам Windows её не отдаёт (WDDM: N/A), поэтому меряем
// разницу до и после загрузки. -1 — nvidia-smi нет.
func vramUsedMB() int {
	out, err := exec.Command("nvidia-smi", "--query-gpu=memory.used", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return -1
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	n, err := strconv.Atoi(line)
	if err != nil {
		return -1
	}
	return n
}
