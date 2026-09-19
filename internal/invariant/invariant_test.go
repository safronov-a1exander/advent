package invariant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rules() []Invariant {
	return []Invariant{
		{Name: "стек", Rule: "только Go", Forbid: []string{"python", "node.js"}},
		{Name: "база", Rule: "только PostgreSQL",
			Domain: []string{"субд", "база данных", "mongodb"}, Allow: []string{"postgresql", "postgres"}},
		{Name: "без кода в планировании", Rule: "на планировании кода нет",
			Forbid: []string{"```go"}, Stages: []string{"planning"}},
		// Правило без фильтра — совершенно нормальное правило: подстрокой
		// «срок с оговоркой» не выразишь.
		{Name: "сроки", Rule: "срок только с оговоркой"},
	}
}

func TestFilterCatchesObviousViolations(t *testing.T) {
	vs := Filter("Возьмём Python и FastAPI", rules(), "")
	if len(vs) != 1 || vs[0].Name != "стек" {
		t.Fatalf("фильтр не сработал: %+v", vs)
	}
	if !strings.Contains(vs[0].Found, "python") {
		t.Fatalf("непонятно, что нашлось: %q", vs[0].Found)
	}
	if len(Filter("Возьмём Go и стандартную библиотеку", rules(), "")) != 0 {
		t.Fatal("нормальный ответ забракован")
	}
}

func TestRuleWithoutFilterIsSkippedByFilter(t *testing.T) {
	// Главное свойство новой модели: фильтр — это отсев, а не проверка.
	// Правило без фильтра он не смотрит вовсе, и это нормально.
	list := []Invariant{{Name: "сроки", Rule: "срок только с оговоркой"}}
	if list[0].HasFilter() {
		t.Fatal("у правила без списков фильтра нет")
	}
	if len(Filter("Сделаем за две недели.", list, "")) != 0 {
		t.Fatal("фильтр не должен ничего решать про правило без фильтра")
	}
	// Но смысловой проверке оно уходит.
	if !NeedsJudge(list, "") {
		t.Fatal("правило без фильтра обязано попадать в смысловую проверку")
	}
	if !strings.Contains(JudgePrompt(list, "", "Сделаем за две недели."), "сроки") {
		t.Fatal("правило не попало в запрос смысловой проверки")
	}
}

func TestJudgeSeesEveryRuleIncludingFiltered(t *testing.T) {
	// Пустой фильтр не значит «нарушения нет». Поэтому смысловой проверке
	// уходят все правила, а не только те, у которых фильтра нет.
	p := JudgePrompt(rules(), "", "какой-то ответ")
	for _, want := range []string{"стек", "база", "сроки"} {
		if !strings.Contains(p, want) {
			t.Fatalf("правило %q не ушло в смысловую проверку:\n%s", want, p)
		}
	}
	// Правило чужой стадии не уходит: проверять то, что сейчас не действует,
	// значит платить за ложные срабатывания. (Без задачи стадии нет вовсе,
	// и тогда действуют все правила — поэтому стадию задаём явно.)
	if strings.Contains(JudgePrompt(rules(), "execution", "ответ"), "на планировании кода нет") {
		t.Fatal("правило стадии planning ушло в проверку на стадии execution")
	}
}

func TestFilterRespectsWordBoundaries(t *testing.T) {
	// Иначе «Go» находилось бы в «Google», а фильтр срабатывал бы
	// на ровном месте.
	list := []Invariant{{Name: "нет Go", Rule: "без Go", Forbid: []string{"Go"}}}
	if len(Filter("Слоты берём из Google Calendar", list, "")) != 0 {
		t.Fatal("«Go» нашлось внутри «Google»")
	}
	if len(Filter("Пишем на go, как договорились", list, "")) != 1 {
		t.Fatal("отдельное слово должно ловиться")
	}
}

