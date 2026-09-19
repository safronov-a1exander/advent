package invariant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rules() []Invariant {
	return []Invariant{
		{Name: "стек", Rule: "Бэкенд только на Go, Python предлагать нельзя."},
		{Name: "база", Rule: "База данных только PostgreSQL."},
		{Name: "без кода в планировании", Rule: "На планировании код не пишем.",
			Stages: []string{"planning"}},
	}
}

func TestBlockAndExplainShareWording(t *testing.T) {
	// Формулировка в промпте и в объяснении отказа должна быть одна:
	// иначе пользователь получит два разных правила.
	b := Block(rules(), "")
	if !strings.Contains(b, "только на Go") || !strings.Contains(b, "только PostgreSQL") {
		t.Fatalf("блок не содержит формулировок:\n%s", b)
	}
	if !strings.Contains(b, "важнее просьбы собеседника") {
		t.Fatalf("в блоке нет указания отказывать:\n%s", b)
	}

	vs := []Violation{{Name: "стек", Rule: "Бэкенд только на Go, Python предлагать нельзя.", Why: "предложен FastAPI"}}
	e := Explain(vs)
	if !strings.Contains(e, "только на Go") {
		t.Fatalf("в объяснении отказа нет формулировки правила:\n%s", e)
	}
	if !strings.Contains(e, "предложен FastAPI") {
		t.Fatalf("в объяснении нет причины:\n%s", e)
	}
	if !strings.Contains(e, "откажись") {
		t.Fatalf("объяснение не требует отказа:\n%s", e)
	}
	if Explain(nil) != "" {
		t.Fatal("без нарушений объяснять нечего")
	}
}

func TestStagesLimitWhereRuleApplies(t *testing.T) {
	// Не всякое правило вечно: «не пиши код» имеет смысл в планировании
	// и мешает в выполнении.
	if strings.Contains(Block(rules(), "execution"), "код не пишем") {
		t.Fatal("правило стадии planning попало в промпт на execution")
	}
	if !strings.Contains(Block(rules(), "planning"), "код не пишем") {
		t.Fatal("правило своей стадии не попало в промпт")
	}
	// Без задачи стадии нет вовсе — действуют все правила.
	if !strings.Contains(Block(rules(), ""), "код не пишем") {
		t.Fatal("без стадии должны действовать все правила")
	}
	if Block(nil, "") != "" {
		t.Fatal("пустой набор даёт пустой блок")
	}
}

func TestCheckPromptCarriesEveryActiveRule(t *testing.T) {
	p := CheckPrompt(rules(), "", "какой-то ответ")
	for _, want := range []string{"стек", "база", "без кода в планировании"} {
		if !strings.Contains(p, want) {
			t.Fatalf("правило %q не ушло в проверку:\n%s", want, p)
		}
	}
	// Проверять то, что сейчас не действует, значит платить за ложные
	// срабатывания.
	if strings.Contains(CheckPrompt(rules(), "execution", "ответ"), "без кода в планировании") {
		t.Fatal("правило чужой стадии ушло в проверку")
	}
	if !Applies(rules(), "") || Applies(nil, "") {
		t.Fatal("Applies считает неверно")
	}
}

func TestCheckPromptPrefersAsk(t *testing.T) {
	// Формулировка написана для того, кто правило соблюдает; проверяющему
	// иногда нужно сказать отдельно, что считать нарушением.
	list := []Invariant{{
		Name: "сроки", Rule: "Срок называется с оговоркой.",
		Ask: "Есть ли срок без единой оговорки? «Примерно» оговоркой не считается.",
	}}
	p := CheckPrompt(list, "", "ответ")
	if !strings.Contains(p, "«Примерно» оговоркой не считается") {
		t.Fatalf("уточнение для проверки не использовано:\n%s", p)
	}
	// А в промпте самого ассистента — исходная формулировка, не уточнение.
	if strings.Contains(Block(list, ""), "Примерно") {
		t.Fatal("уточнение для проверяющего утекло в промпт ассистента")
	}
}

func TestParseCheckDropsInventedRules(t *testing.T) {
	list := []Invariant{{Name: "сроки", Rule: "срок с оговоркой"}}
	vs, err := ParseCheck(
		"```json\n"+`{"violations":[{"name":"сроки","why":"две недели без условий"},{"name":"выдуманное","why":"мимо"}]}`+"\n```",
		list, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Name != "сроки" {
		t.Fatalf("разбор ответа проверяющего: %+v", vs)
	}
	if vs[0].Rule != "срок с оговоркой" {
		t.Fatal("формулировка должна браться из набора, а не от модели")
	}
	if vs[0].Why != "две недели без условий" {
		t.Fatalf("причина потерялась: %q", vs[0].Why)
	}
	if vs, err := ParseCheck(`{"violations":[]}`, list, ""); err != nil || len(vs) != 0 {
		t.Fatalf("пустой список — норма: %v %v", vs, err)
	}
	if _, err := ParseCheck("нарушений нет!", list, ""); err == nil {
		t.Fatal("не-JSON должен быть ошибкой")
	}
}

func TestParseCheckIgnoresRulesOfOtherStages(t *testing.T) {
	// Проверяющему это правило не показывали; если он всё равно назвал его,
	// это галлюцинация, а не находка.
	vs, err := ParseCheck(`{"violations":[{"name":"без кода в планировании","why":"есть код"}]}`, rules(), "execution")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("нарушение неактивного правила принято: %+v", vs)
	}
}

