package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/safronov-a1exander/advent/internal/llm"
)

// honestLLM считает токены как «настоящий» токенизатор: один токен на
// perToken символов плюс шаблон на сообщение. Агент этих чисел не знает
// и должен прийти к ним калибровкой.
func honestLLM(perToken float64, overhead int) *fakeLLM {
	return &fakeLLM{usage: func(msgs []llm.Message) llm.Usage {
		n := 0
		for _, m := range msgs {
			n += int(float64(utf8.RuneCountInString(m.Content))/perToken) + overhead
		}
		return llm.Usage{PromptTokens: n, CompletionTokens: 20, TotalTokens: n + 20}
	}}
}

func TestTurnsShowHistoryGrowth(t *testing.T) {
	a := NewPool(honestLLM(3, 3), "fake", nil).Spawn(Config{Model: "m", System: "ты ассистент по личным финансам"})
	q := "сколько я потратил на кафе в сентябре?"
	for i := 0; i < 5; i++ {
		ask(t, a, q)
	}
	turns := a.Turns()
	if len(turns) != 5 {
		t.Fatalf("ходов в учёте: %d", len(turns))
	}
	for i := 1; i < len(turns); i++ {
		if turns[i].Prompt <= turns[i-1].Prompt {
			t.Fatalf("запрос не растёт: ход %d — %d, ход %d — %d", i, turns[i-1].Prompt, i+1, turns[i].Prompt)
		}
		if turns[i].History() <= turns[i-1].History() {
			t.Fatalf("история не растёт на ходе %d", i+1)
		}
	}
	// вопрос один и тот же — его оценка не должна расти вместе с историей
	first, last := turns[0].Question, turns[4].Question
	if first == 0 || last > first*2 || last*2 < first {
		t.Fatalf("оценка одинакового вопроса скачет: %d → %d", first, last)
	}
	// к пятому ходу история весит больше самого вопроса
	if turns[4].History() <= turns[4].Question {
		t.Fatalf("история %d не больше вопроса %d", turns[4].History(), turns[4].Question)
	}
}

func TestCalibrationConvergesToProvider(t *testing.T) {
	f := honestLLM(1.6, 6) // заметно «плотнее», чем стартовое предположение
	a := NewPool(f, "fake", nil).Spawn(Config{Model: "m", System: strings.Repeat("правило бюджета. ", 20)})

	var firstErr, lastErr float64
	for i := 0; i < 6; i++ {
		ask(t, a, strings.Repeat("операция по карте на 1990 рублей; ", 5+i))
		tr := a.Turns()[i]
		e := relErr(tr.Estimated, tr.Prompt)
		if i == 0 {
			firstErr = e
		}
		lastErr = e
	}
	if lastErr > 0.1 {
		t.Fatalf("после калибровки оценка ошибается на %.0f%% (сначала %.0f%%)", lastErr*100, firstErr*100)
	}
	if lastErr >= firstErr {
		t.Fatalf("калибровка не улучшила оценку: %.2f → %.2f", firstErr, lastErr)
	}
	if c := a.Calibration(); c <= 1 {
		t.Fatalf("коэффициент %g: провайдер считает плотнее стартовой оценки", c)
	}
}

func TestOwnContextLimitBlocksBeforeSending(t *testing.T) {
	f := honestLLM(3, 3)
	a := NewPool(f, "fake", nil).Spawn(Config{Model: "m", ContextLimit: llm.I(150)})
	long := strings.Repeat("выписка: пятёрочка 2340, такси 620, кафе 390. ", 3)

	var overflow *ErrContextOverflow
	var sent int
	for i := 0; i < 20; i++ {
		f.mu.Lock()
		sent = len(f.requests)
		f.mu.Unlock()
		_, err := a.Ask(context.Background(), long, nil)
		if err == nil {
			continue
		}
		if !errors.As(err, &overflow) {
			t.Fatalf("ожидали переполнение, получили %v", err)
		}
		break
	}
	if overflow == nil {
		t.Fatal("лимит в 150 токенов так и не сработал")
	}
	if overflow.Limit != 150 || overflow.Estimated <= 150 {
		t.Fatalf("ошибка переполнения: %+v", overflow)
	}
	f.mu.Lock()
	after := len(f.requests)
	f.mu.Unlock()
	if after != sent {
		t.Fatal("запрос сверх лимита всё равно ушёл в API")
	}
	histBefore := len(a.History())
	if !IsContextOverflow(overflow) {
		t.Fatal("IsContextOverflow не узнал своё переполнение")
	}

	// после сброса разговор снова идёт
	a.Reset()
	if len(a.Turns()) != 0 {
		t.Fatal("сброс не очистил учёт ходов")
	}
	if a.Calibration() == 1 {
		t.Fatal("сброс не должен забывать калибровку: она про модель, а не про разговор")
	}
	ask(t, a, "короткий вопрос")
	if histBefore == 0 {
		t.Fatal("до переполнения разговор должен был накопиться")
	}
}

func TestProviderOverflowIsRecognized(t *testing.T) {
	api := &llm.APIError{Status: 400, Body: `{"error":{"message":"This model's maximum context length is 4096 tokens. However, you requested 5210 tokens","code":"context_length_exceeded"}}`}
	if !IsContextOverflow(api) {
		t.Fatal("отказ провайдера по окну не распознан")
	}
	if IsContextOverflow(&llm.APIError{Status: 400, Body: "Invalid temperature value"}) {
		t.Fatal("чужая ошибка 400 принята за переполнение")
	}
}

func TestContextUsageBeforeSending(t *testing.T) {
	a := NewPool(honestLLM(3, 3), "fake", nil).Spawn(Config{Model: "m", ContextLimit: llm.I(1000)})
	empty := a.Context("")
	ask(t, a, strings.Repeat("длинная реплика про траты. ", 10))
	grown := a.Context("")
	if grown.Estimated <= empty.Estimated || grown.Limit != 1000 {
		t.Fatalf("оценка контекста: пусто %+v, после реплики %+v", empty, grown)
	}
	if p := grown.Percent(); p <= 0 || p >= 100 {
		t.Fatalf("процент заполнения %d", p)
	}
	if (ContextUsage{Estimated: 10}).Percent() != -1 {
		t.Fatal("без лимита процент должен быть -1")
	}
	// с вопросом запрос тяжелее, чем без него
	if a.Context("ещё вопрос").Estimated <= grown.Estimated {
		t.Fatal("вопрос не учтён в оценке")
	}
}

func TestTurnsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	p := NewPool(honestLLM(3, 3), "fake", nil)
	p.SetStore(NewFileStore(dir))
	a := p.Spawn(Config{Name: "x", Model: "m"})
	ask(t, a, "первый")
	ask(t, a, "второй")
	calib := a.Calibration()

	_, restored := restart(t, dir)
	b := restored[0]
	if len(b.Turns()) != 2 || b.Turns()[1].Prompt != a.Turns()[1].Prompt {
		t.Fatalf("учёт ходов после перезапуска: %+v", b.Turns())
	}
	if b.Calibration() != calib {
		t.Fatalf("калибровка после перезапуска %g, была %g", b.Calibration(), calib)
	}
}

func relErr(est, actual int) float64 {
	if actual == 0 {
		return 1
	}
	d := float64(est - actual)
	if d < 0 {
		d = -d
	}
	return d / float64(actual)
}
