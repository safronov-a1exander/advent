package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// У llama.cpp рассуждения Qwen3 переключает шаблон чата: поле thinking
// не должно уходить, вместо него — chat_template_kwargs.enable_thinking.
func TestTemplateThinking(t *testing.T) {
	req := Request{Model: "m", Thinking: &Thinking{Type: "disabled"}}

	deepseek, _ := json.Marshal(New("d", "http://x", "k").buildWire(req, false))
	if !strings.Contains(string(deepseek), `"thinking":{"type":"disabled"}`) || strings.Contains(string(deepseek), "chat_template_kwargs") {
		t.Fatalf("DeepSeek: %s", deepseek)
	}

	local, _ := json.Marshal(New("l", "http://x", "k", WithTemplateThinking()).buildWire(req, false))
	if !strings.Contains(string(local), `"chat_template_kwargs":{"enable_thinking":false}`) || strings.Contains(string(local), `"thinking"`) {
		t.Fatalf("llama.cpp: %s", local)
	}

	// Режим не задан — шаблону ничего не передаём, у модели свой дефолт.
	none, _ := json.Marshal(New("l", "http://x", "k", WithTemplateThinking()).buildWire(Request{Model: "m"}, false))
	if strings.Contains(string(none), "chat_template_kwargs") {
		t.Fatalf("без режима: %s", none)
	}
}
