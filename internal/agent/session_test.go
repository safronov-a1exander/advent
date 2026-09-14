package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/safronov-a1exander/advent/internal/llm"
)

// restart имитирует перезапуск приложения: новый пул, новый клиент,
// общий только каталог на диске.
func restart(t *testing.T, dir string) (*Pool, []*Agent) {
	t.Helper()
	p := NewPool(&fakeLLM{}, "fake", nil)
	p.SetStore(NewFileStore(dir))
	agents, err := p.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	return p, agents
}

func TestDialogueSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	p1 := NewPool(&fakeLLM{}, "fake", nil)
	p1.SetStore(NewFileStore(dir))
	a := p1.Spawn(Config{Name: "бюджет", Model: "m", System: "ты ассистент", Temperature: llm.F(0.3)})
	ask(t, a, "привет, меня зовут Саша")
	ask(t, a, "бюджет 60000")
	before := a.Stats()

	// «перезапуск»: p1 больше не используется
	p2, restored := restart(t, dir)
	if len(restored) != 1 {
		t.Fatalf("восстановлено %d агентов", len(restored))
	}
	b := restored[0]
	if b.ID() != a.ID() {
		t.Fatalf("id после перезапуска %q, был %q", b.ID(), a.ID())
	}
	if got := b.History(); len(got) != 4 || got[0].Content != "привет, меня зовут Саша" {
		t.Fatalf("история после перезапуска: %+v", got)
	}
	if cfg := b.Config(); cfg.System != "ты ассистент" || cfg.Temperature == nil || *cfg.Temperature != 0.3 {
		t.Fatalf("конфиг после перезапуска: %+v", cfg)
	}
	if b.Stats() != before {
		t.Fatalf("счётчики: %+v, были %+v", b.Stats(), before)
	}

	// главное — разговор продолжается так, будто агент не выключался
	if got := ask(t, b, "как меня зовут?"); got != "тебя зовут Саша" {
		t.Fatalf("после перезапуска агент не помнит: %q", got)
	}

	// и продолжение тоже сохранилось
	_, again := restart(t, dir)
	if n := len(again[0].History()); n != 6 {
		t.Fatalf("после второго перезапуска в истории %d сообщений, ожидали 6", n)
	}

	// новый агент не займёт id восстановленного
	if c := p2.Spawn(Config{Name: "бюджет", Model: "m"}); c.ID() == a.ID() {
		t.Fatalf("новый агент получил занятый id %s", c.ID())
	}
}

