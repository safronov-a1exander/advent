package gitrepo

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repo — временный репозиторий: ветка main с README, ветка feature
// с новым файлом и незакоммиченной правкой.
func repo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git не найден")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, text string) {
		t.Helper()
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	write("README.md", "# проект\n")
	run("add", ".")
	run("commit", "-q", "-m", "старт")
	run("checkout", "-q", "-b", "feature")
	write("docs/заметка.md", "# заметка\n")
	run("add", ".")
	run("commit", "-q", "-m", "заметка")
	write("README.md", "# проект\n\nправка\n")
	return dir
}

func call(t *testing.T, dir, tool, args string) (string, bool) {
	t.Helper()
	s := New(dir)
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}`
	var resp struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(s.Handle(context.Background(), []byte(req)), &resp); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range resp.Result.Content {
		b.WriteString(c.Text)
	}
	return b.String(), resp.Result.IsError
}

func TestBranchAndStatus(t *testing.T) {
	dir := repo(t)
	out, bad := call(t, dir, "branch", `{}`)
	if bad || !strings.Contains(out, `"branch": "feature"`) || !strings.Contains(out, `"uncommitted_files": 1`) {
		t.Fatalf("branch: %s", out)
	}
	out, _ = call(t, dir, "status", `{}`)
	if !strings.Contains(out, "README.md — изменён") {
		t.Fatalf("status: %s", out)
	}
}

func TestFilesKeepsCyrillicAndFilters(t *testing.T) {
	dir := repo(t)
	out, _ := call(t, dir, "files", `{"path":"*.md"}`)
	if !strings.Contains(out, "docs/заметка.md") || !strings.Contains(out, "файлов: 2") {
		t.Fatalf("files *.md: %s", out)
	}
	out, _ = call(t, dir, "files", `{"path":"docs"}`)
	if strings.Contains(out, "README") || !strings.Contains(out, "файлов: 1") {
		t.Fatalf("files docs: %s", out)
	}
}

func TestDiffAgainstBaseAndWorkingCopy(t *testing.T) {
	dir := repo(t)
	out, _ := call(t, dir, "diff", `{"base":"main","stat":true}`)
	if !strings.Contains(out, "заметка.md") || strings.Contains(out, "README") {
		t.Fatalf("diff base: работа ветки — только закоммиченное: %s", out)
	}
	out, _ = call(t, dir, "diff", `{}`)
	if !strings.Contains(out, "+правка") {
		t.Fatalf("diff рабочей копии: %s", out)
	}
	out, _ = call(t, dir, "log", `{"base":"main"}`)
	if !strings.Contains(out, "заметка") || strings.Contains(out, "старт") {
		t.Fatalf("log base: %s", out)
	}
}

func TestRefCannotBecomeFlag(t *testing.T) {
	dir := repo(t)
	out, bad := call(t, dir, "diff", `{"base":"--output=x.txt"}`)
	if !bad || !strings.Contains(out, "недопустимое") {
		t.Fatalf("флаг в base должен отклоняться: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.txt")); err == nil {
		t.Fatal("git записал файл через base")
	}
}
