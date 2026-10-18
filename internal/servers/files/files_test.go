package files

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func project(t *testing.T) *Project {
	t.Helper()
	root := t.TempDir()
	write := func(rel, text string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# проект\n\n| шаг | тема |\n|---|---|\n| 1 | старт |\n")
	write("docs/days/day01.md", "# День 1 — старт\n")
	write("docs/days/day02.md", "# День 2 — формат\n")
	write("cmd/main.go", "package main\n\nfunc main() { knowledgeFor() }\n")
	write(".env", "DEEPSEEK_API_KEY=sk-секрет\n")
	write("keys.txt", "телефон sk-local-1\n")
	return &Project{Root: root, Stage: filepath.Join(root, "runs", "files", "stage")}
}

func call(t *testing.T, p *Project, tool, args string) (string, bool) {
	t.Helper()
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}`
	var resp struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(New(p).Handle(context.Background(), []byte(req)), &resp); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range resp.Result.Content {
		b.WriteString(c.Text)
	}
	return b.String(), resp.Result.IsError
}

func TestSandboxClosesSecretsAndOutside(t *testing.T) {
	p := project(t)
	for _, bad := range []string{".env", "keys.txt", "../x.md", "/etc/passwd", "C:/Windows/win.ini", ".git/config", "runs/files/stage/README.md"} {
		if _, isErr := call(t, p, "read_file", `{"path":"`+bad+`"}`); !isErr {
			t.Errorf("%s прочитан", bad)
		}
	}
	out, _ := call(t, p, "search", `{"regex":"sk-"}`)
	if strings.Contains(out, "секрет") || strings.Contains(out, "local-1") {
		t.Fatalf("поиск заглянул в закрытые файлы: %s", out)
	}
}

func TestEditGoesToStageAndIsReadBack(t *testing.T) {
	p := project(t)
	out, bad := call(t, p, "edit_file", `{"path":"README.md","old":"| 1 | старт |","new":"| 1 | старт |\n| 2 | формат |"}`)
	if bad {
		t.Fatal(out)
	}
	orig, _ := os.ReadFile(filepath.Join(p.Root, "README.md"))
	if strings.Contains(string(orig), "формат") {
		t.Fatal("правка должна лечь в черновик, а не в проект")
	}
	out, _ = call(t, p, "read_file", `{"path":"README.md"}`)
	if !strings.Contains(out, "| 2 | формат |") || !strings.Contains(out, "версия из черновика") {
		t.Fatalf("модель читает свою правку: %s", out)
	}
	if st, _ := p.Staged(); len(st) != 1 || st[0] != "README.md" {
		t.Fatalf("в черновике: %v", st)
	}
	out, _ = call(t, p, "list_files", `{"pattern":"*.md"}`)
	if !strings.Contains(out, "README.md  [изменён в черновике]") {
		t.Fatalf("list_files отмечает черновик: %s", out)
	}
}

func TestEditNeedsExactlyOneMatch(t *testing.T) {
	p := project(t)
	if out, bad := call(t, p, "edit_file", `{"path":"README.md","old":"|---|","new":"x"}`); bad {
		t.Fatalf("одно вхождение — можно: %s", out)
	}
	if out, bad := call(t, p, "edit_file", `{"path":"README.md","old":"нет такого","new":"x"}`); !bad || !strings.Contains(out, "нет такого фрагмента") {
		t.Fatal(out)
	}
	call(t, p, "write_file", `{"path":"a.md","content":"x\nx\n"}`)
	if out, bad := call(t, p, "edit_file", `{"path":"a.md","old":"x","new":"y"}`); !bad || !strings.Contains(out, "2 раз") {
		t.Fatalf("неоднозначная правка должна отклоняться: %s", out)
	}
}

func TestSearchAcrossFilesWithLines(t *testing.T) {
	p := project(t)
	out, _ := call(t, p, "search", `{"regex":"^# День","pattern":"docs/days"}`)
	if !strings.Contains(out, "docs/days/day01.md:1: # День 1 — старт") || !strings.Contains(out, "в 2 файлах") {
		t.Fatal(out)
	}
	out, _ = call(t, p, "search", `{"regex":"knowledgeFor\\("}`)
	if !strings.Contains(out, "cmd/main.go:3:") {
		t.Fatal(out)
	}
}
