// Package memory — модель памяти агента (день 11).
//
// На девятом и десятом днях мы разводили память и контекст: память — вся
// история разговора, контекст — то, что из неё уходит в модель. Этого мало.
// История одного разговора умирает вместе с разговором, а агент должен
// помнить и то, что переживает разговор: чем занята задача и кто вообще
// его собеседник.
//
// Поэтому память здесь разложена на три слоя с разным временем жизни:
//
//   - chat scope (краткосрочная) — текущий диалог. История сообщений плюс
//     заметки, нужные только здесь и сейчас. Живёт вместе с разговором и
//     умирает по Ctrl+R;
//   - task scope (рабочая) — данные текущей задачи: требования, решения,
//     открытые вопросы. Переживает разговор: задачу можно продолжить
//     завтра в новом разговоре;
//   - user scope (долговременная) — профиль, предпочтения, знания о
//     собеседнике. Переживает и задачу: у пользователя их много.
//
// Слои хранятся раздельно — файл на задачу, файл на пользователя, — и
// раскладка по ним явная: либо пользователь сам говорит, куда положить,
// либо служебный вызов раскладывает (router.go), но в обоих случаях видно,
// что и куда легло.
//
// Слои ортогональны стратегиям контекста дней 9–10: стратегия решает, как
// ужать историю (chat scope), а task и user уходят в системный промпт
// отдельными блоками независимо от неё.
package memory

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Scope — слой памяти. Имена совпадают с тем, как их называл курс:
// долговременная — user scope, рабочая — task scope, сессионная — chat scope.
type Scope string

const (
	// ScopeChat — краткосрочная: текущий диалог.
	ScopeChat Scope = "chat"
	// ScopeTask — рабочая: данные текущей задачи.
	ScopeTask Scope = "task"
	// ScopeUser — долговременная: профиль, решения, знания.
	ScopeUser Scope = "user"
)

// Scopes — порядок слоёв от короткоживущего к долгоживущему.
// В этом же порядке они показываются и перебираются в интерфейсе.
var Scopes = []Scope{ScopeChat, ScopeTask, ScopeUser}

// Valid — известен ли такой слой.
func (s Scope) Valid() bool {
	switch s {
	case ScopeChat, ScopeTask, ScopeUser:
		return true
	}
	return false
}

// Label — имя слоя для показа.
func (s Scope) Label() string {
	switch s {
	case ScopeChat:
		return "краткосрочная"
	case ScopeTask:
		return "рабочая"
	case ScopeUser:
		return "долговременная"
	}
	return string(s)
}

// Hint — чем слой отличается; подпись под панелью.
func (s Scope) Hint() string {
	switch s {
	case ScopeChat:
		return "текущий диалог: живёт вместе с разговором, уходит по Ctrl+R"
	case ScopeTask:
		return "текущая задача: переживает разговор, задачу можно продолжить в новом"
	case ScopeUser:
		return "профиль и знания о собеседнике: переживают и разговор, и задачу"
	}
	return ""
}

// Source — откуда взялась запись. Различать важно: раскладка служебным
// вызовом ошибается, и по метке видно, что править руками.
type Source string

const (
	// SourceManual — положил пользователь.
	SourceManual Source = "вручную"
	// SourceAuto — разложил служебный вызов.
	SourceAuto Source = "агент"
)

// Entry — одна запись памяти: короткий ключ и значение.
//
// Формат тот же, что у фактов дня 10, и это намеренно: факты оказались
// удобной единицей — ключ либо остаётся как был, либо заменяется целиком,
// и пересказ не копит искажений. Здесь к ключу добавлен слой.
type Entry struct {
	Key    string    `json:"key"`
	Value  string    `json:"value"`
	Source Source    `json:"source,omitempty"`
	At     time.Time `json:"at,omitempty"`
}

// Layer — один слой памяти: записи в порядке добавления.
//
// Порядок важен по той же причине, что и у фактов: слой идёт в системный
// промпт, и если ключи будут каждый раз в новом порядке, кэш префикса
// у провайдера перестанет работать на ровном месте.
type Layer struct {
	scope   Scope
	entries []Entry
	dirty   bool
}

// NewLayer — пустой слой.
func NewLayer(scope Scope) *Layer { return &Layer{scope: scope} }

// Scope — какой это слой.
func (l *Layer) Scope() Scope { return l.scope }

// Entries — копия записей в порядке добавления.
func (l *Layer) Entries() []Entry {
	if l == nil {
		return nil
	}
	return append([]Entry(nil), l.entries...)
}

// Len — сколько записей в слое.
func (l *Layer) Len() int {
	if l == nil {
		return 0
	}
	return len(l.entries)
}

