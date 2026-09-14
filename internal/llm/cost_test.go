package llm

import "testing"

func TestCostFallsBackToRequestedModel(t *testing.T) {
	c := New("x", "http://127.0.0.1", "k", WithPricing(map[string]ModelInfo{
		"deepseek-flash": {ID: "deepseek-flash", InPer1M: 0.3, CachedIn1M: 0.006, OutPer1M: 1.2},
		"old-name":       {ID: "old-name", InPer1M: 1, OutPer1M: 2},
	}))
	u := Usage{PromptTokens: 1_000_000, CachedPromptTokens: 400_000, CompletionTokens: 1_000_000}

	// сервер назвал модель по-новому — считаем по ней
	if got, want := c.costOf("deepseek-flash", "deepseek-v4-flash", u), 0.6*0.3+0.4*0.006+1.2; !near(got, want) {
		t.Fatalf("по имени из ответа: %f, ожидали %f", got, want)
	}
	// имени из ответа в прайсе нет — считаем по запрошенной
	if got, want := c.costOf("renamed-by-server", "old-name", u), 0.6*1+0.4*0+2.0; !near(got, want) {
		t.Fatalf("фолбэк на запрошенную: %f, ожидали %f", got, want)
	}
	// нет ни того ни другого — честный ноль, а не чужой прайс
	if got := c.costOf("a", "b", u); got != 0 {
		t.Fatalf("неизвестная модель: %f", got)
	}
}

func near(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}
