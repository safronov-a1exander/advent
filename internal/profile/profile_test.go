package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlockSkipsEmptyFields(t *testing.T) {
	p := &Profile{
		Name:   "сеньор",
		About:  "тимлид",
		Style:  Style{Tone: "сухой"},
		Limits: []string{"без предисловий"},
	}
	b := p.Block()
	for _, want := range []string{"тимлид", "тон: сухой", "ограничение: без предисловий"} {
		if !strings.Contains(b, want) {
			t.Fatalf("в блоке нет %q:\n%s", want, b)
		}
	}
	// Пустое «длина ответа: » модель читает как указание и начинает
	// выдумывать, каким же он должен быть.
	if strings.Contains(b, "длина ответа") || strings.Contains(b, "обращение") {
		t.Fatalf("незаполненные поля попали в блок:\n%s", b)
	}
	if (&Profile{}).Block() != "" {
		t.Fatal("пустой профиль должен давать пустой блок")
	}
}

func TestPickRoutesByKeyword(t *testing.T) {
	p := &Profile{Pipelines: []Pipeline{
		{Name: "решить"},
		{Name: "инцидент", When: []string{"упал", "таймаут"}},
		{Name: "ревью", When: []string{"посмотри код"}},
	}}

	for _, c := range []struct{ query, want string }{
		{"как лучше сделать кэш?", "решить"},
		{"сервис УПАЛ ночью", "инцидент"},
		{"Посмотри код вот тут", "ревью"},
		{"", "решить"},
	} {
		got, ok := p.Pick(c.query)
		if !ok || got.Name != c.want {
			t.Fatalf("%q → %q, ожидали %q", c.query, got.Name, c.want)
		}
	}

	// Без профиля и без дорог дороги нет — и это не ошибка.
	if _, ok := (*Profile)(nil).Pick("что угодно"); ok {
		t.Fatal("у nil-профиля дорог быть не может")
	}
	if _, ok := (&Profile{Name: "без дорог"}).Pick("вопрос"); ok {
		t.Fatal("профиль без дорог не должен выбирать дорогу")
	}
}

func TestStageBlockNamesStages(t *testing.T) {
	pl := Pipeline{Name: "инцидент", Stages: []string{"гипотезы", "проверка", "фикс"}}
	b := pl.StageBlock()
	if !strings.Contains(b, "гипотезы → проверка → фикс") {
		t.Fatalf("стадии не перечислены: %q", b)
	}
	if !strings.Contains(b, "не перепрыгивай") {
		t.Fatalf("нет запрета перепрыгивать стадии: %q", b)
	}
	if (Pipeline{Name: "пусто"}).StageBlock() != "" {
		t.Fatal("дорога без стадий и без system даёт пустой блок")
	}
}

func TestValidateRejectsDeadPipeline(t *testing.T) {
	p := &Profile{Name: "x", Pipelines: []Pipeline{
		{Name: "первая"},
		{Name: "вторая"}, // без when — никогда не выберется
	}}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "недостижима") {
		t.Fatalf("мёртвая дорога должна отвергаться: %v", err)
	}

	dup := &Profile{Name: "x", Pipelines: []Pipeline{
		{Name: "одна"}, {Name: "одна", When: []string{"а"}},
	}}
	if err := dup.Validate(); err == nil {
		t.Fatal("две дороги с одним именем должны отвергаться")
	}

	if err := (&Profile{}).Validate(); err == nil {
		t.Fatal("пустой профиль должен отвергаться")
	}
}

func TestFileStoreLoadsAndNoticesEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "сеньор.yaml")
	write := func(tone string) {
		body := "name: Олег\nabout: тимлид\nstyle:\n  tone: " + tone + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("сухой")

	st := NewFileStore(dir)
	p, err := st.Load("сеньор")
	if err != nil {
		t.Fatal(err)
	}
	if p.Style.Tone != "сухой" || p.ID != "сеньор" {
		t.Fatalf("профиль прочитан неверно: %+v", p)
	}

	// Правка файла должна действовать со следующего запроса: иначе
	// «поправил профиль — перезапусти приложение».
	write("подробный")
	// mtime на Windows тикает не всегда — меняем и размер тоже.
	if err := os.WriteFile(path, []byte("name: Олег\nabout: тимлид\nstyle:\n  tone: очень подробный\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p2, err := st.Load("сеньор")
	if err != nil {
		t.Fatal(err)
	}
	if p2.Style.Tone != "очень подробный" {
		t.Fatalf("правка файла не подхватилась: %q", p2.Style.Tone)
	}

	ids, err := st.List()
	if err != nil || len(ids) != 1 || ids[0] != "сеньор" {
		t.Fatalf("список профилей: %v %v", ids, err)
	}
}

func TestLoadEmptyIDIsNotAnError(t *testing.T) {
	st := NewFileStore(t.TempDir())
	p, err := st.Load("")
	if err != nil || p != nil {
		t.Fatalf("пустой id — это «без профиля», а не ошибка: %v %v", p, err)
	}
	if _, err := st.Load("нет-такого"); err == nil {
		t.Fatal("несуществующий профиль должен быть ошибкой")
	}
}

func TestSampleProfilesAreValid(t *testing.T) {
	// Профили в репозитории — часть сдачи дня: если они перестанут
	// читаться, демо развалится на записи, а не в тестах.
	st := NewFileStore(filepath.Join("..", "..", "profiles"))
	ids, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) < 3 {
		t.Fatalf("профилей в репозитории %d, ожидали хотя бы 3", len(ids))
	}
	for _, id := range ids {
		p, err := st.Load(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if p.Block() == "" {
			t.Fatalf("%s: пустой блок для промпта", id)
		}
		if _, ok := p.Default(); !ok {
			t.Fatalf("%s: нет ни одной дороги", id)
		}
	}
}