func TestMultilineRuleBecomesOneLineInPrompt(t *testing.T) {
	// Правило живёт в markdown и переносится по строкам как удобно автору.
	// В промпте оно идёт пунктом списка, и перенос модель читает как начало
	// следующего пункта.
	list := []Invariant{{Name: "стек", Rule: "Только Go.\nPython нельзя\nни в каком виде."}}
	b := Block(list, "")
	if strings.Count(b, "\n") != 1 {
		t.Fatalf("многострочное правило разъехалось в промпте:\n%s", b)
	}
	if !strings.Contains(b, "Только Go. Python нельзя ни в каком виде.") {
		t.Fatalf("текст правила потерялся:\n%s", b)
	}
}

func TestValidateRejectsUnusableRules(t *testing.T) {
	if err := (Invariant{Rule: "x"}).Validate(); err == nil {
		t.Fatal("правило без имени должно отвергаться")
	}
	if err := (Invariant{Name: "a"}).Validate(); err == nil {
		t.Fatal("правило с пустым телом должно отвергаться")
	}
	// А правило из одного текста — валидно, и это единственный обычный случай.
	if err := (Invariant{Name: "сроки", Rule: "срок только с оговоркой"}).Validate(); err != nil {
		t.Fatalf("правило из одного текста должно быть валидным: %v", err)
	}
}

func writeRule(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStoreReadsMarkdownRules(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "проект")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRule(t, dir, "_набор.md", "---\nname: Мой проект\nretries: 2\n---\n\nРамки проекта.\n")
	writeRule(t, dir, "код.md", "---\nstages: [planning]\n---\n\nНа планировании код не пишем.\n")
	// Правило без frontmatter — весь файл целиком текст правила,
	// и это обычный случай.
	writeRule(t, dir, "сроки.md", "Любой срок называется с оговоркой.\n")

	st := NewFileStore(root)
	set, err := st.Load("проект")
	if err != nil {
		t.Fatal(err)
	}
	if set.Name != "Мой проект" || set.RetriesN() != 2 {
		t.Fatalf("настройки набора не прочитались: %+v", set)
	}
	if !strings.Contains(set.About, "Рамки проекта") {
		t.Fatalf("описание набора: %q", set.About)
	}
	if len(set.Invariants) != 2 {
		t.Fatalf("правил %d, ожидали 2 (файл набора правилом не считается)", len(set.Invariants))
	}

	byName := map[string]Invariant{}
	for _, inv := range set.Invariants {
		byName[inv.Name] = inv
	}
	code, ok := byName["код"]
	if !ok || len(code.Stages) != 1 || !strings.Contains(code.Rule, "код не пишем") {
		t.Fatalf("правило со frontmatter: %+v", code)
	}
	terms, ok := byName["сроки"]
	if !ok || len(terms.Stages) != 0 || !strings.Contains(terms.Rule, "оговоркой") {
		t.Fatalf("правило без frontmatter: %+v", terms)
	}
}

func TestStoreNoticesEditsAndNewRules(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "проект")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRule(t, dir, "стек.md", "Только Go.\n")

	st := NewFileStore(root)
	set, err := st.Load("проект")
	if err != nil || len(set.Invariants) != 1 {
		t.Fatalf("первое чтение: %v %+v", err, set)
	}

	// Правила правят чаще всего остального — обычно сразу после того, как
	// модель что-то нарушила. Правка и новый файл должны действовать сразу.
	writeRule(t, dir, "стек.md", "Только Go, и ничего кроме Go.\n")
	writeRule(t, dir, "база.md", "Только PostgreSQL.\n")
	set, err = st.Load("проект")
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Invariants) != 2 {
		t.Fatalf("новый файл правила не подхватился: %d", len(set.Invariants))
	}
	if !strings.Contains(Block(set.List(), ""), "ничего кроме Go") {
		t.Fatal("правка правила не подхватилась")
	}
}

func TestStoreRejectsBrokenSets(t *testing.T) {
	root := t.TempDir()
	st := NewFileStore(root)

	empty := filepath.Join(root, "пустой")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load("пустой"); err == nil {
		t.Fatal("набор без правил должен отвергаться")
	}

	unclosed := filepath.Join(root, "незакрытый")
	if err := os.MkdirAll(unclosed, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRule(t, unclosed, "правило.md", "---\nstages: [planning]\n\nБез закрывающей строки.\n")
	if _, err := st.Load("незакрытый"); err == nil {
		t.Fatal("незакрытый frontmatter должен быть ошибкой, а не молча съеденным правилом")
	}

	if p, err := st.Load(""); err != nil || p != nil {
		t.Fatal("пустой id — это «без инвариантов»")
	}
	if _, err := st.Load("нет-такого"); err == nil {
		t.Fatal("несуществующий набор — ошибка")
	}
}

func TestRepoSetIsValid(t *testing.T) {
	// Набор в репозитории — часть сдачи дня: если он перестанет читаться,
	// демо развалится на записи, а не в тестах.
	st := NewFileStore(filepath.Join("..", "..", "invariants"))
	ids, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 {
		t.Fatal("в репозитории нет ни одного набора инвариантов")
	}
	for _, id := range ids {
		s, err := st.Load(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if Block(s.List(), "") == "" {
			t.Fatalf("%s: пустой блок для промпта", id)
		}
	}
}
