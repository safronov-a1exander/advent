package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlockIsTheFileBody(t *testing.T) {
	// Профиль — это текст, который написал человек. Никакой сборки
	// из полей: что написано, то и читает модель.
	p := &Profile{Name: "сеньор", About: "Олег — тимлид.\n\nОтвечай сухо и коротко."}
	b := p.Block()
	if !strings.Contains(b, "Олег — тимлид.") || !strings.Contains(b, "Отвечай сухо и коротко.") {
		t.Fatalf("тело файла не дошло до промпта:\n%s", b)
	}
	// Абзацы сохраняются: это проза, а не список пунктов.
	if !strings.Contains(b, "\n\n") {
		t.Fatalf("разбиение на абзацы потерялось:\n%s", b)
	}
	if (&Profile{Name: "пустой"}).Block() != "" {
		t.Fatal("профиль без тела даёт пустой блок")
	}
}

func TestChoosePromptDescribesRoadsInProse(t *testing.T) {
	p := &Profile{Pipelines: []Pipeline{
		{Name: "решить"},
		{Name: "инцидент", When: "что-то сломалось прямо сейчас"},
	}}
	q := ChoosePrompt(p, "у нас всё легло")
	if !strings.Contains(q, "решить (по умолчанию)") {
		t.Fatalf("дорога по умолчанию не помечена:\n%s", q)
	}
	if !strings.Contains(q, "инцидент: что-то сломалось прямо сейчас") {
		t.Fatalf("описание дороги не ушло выбирающему:\n%s", q)
	}
	if !strings.Contains(q, "у нас всё легло") {
		t.Fatal("реплика не ушла выбирающему")
	}
}

func TestChoiceMapsBackToRoad(t *testing.T) {
	p := &Profile{Pipelines: []Pipeline{
		{Name: "решить"},
		{Name: "разобрать инцидент", When: "что-то сломалось"},
	}}

	name, err := ParseChoice("```json\n" + `{"road":"разобрать инцидент"}` + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	if pl, ok := p.ByName(name); !ok || pl.Name != "разобрать инцидент" {
		t.Fatalf("имя не легло на дорогу: %+v", pl)
	}
	// Модель назвала несуществующую дорогу — берём дорогу по умолчанию,
	// а не падаем: ошибка выбора должна стоить обычного маршрута.
	if pl, ok := p.ByName("рефлексия"); !ok || pl.Name != "решить" {
		t.Fatalf("выдуманная дорога не свелась к умолчанию: %+v", pl)
	}
	if _, err := ParseChoice("думаю, это инцидент"); err == nil {
		t.Fatal("не-JSON должен быть ошибкой")
	}
}

func TestNoChoiceWhenThereIsNothingToChoose(t *testing.T) {
	// Одна дорога — выбирать не из чего и платить за вызов не за что.
	one := &Profile{Pipelines: []Pipeline{{Name: "оценить"}}}
	if one.NeedsChoice() {
		t.Fatal("при одной дороге выбор не нужен")
	}
	if (&Profile{}).NeedsChoice() {
		t.Fatal("без дорог выбирать нечего")
	}
	two := &Profile{Pipelines: []Pipeline{{Name: "a"}, {Name: "b", When: "иначе"}}}
	if !two.NeedsChoice() {
		t.Fatal("две дороги — выбор нужен")
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

func TestValidateRejectsUnusableProfiles(t *testing.T) {
	if err := (&Profile{ID: "x"}).Validate(); err == nil {
		t.Fatal("профиль с пустым телом должен отвергаться")
	}
	dead := &Profile{ID: "x", About: "текст", Pipelines: []Pipeline{
		{Name: "первая"},
		{Name: "вторая"}, // без when — выбирающий про неё ничего не узнает
	}}
	if err := dead.Validate(); err == nil || !strings.Contains(err.Error(), "when") {
		t.Fatalf("дорога без описания должна отвергаться: %v", err)
	}
	dup := &Profile{ID: "x", About: "текст", Pipelines: []Pipeline{
		{Name: "одна"}, {Name: "одна", When: "иначе"},
	}}
	if err := dup.Validate(); err == nil {
		t.Fatal("две дороги с одним именем должны отвергаться")
	}
}

func TestFileStoreReadsMarkdownAndNoticesEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "сеньор.md")
	write := func(body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("---\nname: Олег\npipelines:\n  - name: решить\n---\n\nТимлид. Отвечай сухо.\n")

	st := NewFileStore(dir)
	p, err := st.Load("сеньор")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Олег" || p.ID != "сеньор" {
		t.Fatalf("шапка прочитана неверно: %+v", p)
	}
	if !strings.Contains(p.About, "Тимлид. Отвечай сухо.") {
		t.Fatalf("тело прочитано неверно: %q", p.About)
	}
	if len(p.Pipelines) != 1 {
		t.Fatalf("дороги не прочитались: %+v", p.Pipelines)
	}

	// Правка файла должна действовать со следующего запроса.
	write("---\nname: Олег\npipelines:\n  - name: решить\n---\n\nТимлид. Отвечай очень подробно.\n")
	p2, err := st.Load("сеньор")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p2.About, "очень подробно") {
		t.Fatalf("правка файла не подхватилась: %q", p2.About)
	}

	ids, err := st.List()
	if err != nil || len(ids) != 1 || ids[0] != "сеньор" {
		t.Fatalf("список профилей: %v %v", ids, err)
	}
}

func TestFileStoreReadsProfileWithoutFrontmatter(t *testing.T) {
	// Профиль без дорог — просто текстовый файл, и это нормальный профиль.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "простой.md"),
		[]byte("Отвечай коротко и по-русски.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := NewFileStore(dir).Load("простой")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "простой" || len(p.Pipelines) != 0 {
		t.Fatalf("профиль без frontmatter: %+v", p)
	}
	if !strings.Contains(p.Block(), "коротко") {
		t.Fatal("тело не дошло до промпта")
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

func TestRepoProfilesAreValid(t *testing.T) {
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
