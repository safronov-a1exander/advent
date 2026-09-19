package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLayerReplacesKeyAndDropsOnEmpty(t *testing.T) {
	l := NewLayer(ScopeTask)
	l.Put(Entry{Key: "срок", Value: "6 недель"})
	l.Put(Entry{Key: "бюджет", Value: "150000"})
	l.Put(Entry{Key: "Срок", Value: "8 недель"}) // тот же ключ в другом регистре

	if l.Len() != 2 {
		t.Fatalf("в слое %d записей, ожидали 2: %+v", l.Len(), l.Entries())
	}
	if e, _ := l.Get("срок"); e.Value != "8 недель" {
		t.Fatalf("ключ не заменился: %q", e.Value)
	}
	// Порядок добавления сохраняется: блок слоя идёт в системный промпт,
	// и прыгающие ключи каждый ход сбивали бы кэш префикса.
	if got := l.Entries()[0].Key; got != "срок" {
		t.Fatalf("порядок сбился, первым идёт %q", got)
	}

	l.Put(Entry{Key: "бюджет", Value: "   "})
	if _, ok := l.Get("бюджет"); ok {
		t.Fatal("пустое значение должно удалять ключ")
	}
}

func TestLayerDirtyOnlyOnRealChange(t *testing.T) {
	l := NewLayer(ScopeUser)
	l.Put(Entry{Key: "стек", Value: "Go"})
	if !l.Dirty() {
		t.Fatal("новая запись должна помечать слой изменённым")
	}
	l.Clean()
	l.Put(Entry{Key: "стек", Value: "Go"})
	if l.Dirty() {
		t.Fatal("та же запись не должна вызывать перезапись файла")
	}
}

func TestBlocksSkipEmptyLayersAndKeepOrder(t *testing.T) {
	m := New(nil, "саша", "барбершоп")
	if got := m.Blocks(); got != nil {
		t.Fatalf("пустая память не должна давать блоков: %q", got)
	}

	_ = m.Put(ScopeChat, Entry{Key: "на чём остановились", Value: "на оплате"})
	_ = m.Put(ScopeUser, Entry{Key: "стек", Value: "Go"})
	_ = m.Put(ScopeTask, Entry{Key: "срок", Value: "6 недель"})

	blocks := m.Blocks()
	if len(blocks) != 3 {
		t.Fatalf("блоков %d, ожидали 3", len(blocks))
	}
	// Порядок: сначала кто собеседник, потом чем заняты, потом мелочи.
	for i, want := range []string{"собеседнике", "текущей задачи", "текущему разговору"} {
		if !strings.Contains(blocks[i], want) {
			t.Fatalf("блок %d — не %q: %q", i, want, blocks[i])
		}
	}

	// Ограничение набора слоёв — ответ на антипаттерн «всё в один промпт».
	only := m.Blocks(ScopeUser)
	if len(only) != 1 || !strings.Contains(only[0], "Go") {
		t.Fatalf("фильтр слоёв не сработал: %q", only)
	}
}

func TestChatLayerDiesWithConversationOthersSurvive(t *testing.T) {
	m := New(nil, "саша", "барбершоп")
	_ = m.Put(ScopeChat, Entry{Key: "на чём остановились", Value: "на оплате"})
	_ = m.Put(ScopeTask, Entry{Key: "срок", Value: "6 недель"})
	_ = m.Put(ScopeUser, Entry{Key: "имя", Value: "Саша"})

	m.ClearChat()

	if n := m.Layer(ScopeChat).Len(); n != 0 {
		t.Fatalf("краткосрочный слой пережил сброс: %d записей", n)
	}
	if m.Layer(ScopeTask).Len() != 1 || m.Layer(ScopeUser).Len() != 1 {
		t.Fatal("рабочий и долговременный слои не должны уходить со сбросом разговора")
	}
}

