package main

// День 31 — ассистент разработчика: `advent dev`.
//
// Это тот же чат, что и с первого дня, но собранный под проект: база
// знаний — документация самого стенда (rag.bases.project), MCP — его
// git-репозиторий, а команда /help задаёт вопрос о проекте. Новой машинки
// здесь почти нет: агент, поиск с реранком и цитатами, инструменты —
// всё из прошлых недель. Новое — что они собраны вместе под одну задачу.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/safronov-a1exander/advent/internal/mcp"
	"github.com/safronov-a1exander/advent/internal/servers/gitrepo"
	"github.com/safronov-a1exander/advent/internal/tui"
)

const devSystem = `Ты — ассистент разработчика проекта advent: стенд на Go для экспериментов с LLM по REST API, собранный вокруг ассистента «Бюджет».
Отвечаешь на вопросы о проекте: как он устроен, где что лежит, какие есть команды и флаги, что сделано на каком шаге.
Опирайся на фрагменты документации проекта и на состояние репозитория. Если нужно знать, что сейчас в репозитории, — ветку, изменённые файлы, diff, историю, список файлов, — вызови инструменты git, не угадывай.
Называй пути к файлам и команды точно так, как они написаны в проекте. Отвечай по-русски, коротко и по делу.`

const devIntro = `Ассистент разработчика advent:
  /help <вопрос>  вопрос о проекте — поиск по README, docs, конфигу и схемам MCP + состояние git
  /help           показать ветку и последний коммит
  обычная реплика разговор без поиска по документации; инструменты git — по запросу модели`

func cmdDev(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dev", flag.ExitOnError)
	a := bindAsk(fs)
	sf := bindSessions(fs)
	script := fs.String("script", "", "сценарий демо (.demo) — для записи видео")
	title := fs.String("title", "advent · ассистент разработчика", "заголовок экрана")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Значения по умолчанию — под проект; любой флаг можно переопределить.
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if !set["system"] {
		a.system = devSystem
	}
	if !set["kb"] {
		a.kb = "project"
	}
	if !set["mcp"] {
		a.mcp = "git"
	}
	if !set["thinking"] {
		a.thinking = "disabled"
	}
	// Поиск — как в мини-чате дня 25: реранк, rewrite, цитаты с проверкой,
	// раздел целиком. Включается только на /help (см. runTUI).
	a.ragRerank, a.ragRewrite, a.ragCite, a.ragChat = true, true, true, true
	a.devHelp = true

	var acts []tui.Action
	if *script != "" {
		var err error
		if acts, err = tui.ParseDemo(*script); err != nil {
			return err
		}
	}
	return runTUI(ctx, a, sf, acts, *title)
}

// devHelp — /help поверх хаба MCP: состояние репозитория код берёт
// у сервера git тем же вызовом инструмента, что и модель.
func devHelp(hub *mcp.Hub) *tui.Help {
	h := &tui.Help{Intro: devIntro}
	if hub == nil {
		return h
	}
	h.Context = func(ctx context.Context) (string, error) {
		out := hub.Call(ctx, "git"+mcp.Sep+"branch", "{}", []string{"git"})
		if out.IsError {
			return "", fmt.Errorf("%s", strings.TrimSpace(out.Text))
		}
		var b gitrepo.Branch
		if err := json.Unmarshal([]byte(out.Text), &b); err != nil {
			return "", fmt.Errorf("git.branch ответил не JSON: %w", err)
		}
		s := fmt.Sprintf("ветка: %s\nпоследний коммит: %s %s\nнезакоммичено файлов: %d", b.Name, b.Head, b.Subject, b.Dirty)
		return s, nil
	}
	return h
}