// Dirty — менялся ли слой с последнего сохранения.
func (l *Layer) Dirty() bool { return l != nil && l.dirty }

// Clean снимает пометку об изменении; зовёт хранилище после записи.
func (l *Layer) Clean() {
	if l != nil {
		l.dirty = false
	}
}

// Put кладёт запись. Существующий ключ заменяется целиком — как у фактов:
// склейка старого и нового значения быстро превращается в кашу.
// Пустое значение удаляет ключ: так пользователь снимает устаревшее.
func (l *Layer) Put(e Entry) {
	key := strings.TrimSpace(e.Key)
	val := strings.TrimSpace(e.Value)
	if key == "" {
		return
	}
	if val == "" {
		l.Delete(key)
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	e.Key, e.Value = key, val
	for i := range l.entries {
		if strings.EqualFold(l.entries[i].Key, key) {
			if l.entries[i].Value == val && l.entries[i].Source == e.Source {
				return // ничего не изменилось — не трогаем и не помечаем грязным
			}
			// Написание ключа остаётся прежним: «Срок» и «срок» — один
			// и тот же ключ, и менять его написание значит менять текст
			// системного промпта на ровном месте, а с ним и кэш префикса.
			e.Key = l.entries[i].Key
			l.entries[i] = e
			l.dirty = true
			return
		}
	}
	l.entries = append(l.entries, e)
	l.dirty = true
}

// Get — значение по ключу.
func (l *Layer) Get(key string) (Entry, bool) {
	if l == nil {
		return Entry{}, false
	}
	for _, e := range l.entries {
		if strings.EqualFold(e.Key, key) {
			return e, true
		}
	}
	return Entry{}, false
}

// Delete убирает ключ.
func (l *Layer) Delete(key string) bool {
	if l == nil {
		return false
	}
	for i, e := range l.entries {
		if strings.EqualFold(e.Key, key) {
			l.entries = append(l.entries[:i], l.entries[i+1:]...)
			l.dirty = true
			return true
		}
	}
	return false
}

// Clear опустошает слой.
func (l *Layer) Clear() {
	if l == nil || len(l.entries) == 0 {
		return
	}
	l.entries = nil
	l.dirty = true
}

// Load заменяет содержимое слоя (чтение с диска). Грязным не помечает:
// прочитанное с диска на диск писать незачем.
func (l *Layer) Load(entries []Entry) {
	l.entries = append([]Entry(nil), entries...)
	l.dirty = false
}

// Block — слой для системного промпта. Пустой слой даёт пустую строку:
// заголовок без записей только съел бы токены и сбил модель.
func (l *Layer) Block() string {
	if l.Len() == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(blockTitle(l.scope))
	b.WriteString("\n")
	for _, e := range l.entries {
		fmt.Fprintf(&b, "- %s: %s\n", e.Key, e.Value)
	}
	return strings.TrimRight(b.String(), "\n")
}

// blockTitle — заголовок блока слоя в системном промпте. Заголовки разные
// не для красоты: модель должна понимать, что знание о пользователе
// действует всегда, а данные задачи — только пока задача та же.
func blockTitle(s Scope) string {
	switch s {
	case ScopeUser:
		return "Что известно о собеседнике (долговременная память, действует во всех разговорах):"
	case ScopeTask:
		return "Данные текущей задачи (рабочая память, действует, пока идёт эта задача):"
	case ScopeChat:
		return "Заметки по текущему разговору (краткосрочная память):"
	}
	return "Память:"
}

// Memory — три слоя одного агента.
//
// Слои живут по-разному, и это единственное, что их различает по коду:
// chat никуда не пишется (умирает с разговором), task и user привязаны
// к идентификаторам и сохраняются в хранилище.
type Memory struct {
	chat *Layer
	task *Layer
	user *Layer

	store  Store
	taskID string
	userID string
}

// New — память с пустыми слоями. store может быть nil: тогда task и user
// живут только в процессе (удобно в тестах и в сравнении вариантов).
func New(store Store, userID, taskID string) *Memory {
	return &Memory{
		chat:   NewLayer(ScopeChat),
		task:   NewLayer(ScopeTask),
		user:   NewLayer(ScopeUser),
		store:  store,
		userID: strings.TrimSpace(userID),
		taskID: strings.TrimSpace(taskID),
	}
}

// UserID — чей долговременный слой.
func (m *Memory) UserID() string { return m.userID }

// TaskID — какой задачи рабочий слой.
func (m *Memory) TaskID() string { return m.taskID }

// Layer — слой по имени; nil, если слой неизвестен.
func (m *Memory) Layer(s Scope) *Layer {
	if m == nil {
		return nil
	}
	switch s {
	case ScopeChat:
		return m.chat
	case ScopeTask:
		return m.task
	case ScopeUser:
		return m.user
	}
	return nil
}

// Put кладёт запись в слой и сразу сохраняет его, если слой хранимый.
// Сохранять сразу, а не в конце разговора, — то же решение, что на седьмом
// дне с разговорами: упавшее приложение не должно уносить с собой знание.
func (m *Memory) Put(s Scope, e Entry) error {
	l := m.Layer(s)
	if l == nil {
		return fmt.Errorf("неизвестный слой памяти %q", s)
	}
	l.Put(e)
	return m.saveLayer(s)
}

// Delete убирает ключ из слоя и сохраняет слой.
func (m *Memory) Delete(s Scope, key string) error {
	l := m.Layer(s)
	if l == nil {
		return fmt.Errorf("неизвестный слой памяти %q", s)
	}
	if !l.Delete(key) {
		return nil
	}
	return m.saveLayer(s)
}

// ClearChat стирает краткосрочный слой. Зовётся при сбросе разговора:
// заметки текущего диалога относятся к нему и вместе с ним уходят,
// а задача и пользователь остаются.
func (m *Memory) ClearChat() {
	if m != nil {
		m.chat.Clear()
		m.chat.Clean()
	}
}

// Restore поднимает хранимые слои из хранилища. Отсутствующий файл —
// не ошибка: у нового пользователя и новой задачи памяти просто нет.
func (m *Memory) Restore() error {
	if m == nil || m.store == nil {
		return nil
	}
	var errs []string
	if m.userID != "" {
		entries, err := m.store.Load(ScopeUser, m.userID)
		if err != nil {
			errs = append(errs, err.Error())
		} else {
			m.user.Load(entries)
		}
	}
	if m.taskID != "" {
		entries, err := m.store.Load(ScopeTask, m.taskID)
		if err != nil {
			errs = append(errs, err.Error())
		} else {
			m.task.Load(entries)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("память не поднялась: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Flush сохраняет все изменённые хранимые слои.
func (m *Memory) Flush() error {
	if m == nil || m.store == nil {
		return nil
	}
	var errs []string
	for _, s := range []Scope{ScopeUser, ScopeTask} {
		if err := m.saveLayer(s); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (m *Memory) saveLayer(s Scope) error {
	l := m.Layer(s)
	if m.store == nil || l == nil || !l.Dirty() {
		return nil
	}
	id := m.idOf(s)
	if id == "" {
		// Слой без идентификатора хранить негде. Это не ошибка: агент может
		// работать без задачи или без пользователя — тогда слой живёт
		// в процессе и уходит вместе с ним.
		return nil
	}
	if err := m.store.Save(s, id, l.Entries()); err != nil {
		return err
	}
	l.Clean()
	return nil
}

func (m *Memory) idOf(s Scope) string {
	switch s {
	case ScopeUser:
		return m.userID
	case ScopeTask:
		return m.taskID
	}
	return ""
}

// Blocks — блоки слоёв для системного промпта в порядке от долговременного
// к краткосрочному: сначала кто собеседник, потом чем заняты, потом мелочи
// текущего разговора.
//
// only ограничивает набор слоёв; пустой список означает все. Это прямой
// ответ на антипаттерн «всё в один промпт»: слой, который в этой задаче
// ничего не решает, можно не отправлять.
func (m *Memory) Blocks(only ...Scope) []string {
	if m == nil {
		return nil
	}
	want := map[Scope]bool{}
	for _, s := range only {
		want[s] = true
	}
	var out []string
	for _, s := range []Scope{ScopeUser, ScopeTask, ScopeChat} {
		if len(want) > 0 && !want[s] {
			continue
		}
		if b := m.Layer(s).Block(); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// Counts — сколько записей в каждом слое; для шапки и отчётов.
func (m *Memory) Counts() map[Scope]int {
	out := map[Scope]int{}
	for _, s := range Scopes {
		out[s] = m.Layer(s).Len()
	}
	return out
}

// Summary — однострочная сводка «слой: сколько» для шапки интерфейса.
func (m *Memory) Summary() string {
	var parts []string
	for _, s := range Scopes {
		if n := m.Layer(s).Len(); n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", s, n))
		}
	}
	if len(parts) == 0 {
		return "память пуста"
	}
	return strings.Join(parts, " · ")
}

// Keys — все ключи слоя, отсортированные; для подсказок и тестов.
func (m *Memory) Keys(s Scope) []string {
	l := m.Layer(s)
	if l == nil {
		return nil
	}
	out := make([]string, 0, l.Len())
	for _, e := range l.entries {
		out = append(out, e.Key)
	}
	sort.Strings(out)
	return out
}
