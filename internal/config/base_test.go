package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/safronov-a1exander/advent/internal/rag"
)

func TestBase(t *testing.T) {
	c := &Config{RAG: RAGConfig{
		IndexDir: "index",
		Sources:  rag.Sources{Include: []string{"README.md"}},
		Bases: map[string]rag.Sources{
			"project": {Include: []string{"docs/*.md"}},
			"review":  {Include: []string{"**/*.go"}},
		},
	}}
	src, dir, err := c.Base("")
	if err != nil || dir != "index" || src.Include[0] != "README.md" {
		t.Fatalf("пустое имя — основная база: %v %q %v", src, dir, err)
	}
	src, dir, err = c.Base("project")
	if err != nil || dir != filepath.Join("index", "project") || src.Include[0] != "docs/*.md" {
		t.Fatalf("своя база — свой каталог индекса: %v %q %v", src, dir, err)
	}
	_, _, err = c.Base("нет-такой")
	if err == nil || !strings.Contains(err.Error(), "есть: project, review") {
		t.Fatalf("ошибка должна перечислять базы по алфавиту: %v", err)
	}
}
