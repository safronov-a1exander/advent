package tui

import (
	"strings"
	"testing"
)

func TestRAGCommandTogglesKnowledge(t *testing.T) {
	m := memModel(t)
	typeText(t, m, "/rag on")
	if m.ag.Config().RAG != "on" {
		t.Fatal("/rag on не включил базу знаний")
	}
	if n := len(m.ag.History()); n != 0 {
		t.Fatalf("команда ушла в разговор: %d сообщений", n)
	}
	typeText(t, m, "/rag")
	if m.ag.Config().RAG != "" {
		t.Fatal("/rag без аргумента должен переключать")
	}
	typeText(t, m, "/rag может быть")
	if m.flash == "" || !strings.Contains(m.flash, "/rag") {
		t.Fatalf("на неверный аргумент нужна подсказка, а не молчание: %q", m.flash)
	}
}
