package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/safronov-a1exander/advent/internal/llm"
)

func summaryConfig(keep, every int) Config {
	return Config{Model: "m", System: "ты ассистент", Context: ContextSummary,
		KeepLast: llm.I(keep), SummarizeEvery: llm.I(every)}
}

func TestSummaryKeepsTailAndCompressesRest(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(f, "fake", nil).Spawn(summaryConfig(2, 4))

	var compressions int
	onEv := func(e Event) {
		if e.Kind == EventContext {
			compressions++
		}
	}
	ask(t, a, "меня зовут Саша")
	ask(t, a, "бюджет 60000")
	if _, n := a.Summary(); n != 0 {
		t.Fatal("сжали слишком рано: за хвостом ещё нет 4 сообщений")
	}
	// история 4 сообщения, хвост 2 → за хвостом 2, мало
	if _, err := a.Ask(t.Context(), "потрачено 38865", onEv); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ask(t.Context(), "цель отложить 10000", onEv); err != nil {
		t.Fatal(err)
	}
	// перед четвёртым ходом в истории 6 сообщений, хвост 2 → за хвостом 4: сжимаем
	text, covered := a.Summary()
	if compressions != 1 || covered != 4 {
		t.Fatalf("сжатий %d, сводка покрывает %d сообщений", compressions, covered)
	}
	if !strings.Contains(text, "меня зовут Саша") || !strings.Contains(text, "бюджет 60000") {
		t.Fatalf("сводка потеряла факты:\n%s", text)
	}

	// в запрос ушли: system со сводкой, два последних сообщения и вопрос
	req := f.last()
	if len(req.Messages) != 4 {
		t.Fatalf("в запросе %d сообщений, ожидали system + 2 из хвоста + вопрос", len(req.Messages))
	}
	if !strings.Contains(req.Messages[0].Content, "Краткое содержание") || !strings.Contains(req.Messages[0].Content, "меня зовут Саша") {
		t.Fatalf("сводки нет в системном промпте:\n%s", req.Messages[0].Content)
	}
	if req.Messages[1].Content != "потрачено 38865" {
		t.Fatalf("хвост начинается не с того сообщения: %q", req.Messages[1].Content)
	}

	// память не сокращается: вся история на месте
	if n := len(a.History()); n != 8 {
		t.Fatalf("история после сжатия %d сообщений, ожидали все 8", n)
	}
	// и факт из сжатой части по-прежнему доступен модели
	if got := ask(t, a, "как меня зовут?"); got != "тебя зовут Саша" {
		t.Fatalf("после сжатия агент забыл имя: %q", got)
	}
	// сжатие учтено в ходе, где случилось
	var withCompress int
	for _, tr := range a.Turns() {
		withCompress += tr.AuxCalls
	}
	if withCompress != 1 {
		t.Fatalf("вызовов сжатия в учёте ходов: %d", withCompress)
	}
}

func TestRollingSummaryCarriesPreviousOne(t *testing.T) {
	f := &fakeLLM{}
	a := NewPool(f, "fake", nil).Spawn(summaryConfig(2, 2))
	for i := 1; i <= 6; i++ {
		ask(t, a, fmt.Sprintf("факт номер %d", i))
	}
	var summarizeReqs []string
	f.mu.Lock()
	for _, r := range f.requests {
		if strings.HasPrefix(r.Messages[0].Content, "Ты сжимаешь переписку") {
			summarizeReqs = append(summarizeReqs, r.Messages[1].Content)
		}
	}
	f.mu.Unlock()
	if len(summarizeReqs) < 2 {
		t.Fatalf("сжатий %d, ожидали хотя бы два", len(summarizeReqs))
	}
	if strings.Contains(summarizeReqs[0], "Предыдущая сводка") {
		t.Fatal("в первом сжатии не может быть предыдущей сводки")
	}
	if !strings.Contains(summarizeReqs[1], "Предыдущая сводка") || !strings.Contains(summarizeReqs[1], "факт номер 1") {
		t.Fatalf("второе сжатие не получило прежнюю сводку:\n%s", summarizeReqs[1])
	}
	text, _ := a.Summary()
	if !strings.Contains(text, "факт номер 1") {
		t.Fatalf("старый факт потерялся при повторном сжатии:\n%s", text)
	}
}