func TestFilterDoesNotPunishRefusal(t *testing.T) {
	// Самый важный тест дня. Ассистент, которому запретили ЮKassa,
	// отказывается словами «ЮKassa подключать не будем» — и без unless
	// попадает под собственный запрет. На живом прогоне так и вышло:
	// фильтр забраковал корректный отказ, повтор сгорел впустую.
	list := []Invariant{{
		Name: "платежи", Rule: "приём платежей не делаем",
		Forbid: []string{"юkassa", "stripe"},
		Unless: []string{"не будем", "не подключаем", "нельзя", "не делаем"},
	}}

	refusal := "ЮKassa подключать не будем: в проекте только бронь. Оплату клиент делает на месте."
	if vs := Filter(refusal, list, ""); len(vs) != 0 {
		t.Fatalf("корректный отказ забракован: %+v", vs)
	}
	breach := "Подключим ЮKassa, это полдня работы. Токены храним в Vault."
	if vs := Filter(breach, list, ""); len(vs) != 1 {
		t.Fatalf("настоящее нарушение не поймано: %+v", vs)
	}
	// Отказ в одном предложении не должен прикрывать нарушение в другом.
	mixed := "ЮKassa подключать не будем. Но можно быстро прикрутить Stripe."
	if vs := Filter(mixed, list, ""); len(vs) != 1 {
		t.Fatalf("нарушение в соседнем предложении не поймано: %+v", vs)
	}
}

func TestAllowChecksChoiceNotMention(t *testing.T) {
	// Ответ вообще не про базу — фильтру нечего делать.
	if len(Filter("Добавим хендлер записи", rules(), "")) != 0 {
		t.Fatal("фильтр allow сработал там, где о базе речи нет")
	}
	vs := Filter("Возьмём MongoDB, она гибче", rules(), "")
	if len(vs) != 1 || vs[0].Name != "база" {
		t.Fatalf("выбор чужой СУБД не пойман: %+v", vs)
	}
	if len(Filter("База данных — PostgreSQL", rules(), "")) != 0 {
		t.Fatal("разрешённая СУБД забракована")
	}
}

func TestRequireNeedsDomain(t *testing.T) {
	list := []Invariant{{
		Name: "срок с оговоркой", Rule: "срок только с условием",
		Domain: []string{"недел", "срок"}, Require: []string{"если", "при условии", "зависит"},
	}}
	if len(Filter("Добавим индекс на slot_id", list, "")) != 0 {
		t.Fatal("require сработало без domain")
	}
	if len(Filter("Срок — две недели", list, "")) != 1 {
		t.Fatal("срок без оговорки должен ловиться")
	}
	if len(Filter("Срок — две недели, если Calendar отдаёт слоты", list, "")) != 0 {
		t.Fatal("срок с оговоркой забракован")
	}
}

func TestStagesLimitWhereRuleApplies(t *testing.T) {
	answer := "```go\nfunc main() {}\n```"
	if len(Filter(answer, rules(), "planning")) == 0 {
		t.Fatal("на планировании кода быть не должно")
	}
	if len(Filter(answer, rules(), "execution")) != 0 {
		t.Fatal("правило стадии planning сработало на execution")
	}
	if len(Filter(answer, rules(), "")) == 0 {
		t.Fatal("без стадии правило должно действовать")
	}
}