func TestFileStoreRoundTripAndSeparateFiles(t *testing.T) {
	dir := t.TempDir()
	st := NewFileStore(dir)

	m := New(st, "саша", "бот для барбершопа")
	if err := m.Put(ScopeUser, Entry{Key: "стек", Value: "Go", Source: SourceManual}); err != nil {
		t.Fatal(err)
	}
	if err := m.Put(ScopeTask, Entry{Key: "срок", Value: "6 недель", Source: SourceAuto}); err != nil {
		t.Fatal(err)
	}

	// Слои лежат раздельно — это и есть требование дня: разные типы памяти
	// хранятся отдельно.
	for _, p := range []string{
		filepath.Join(dir, "users", "саша.json"),
		filepath.Join(dir, "tasks", "бот-для-барбершопа.json"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("нет файла слоя %s: %v", p, err)
		}
	}

	// Новый разговор того же пользователя и той же задачи видит ту же память.
	again := New(st, "саша", "бот для барбершопа")
	if err := again.Restore(); err != nil {
		t.Fatal(err)
	}
	if e, ok := again.Layer(ScopeUser).Get("стек"); !ok || e.Value != "Go" {
		t.Fatalf("долговременный слой не поднялся: %+v", again.Layer(ScopeUser).Entries())
	}
	if e, ok := again.Layer(ScopeTask).Get("срок"); !ok || e.Source != SourceAuto {
		t.Fatalf("рабочий слой не поднялся или потерял источник: %+v", e)
	}

	// Другая задача того же пользователя: профиль на месте, рабочая память пуста.
	other := New(st, "саша", "другая задача")
	if err := other.Restore(); err != nil {
		t.Fatal(err)
	}
	if other.Layer(ScopeUser).Len() != 1 {
		t.Fatal("долговременный слой должен переживать смену задачи")
	}
	if other.Layer(ScopeTask).Len() != 0 {
		t.Fatal("рабочая память чужой задачи не должна подтягиваться")
	}
}

func TestChatLayerIsNotStoredSeparately(t *testing.T) {
	st := NewFileStore(t.TempDir())
	if _, err := st.Load(ScopeChat, "что-нибудь"); err == nil {
		t.Fatal("краткосрочный слой хранится вместе с разговором, отдельного файла у него нет")
	}
}

func TestSafeNameKeepsWritesInsideStore(t *testing.T) {
	dir := t.TempDir()
	st := NewFileStore(dir)
	if err := st.Save(ScopeUser, "../../сбежал", []Entry{{Key: "k", Value: "v"}}); err != nil {
		t.Fatal(err)
	}
	ids, err := st.List(ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || strings.Contains(ids[0], "..") {
		t.Fatalf("имя файла не обезврежено: %q", ids)
	}
}

func TestParsePlanDropsGarbage(t *testing.T) {
	p, err := ParsePlan("```json\n" + `{"remember":[
	    {"scope":"user","key":"имя","value":"Саша"},
	    {"scope":"склад","key":"мимо","value":"мимо"},
	    {"scope":"task","key":"","value":"без ключа"},
	    {"scope":"task","key":"срок","value":""}
	],"forget":[{"scope":"task","key":"бюджет"}]}` + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Remember) != 1 || p.Remember[0].Key != "имя" {
		t.Fatalf("мусор не отброшен: %+v", p.Remember)
	}
	if len(p.Forget) != 1 {
		t.Fatalf("forget разобран неверно: %+v", p.Forget)
	}
}

func TestParsePlanEmptyIsNotAnError(t *testing.T) {
	// Обычная реплика ничего не добавляет — и это норма, а не сбой:
	// раскладка возвращает разницу, а не весь набор.
	p, err := ParsePlan(`{"remember":[],"forget":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Empty() {
		t.Fatal("план должен быть пустым")
	}
}

func TestApplyDescribesChanges(t *testing.T) {
	m := New(nil, "саша", "задача")
	_ = m.Put(ScopeTask, Entry{Key: "срок", Value: "6 недель"})
	_ = m.Put(ScopeTask, Entry{Key: "бюджет", Value: "150000"})

	plan := Plan{
		Remember: []Change{
			{Scope: ScopeUser, Key: "имя", Value: "Саша"},
			{Scope: ScopeTask, Key: "срок", Value: "8 недель"},
		},
		Forget: []Change{
			{Scope: ScopeTask, Key: "бюджет"},
			{Scope: ScopeTask, Key: "которого нет"},
		},
	}
	got, err := m.Apply(plan, SourceAuto)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+ user/имя: Саша", "~ task/срок: 6 недель → 8 недель", "− task/бюджет"} {
		if !strings.Contains(got, want) {
			t.Fatalf("в описании изменений нет %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "которого нет") {
		t.Fatal("удаление несуществующего ключа не должно попадать в ленту")
	}
}