func TestSessionFileIsReadableJSON(t *testing.T) {
	dir := t.TempDir()
	p := NewPool(&fakeLLM{}, "fake", nil)
	store := NewFileStore(dir)
	p.SetStore(store)
	a := p.Spawn(Config{Name: "бюджет", Model: "m"})
	ask(t, a, "вопрос")

	b, err := os.ReadFile(store.Path(a.ID()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"version": 1`, `"role": "user"`, `"content": "вопрос"`, `"model": "m"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("в файле нет %s:\n%s", want, b)
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(leftovers) > 0 {
		t.Fatalf("остались временные файлы: %v", leftovers)
	}
}

func TestResetAndConfigArePersisted(t *testing.T) {
	dir := t.TempDir()
	p := NewPool(&fakeLLM{}, "fake", nil)
	p.SetStore(NewFileStore(dir))
	a := p.Spawn(Config{Name: "x", Model: "m"})
	ask(t, a, "вопрос")

	a.Reset()
	cfg := a.Config()
	cfg.Model = "другая"
	a.SetConfig(cfg)

	_, restored := restart(t, dir)
	if n := len(restored[0].History()); n != 0 {
		t.Fatalf("сброс не сохранился: %d сообщений", n)
	}
	if restored[0].Config().Model != "другая" {
		t.Fatal("смена модели не сохранилась")
	}
}

func TestSameConfigDoesNotRewrite(t *testing.T) {
	p := NewPool(&fakeLLM{}, "fake", nil)
	p.SetStore(NewFileStore(t.TempDir()))
	a := p.Spawn(Config{Model: "m", Temperature: llm.F(0.5)})
	rev := a.snapshot().Rev
	a.SetConfig(Config{Model: "m", Temperature: llm.F(0.5)})
	if got := a.snapshot().Rev; got != rev {
		t.Fatalf("тот же конфиг поднял ревизию %d → %d", rev, got)
	}
}

func TestTempAgentsAreNotSaved(t *testing.T) {
	dir := t.TempDir()
	p := NewPool(&fakeLLM{}, "fake", nil)
	p.SetStore(NewFileStore(dir))
	tmp := p.SpawnTemp(Config{Name: "bench", Model: "m"})
	ask(t, tmp, "сравни")
	if err := p.Remove(tmp.ID()); err != nil {
		t.Fatal(err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(files) != 0 {
		t.Fatalf("временный агент попал на диск: %v", files)
	}
}

func TestRemoveArchivesInsteadOfDeleting(t *testing.T) {
	dir := t.TempDir()
	p := NewPool(&fakeLLM{}, "fake", nil)
	store := NewFileStore(dir)
	p.SetStore(store)
	keep := p.Spawn(Config{Name: "keep", Model: "m"})
	ask(t, keep, "остаётся")
	gone := p.Spawn(Config{Name: "gone", Model: "m"})
	ask(t, gone, "важный разговор")

	if err := p.Remove(gone.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Path(gone.ID())); !os.IsNotExist(err) {
		t.Fatal("закрытый разговор остался среди активных")
	}
	archived, _ := filepath.Glob(filepath.Join(dir, "archive", gone.ID()+"-*.json"))
	if len(archived) != 1 {
		t.Fatalf("в архиве %d файлов", len(archived))
	}
	_, restored := restart(t, dir)
	if len(restored) != 1 || restored[0].ID() != keep.ID() {
		t.Fatalf("после перезапуска поднялся не тот набор: %d", len(restored))
	}
}

func TestStaleSnapshotIsSkipped(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	newer := Snapshot{Version: 1, ID: "a-001", Rev: 5, History: []llm.Message{{Role: llm.RoleUser, Content: "новое"}}}
	older := Snapshot{Version: 1, ID: "a-001", Rev: 4, History: []llm.Message{{Role: llm.RoleUser, Content: "старое"}}}
	if err := store.Save(newer); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(older); err != nil {
		t.Fatal(err)
	}
	snaps, err := NewFileStore(dir).LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if snaps[0].History[0].Content != "новое" {
		t.Fatal("запоздавший старый снимок перезаписал новый")
	}
}

func TestBrokenFileDoesNotBlockOthers(t *testing.T) {
	dir := t.TempDir()
	p := NewPool(&fakeLLM{}, "fake", nil)
	p.SetStore(NewFileStore(dir))
	ask(t, p.Spawn(Config{Name: "ok", Model: "m"}), "вопрос")
	if err := os.WriteFile(filepath.Join(dir, "broken-002.json"), []byte("{обрезано"), 0o644); err != nil {
		t.Fatal(err)
	}

	p2 := NewPool(&fakeLLM{}, "fake", nil)
	p2.SetStore(NewFileStore(dir))
	restored, err := p2.Restore()
	if err == nil || !strings.Contains(err.Error(), "broken-002.json") {
		t.Fatalf("ожидали ошибку с именем битого файла, получили %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("битый файл помешал поднять остальные: %d", len(restored))
	}
}

func TestEmptyConversationIsNotSavedUntilFirstMessage(t *testing.T) {
	dir := t.TempDir()
	p := NewPool(&fakeLLM{}, "fake", nil)
	p.SetStore(NewFileStore(dir))
	a := p.Spawn(Config{Name: "x", Model: "m"})
	cfg := a.Config()
	cfg.Model = "другая"
	a.SetConfig(cfg) // правка настроек пустого разговора — ещё не повод для файла

	if files, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(files) != 0 {
		t.Fatalf("пустой разговор попал на диск: %v", files)
	}
	ask(t, a, "первая реплика")
	a.Reset() // а однажды сохранённый сохраняется и пустым
	_, restored := restart(t, dir)
	if len(restored) != 1 || len(restored[0].History()) != 0 || restored[0].Config().Model != "другая" {
		t.Fatalf("после сброса: %d агентов", len(restored))
	}
}

func TestRestoreOnlyOwnProvider(t *testing.T) {
	dir := t.TempDir()
	onDeepseek := NewPool(&fakeLLM{}, "deepseek", nil)
	onDeepseek.SetStore(NewFileStore(dir))
	ask(t, onDeepseek.Spawn(Config{Name: "ds", Model: "deepseek-flash"}), "вопрос")
	onMock := NewPool(&fakeLLM{}, "mock", nil)
	onMock.SetStore(NewFileStore(dir))
	ask(t, onMock.Spawn(Config{Name: "mk", Model: "mock-weak"}), "вопрос")

	p := NewPool(&fakeLLM{}, "mock", nil)
	p.SetStore(NewFileStore(dir))
	restored, err := p.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0].Config().Model != "mock-weak" {
		t.Fatalf("под mock поднялось %d разговоров", len(restored))
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(files) != 2 {
		t.Fatalf("чужой разговор должен остаться на диске, файлов: %d", len(files))
	}
}

func TestRestoreIntoEmptyDir(t *testing.T) {
	p, restored := restart(t, filepath.Join(t.TempDir(), "ещё-нет"))
	if len(restored) != 0 || p.Len() != 0 {
		t.Fatal("из несуществующего каталога что-то поднялось")
	}
}

func TestSaveErrorIsReported(t *testing.T) {
	// каталог хранилища — на месте обычного файла: писать некуда
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewPool(&fakeLLM{}, "fake", nil)
	p.SetStore(NewFileStore(blocker))
	a := p.Spawn(Config{Model: "m"})
	if _, err := a.Ask(context.Background(), "вопрос", nil); err != nil {
		t.Fatal(err)
	}
	if err := p.SaveErr(); err == nil {
		t.Fatal("ошибка сохранения потерялась")
	}
	if err := p.SaveErr(); err != nil {
		t.Fatal("SaveErr должна сбрасываться после чтения")
	}
}