func TestCompressionFailureDoesNotBreakTurn(t *testing.T) {
	f := &fakeLLM{failSummary: true}
	a := NewPool(f, "fake", nil).Spawn(summaryConfig(2, 2))
	var warned bool
	for i := 1; i <= 3; i++ {
		_, err := a.Ask(t.Context(), fmt.Sprintf("реплика %d", i), func(e Event) {
			if e.Kind == EventContext && strings.Contains(e.Label, "не удалось") {
				warned = true
			}
		})
		if err != nil {
			t.Fatalf("сбой сжатия сломал ход %d: %v", i, err)
		}
	}
	if !warned {
		t.Fatal("о сбое сжатия не сообщили")
	}
	if _, covered := a.Summary(); covered != 0 {
		t.Fatal("сводка появилась, хотя сжатие падало")
	}
	// без сводки ушла вся история
	if n := len(f.last().Messages); n != 6 {
		t.Fatalf("при сбое сжатия в запросе %d сообщений, ожидали всю историю (6)", n)
	}
}

func TestFullContextIgnoresSummary(t *testing.T) {
	a := NewPool(&fakeLLM{}, "fake", nil).Spawn(summaryConfig(2, 2))
	for i := 1; i <= 3; i++ {
		ask(t, a, fmt.Sprintf("реплика %d", i))
	}
	if _, covered := a.Summary(); covered == 0 {
		t.Fatal("подготовка: сводка должна была появиться")
	}
	cfg := a.Config()
	cfg.Context = ContextFull
	a.SetConfig(cfg)
	if n := len(a.Messages("ещё")); n != 1+6+1 {
		t.Fatalf("полная история: в стеке %d сообщений", n)
	}
	if strings.Contains(a.Messages("ещё")[0].Content, "Краткое содержание") {
		t.Fatal("в режиме полной истории в system осталась сводка")
	}
}

func TestSummaryResetAndRestart(t *testing.T) {
	dir := t.TempDir()
	p := NewPool(&fakeLLM{}, "fake", nil)
	p.SetStore(NewFileStore(dir))
	cfg := summaryConfig(2, 2)
	cfg.Name = "s"
	a := p.Spawn(cfg)
	ask(t, a, "меня зовут Саша")
	ask(t, a, "реплика 2")
	ask(t, a, "реплика 3")
	text, covered := a.Summary()
	if covered == 0 {
		t.Fatal("подготовка: нет сводки")
	}

	_, restored := restart(t, dir)
	rt, rc := restored[0].Summary()
	if rt != text || rc != covered {
		t.Fatalf("сводка после перезапуска: %d/%q, была %d/%q", rc, rt, covered, text)
	}
	if got := ask(t, restored[0], "как меня зовут?"); got != "тебя зовут Саша" {
		t.Fatalf("после перезапуска со сводкой: %q", got)
	}

	restored[0].Reset()
	if rt, rc := restored[0].Summary(); rt != "" || rc != 0 {
		t.Fatal("сброс не стёр сводку")
	}
}

func TestCompressionBoundaryKeepsPairs(t *testing.T) {
	msgs := func(n int) []llm.Message {
		out := make([]llm.Message, n)
		for i := range out {
			out[i].Role = llm.RoleUser
			if i%2 == 1 {
				out[i].Role = llm.RoleAssistant
			}
		}
		return out
	}
	// хвост 3 — нечётный: граница сдвигается, чтобы не разорвать пару
	upTo, ok := needsCompression(summaryConfig(3, 2), msgs(8), summaryState{})
	if !ok || upTo%2 != 0 || upTo != 4 {
		t.Fatalf("граница сжатия %d (ok=%v)", upTo, ok)
	}
	if _, ok := needsCompression(Config{Model: "m"}, msgs(40), summaryState{}); ok {
		t.Fatal("в режиме полной истории сжатие запускаться не должно")
	}
}