func TestBlockAndExplainShareWording(t *testing.T) {
	// Формулировка в промпте и в объяснении отказа должна быть одна:
	// иначе пользователь получит два разных правила.
	b := Block(rules(), "")
	if !strings.Contains(b, "только Go") || !strings.Contains(b, "только PostgreSQL") {
		t.Fatalf("блок не содержит формулировок:\n%s", b)
	}
	if strings.Contains(Block(rules(), "execution"), "на планировании кода нет") {
		t.Fatal("правило чужой стадии попало в промпт")
	}

	vs := Filter("Возьмём Python", rules(), "")
	e := Explain(vs)
	if !strings.Contains(e, "только Go") {
		t.Fatalf("в объяснении отказа нет формулировки правила:\n%s", e)
	}
	if !strings.Contains(e, "откажись") {
		t.Fatalf("объяснение не требует отказа:\n%s", e)
	}
	if Explain(nil) != "" {
		t.Fatal("без нарушений объяснять нечего")
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
	cases := []Invariant{
		{Rule: "x", Forbid: []string{"y"}},                 // без имени
		{Name: "a", Forbid: []string{"y"}},                 // пустое тело файла
		{Name: "a", Rule: "x", Allow: []string{"б"}},       // allow без domain
		{Name: "a", Rule: "x", Require: []string{"б"}},     // require без domain
		{Name: "a", Rule: "x", Unless: []string{"нельзя"}}, // unless без forbid
		{Name: "a", Rule: "x", Domain: []string{"б"}},      // domain без фильтра
	}
	for i, c := range cases {
		if err := c.Validate(); err == nil {
			t.Fatalf("случай %d должен был отвергнуться: %+v", i, c)
		}
	}
	// А правило из одного текста — валидно, и это самый обычный случай.
	if err := (Invariant{Name: "сроки", Rule: "срок только с оговоркой"}).Validate(); err != nil {
		t.Fatalf("правило без фильтра должно быть валидным: %v", err)
	}
}

func TestParseJudgeDropsUnknownRules(t *testing.T) {
	list := []Invariant{{Name: "сроки", Rule: "срок с оговоркой"}}
	vs, err := ParseJudge(`{"violations":[{"name":"сроки","why":"две недели без условий"},{"name":"выдуманное","why":"мимо"}]}`, list, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Name != "сроки" || !vs[0].ByJudge {
		t.Fatalf("разбор ответа проверяющего: %+v", vs)
	}
	if vs[0].Rule != "срок с оговоркой" {
		t.Fatal("формулировка должна браться из набора, а не от модели")
	}
	if vs, err := ParseJudge(`{"violations":[]}`, list, ""); err != nil || len(vs) != 0 {
		t.Fatalf("пустой список — норма: %v %v", vs, err)
	}
	if _, err := ParseJudge("нарушений нет!", list, ""); err == nil {
		t.Fatal("не-JSON должен быть ошибкой")
	}
}

// writeRule — правило-файл в каталоге набора.
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
	writeRule(t, dir, "стек.md", "---\nforbid: [python]\nunless: [нельзя]\n---\n\nБэкенд только на Go: Python нельзя.\n")
	// Правило без frontmatter — весь файл целиком текст правила.
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

	// Имя берётся из имени файла, текст — из тела.
	byName := map[string]Invariant{}
	for _, inv := range set.Invariants {
		byName[inv.Name] = inv
	}
	stack, ok := byName["стек"]
	if !ok || !strings.Contains(stack.Rule, "только на Go") || len(stack.Forbid) != 1 {
		t.Fatalf("правило со frontmatter: %+v", stack)
	}
	terms, ok := byName["сроки"]
	if !ok || terms.HasFilter() || !strings.Contains(terms.Rule, "оговоркой") {
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
	empty := filepath.Join(root, "пустой")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	st := NewFileStore(root)
	if _, err := st.Load("пустой"); err == nil {
		t.Fatal("набор без правил должен отвергаться")
	}

	bad := filepath.Join(root, "кривой")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRule(t, bad, "правило.md", "---\nallow: [postgres]\n---\n\nТолько Postgres.\n")
	if _, err := st.Load("кривой"); err == nil || !strings.Contains(err.Error(), "domain") {
		t.Fatalf("allow без domain должен отвергаться при чтении: %v", err)
	}

	unclosed := filepath.Join(root, "незакрытый")
	if err := os.MkdirAll(unclosed, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRule(t, unclosed, "правило.md", "---\nforbid: [x]\n\nБез закрывающей строки.\n")
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
		// У каждого forbid-правила должен быть unless: иначе оно поймает
		// собственный отказ. Это не вкусовщина, это баг первого прогона.
		for _, inv := range s.List() {
			if len(inv.Forbid) > 0 && len(inv.Unless) == 0 && !strings.Contains(inv.Name, "код") {
				t.Fatalf("%s/%s: forbid без unless поймает корректный отказ", id, inv.Name)
			}
		}
	}
}
