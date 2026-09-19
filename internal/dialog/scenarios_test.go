package dialog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Все сценарии репозитория читаются строгим разбором.
//
// Тест дешёвый и ловит ровно то, на чём я уже обжёгся: опечатка в имени
// поля или проверка, которой в структуре ещё нет, раньше проходили молча,
// и прогон «проверял» несуществующее условие.
func TestAllScenariosParse(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "scenarios", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("сценариев не нашлось — проверь путь")
	}
	seen := 0
	for _, p := range paths {
		// В каталоге лежат сценарии двух видов: диалоговые (день 9 и дальше)
		// и прогоны одиночных запросов первой недели. Здесь — только свои.
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "\ndialog:") {
			continue
		}
		seen++
		s, err := Load(p)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(p), err)
		}
		// Проверка, написанная для варианта с опечаткой в имени, тоже
		// ничего не проверяет — молча и до конца прогона.
		names := map[string]bool{}
		for _, v := range s.Variants {
			names[v.Name] = true
		}
		for i, l := range s.Dialog {
			for name := range l.ExpectBy {
				if !names[name] {
					t.Fatalf("%s, реплика %d: проверка для варианта %q, которого в сценарии нет",
						filepath.Base(p), i+1, name)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("диалоговых сценариев не нашлось — проверь путь")
	}
}
