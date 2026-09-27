package budget

// Три инструмента и цепочка между ними.
//
// Задание дня — пайплайн: первый инструмент получает данные, второй
// обрабатывает, третий сохраняет. Цепочку строит не сервер и не код
// агента, а модель: получила результат search_transactions и сама
// сформулировала вызов summarize_transactions. Ведущий курса подтвердил
// именно такое понимание.
//
// Отсюда главный вопрос дня — «корректность передачи данных между
// инструментами». Передаёт их модель, и передать может двумя способами:
//
//   - по ссылке: search отдаёт dataset_id, summarize принимает его,
//     а сами операции остаются на сервере;
//   - по значению: search отдаёт операции целиком, модель переписывает их
//     в аргументы summarize.
//
// Во втором случае модель — копировальщик, и копировальщик может
// ошибиться: потерять строку, округлить сумму, «поправить» знак возврата.
// Сервер поэтому сверяет всё, что к нему пришло, с тем, что он сам отдал,
// и пишет результат сверки в ответ. Какой способ надёжнее и сколько стоит
// каждый — меряет сценарий дня.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/safronov-a1exander/advent/internal/mcp"
)

// Version — версия сервера в ответе на initialize.
const Version = "day-19"

// Handoff — как данные идут от search к summarize.
type Handoff string

const (
	ByRef   Handoff = "ref"
	ByValue Handoff = "value"
)

// Options — настройки сервера.
type Options struct {
	Handoff    Handoff
	ReportsDir string
	Now        func() time.Time
}

type state struct {
	mu        sync.Mutex
	txs       []Tx
	opts      Options
	datasets  map[string][]Tx
	order     []string
	summaries map[string]Summary
	sumOrder  []string
}

// Summary — результат обработки: то, что должно оказаться в отчёте.
type Summary struct {
	ID         string             `json:"summary_id"`
	Count      int                `json:"count"`
	Spent      float64            `json:"spent_rub"`
	Refunds    float64            `json:"refunds_rub"`
	Net        float64            `json:"net_rub"`
	ByCategory map[string]float64 `json:"by_category_rub"`
	Foreign    map[string]float64 `json:"foreign"`
	Largest    *Tx                `json:"largest,omitempty"`
	// Audit — сверка входа с выдачей search: ровно ли то пришло.
	Audit string `json:"handoff_audit"`
}

// New — сервер «Бюджет» над операциями txs.
func New(txs []Tx, o Options) *mcp.Server {
	if o.Handoff == "" {
		o.Handoff = ByRef
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.ReportsDir == "" {
		o.ReportsDir = "reports/budget"
	}
	st := &state{txs: txs, opts: o, datasets: map[string][]Tx{}, summaries: map[string]Summary{}}
	s := mcp.NewServer("budget", Version,
		"Выписка по карте пользователя. Чтобы ответить про траты: search_transactions → summarize_transactions "+
			"→ при просьбе сохранить — save_report. Цифры бери из инструментов, не считай в уме.")
	t, f := true, false

	searchDesc := "Найти операции в выписке по карте. Все фильтры необязательны и складываются. "
	if o.Handoff == ByRef {
		searchDesc += "Возвращает найденные операции и dataset_id — его передают в summarize_transactions."
	} else {
		searchDesc += "Возвращает найденные операции JSON-массивом — его целиком передают в summarize_transactions."
	}
	s.Register(mcp.Tool{
		Name: "search_transactions", Title: "Поиск операций", Description: searchDesc,
		InputSchema: mcp.Schema(
			mcp.Prop{Name: "query", Type: "string", Description: "подстрока в названии продавца, например YANDEX или WILDBERRIES"},
			mcp.Prop{Name: "category", Type: "string", Description: "категория",
				Enum: []string{"кафе", "транспорт", "продукты", "покупки", "здоровье", "подписки", "перевод", "наличные"}},
			mcp.Prop{Name: "from", Type: "string", Description: "с даты ГГГГ-ММ-ДД включительно"},
			mcp.Prop{Name: "to", Type: "string", Description: "по дату ГГГГ-ММ-ДД включительно"},
		),
		Annotations: &mcp.Annotations{ReadOnlyHint: &t},
	}, st.search)

	var sumSchema json.RawMessage
	sumDesc := "Обработать найденные операции: сумма списаний, возвратов и итог в рублях, разбивка по категориям, " +
		"крупнейшая операция; операции в валюте — отдельно, без пересчёта. "
	if o.Handoff == ByRef {
		sumDesc += "Принимает dataset_id из search_transactions."
		sumSchema = mcp.Schema(mcp.Prop{Name: "dataset_id", Type: "string", Required: true, Description: "id выборки из search_transactions"})
	} else {
		sumDesc += "Принимает операции ровно в том виде, в каком их вернул search_transactions."
		sumSchema = mcp.Schema(mcp.Prop{Name: "transactions", Type: "array", Required: true,
			Description: "операции из search_transactions без изменений",
			Items:       json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"date":{"type":"string"},"merchant":{"type":"string"},"amount":{"type":"number"},"currency":{"type":"string"},"category":{"type":"string"}},"required":["id","amount","currency"]}`)})
	}
	s.Register(mcp.Tool{
		Name: "summarize_transactions", Title: "Сводка по операциям", Description: sumDesc,
		InputSchema: sumSchema, Annotations: &mcp.Annotations{ReadOnlyHint: &t},
	}, st.summarize)

	s.Register(mcp.Tool{
		Name: "save_report", Title: "Сохранить отчёт",
		Description: "Сохранить отчёт в файл markdown. Содержимое пишет ассистент по результатам summarize_transactions. " +
			"Возвращает путь к файлу.",
		InputSchema: mcp.Schema(
			mcp.Prop{Name: "title", Type: "string", Required: true, Description: "заголовок отчёта"},
			mcp.Prop{Name: "content", Type: "string", Required: true, Description: "текст отчёта в markdown"},
		),
		Annotations: &mcp.Annotations{ReadOnlyHint: &f, DestructiveHint: &f},
	}, st.save)
	return s
}

// ---- search ----

func (st *state) search(_ context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct {
		Query, Category, From, To string
	}](raw)
	if bad != nil {
		return *bad
	}
	for _, d := range []string{a.From, a.To} {
		if d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return mcp.ErrorResult("дата %q не в формате ГГГГ-ММ-ДД", d)
			}
		}
	}
	var found []Tx
	for _, t := range st.txs {
		switch {
		case a.Query != "" && !strings.Contains(strings.ToUpper(t.Merchant), strings.ToUpper(strings.TrimSpace(a.Query))):
		case a.Category != "" && !strings.EqualFold(t.Category, strings.TrimSpace(a.Category)):
		case a.From != "" && t.Date < a.From:
		case a.To != "" && t.Date > a.To:
		default:
			found = append(found, t)
		}
	}
	st.mu.Lock()
	id := fmt.Sprintf("ds-%d", len(st.order)+1)
	st.datasets[id] = found
	st.order = append(st.order, id)
	st.mu.Unlock()

	if len(found) == 0 {
		return mcp.TextResult("Операций по этим условиям нет. dataset_id: " + id + " (пустая выборка).")
	}
	if st.opts.Handoff == ByValue {
		b, _ := json.Marshal(found)
		r := mcp.TextResult(fmt.Sprintf("Найдено операций: %d. Передай этот массив в summarize_transactions без изменений:\n%s", len(found), b))
		return r
	}
	var lines []string
	for _, t := range found {
		lines = append(lines, fmt.Sprintf("%s %s %s %s %s · %s", t.ID, t.Date, t.Merchant, money(t.Amount), t.Currency, t.Category))
	}
	r := mcp.TextResult(fmt.Sprintf("Найдено операций: %d. dataset_id: %s — передай его в summarize_transactions.\n%s",
		len(found), id, strings.Join(lines, "\n")))
	r.StructuredContent, _ = json.Marshal(map[string]any{"dataset_id": id, "count": len(found)})
	return r
}

// ---- summarize ----

func (st *state) summarize(_ context.Context, raw json.RawMessage) mcp.CallResult {
	var in []Tx
	var audit string
	if st.opts.Handoff == ByRef {
		a, bad := mcp.Args[struct {
			DatasetID string `json:"dataset_id"`
		}](raw)
		if bad != nil {
			return *bad
		}
		st.mu.Lock()
		ds, ok := st.datasets[strings.TrimSpace(a.DatasetID)]
		st.mu.Unlock()
		if !ok {
			return mcp.ErrorResult("выборки %q нет: сначала search_transactions, потом его dataset_id", a.DatasetID)
		}
		in = ds
		audit = "по ссылке " + a.DatasetID + ": операции взяты с сервера, без расхождений"
	} else {
		a, bad := mcp.Args[struct {
			Transactions []Tx `json:"transactions"`
		}](raw)
		if bad != nil {
			return *bad
		}
		if len(a.Transactions) == 0 {
			return mcp.ErrorResult("не переданы операции: нужен массив transactions из search_transactions")
		}
		in = a.Transactions
		audit = st.audit(in)
	}
	sum := summarize(in)
	sum.Audit = audit
	st.mu.Lock()
	sum.ID = fmt.Sprintf("sum-%d", len(st.sumOrder)+1)
	st.summaries[sum.ID] = sum
	st.sumOrder = append(st.sumOrder, sum.ID)
	st.mu.Unlock()

	var cats []string
	for _, c := range sortedKeys(sum.ByCategory) {
		cats = append(cats, fmt.Sprintf("%s %s", c, money(sum.ByCategory[c])))
	}
	text := fmt.Sprintf("Сводка %s по %d операциям: списания %s RUB, возвраты %s RUB, итого %s RUB. По категориям: %s.",
		sum.ID, sum.Count, money(sum.Spent), money(sum.Refunds), money(sum.Net), strings.Join(cats, ", "))
	if len(sum.Foreign) > 0 {
		var fx []string
		for _, c := range sortedKeys(sum.Foreign) {
			fx = append(fx, money(sum.Foreign[c])+" "+c)
		}
		text += " В валюте, без пересчёта в рубли: " + strings.Join(fx, ", ") + "."
	}
	if sum.Largest != nil {
		text += fmt.Sprintf(" Крупнейшая: %s %s %s.", sum.Largest.Merchant, money(sum.Largest.Amount), sum.Largest.Currency)
	}
	text += " Сверка входа: " + audit + "."
	r := mcp.TextResult(text)
	r.StructuredContent, _ = json.Marshal(sum)
	return r
}

func summarize(in []Tx) Summary {
	s := Summary{Count: len(in), ByCategory: map[string]float64{}, Foreign: map[string]float64{}}
	for i, t := range in {
		if !strings.EqualFold(t.Currency, "RUB") {
			s.Foreign[strings.ToUpper(t.Currency)] += t.Amount
			continue
		}
		if t.Amount >= 0 {
			s.Spent += t.Amount
		} else {
			s.Refunds += -t.Amount
		}
		cat := t.Category
		if cat == "" {
			cat = categoryOf(t.Merchant)
		}
		s.ByCategory[cat] += t.Amount
		if s.Largest == nil || t.Amount > s.Largest.Amount {
			s.Largest = &in[i]
		}
	}
	s.Net = s.Spent - s.Refunds
	return s
}

// audit сверяет операции, переписанные моделью, с тем, что отдавал search:
// берёт выборку с наибольшим совпадением id и считает расхождения.
func (st *state) audit(in []Tx) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	best, bestHits := "", -1
	for _, id := range st.order {
		hits := 0
		for _, t := range st.datasets[id] {
			for _, x := range in {
				if x.ID == t.ID {
					hits++
					break
				}
			}
		}
		if hits > bestHits {
			best, bestHits = id, hits
		}
	}
	if best == "" {
		return "search ещё не вызывался — сверять не с чем"
	}
	src := st.datasets[best]
	byID := map[string]Tx{}
	for _, t := range src {
		byID[t.ID] = t
	}
	var lost, changed, extra int
	seen := map[string]bool{}
	for _, x := range in {
		t, ok := byID[x.ID]
		switch {
		case !ok:
			extra++
		case math.Abs(t.Amount-x.Amount) > 0.001 || !strings.EqualFold(t.Currency, x.Currency):
			changed++
		}
		seen[x.ID] = true
	}
	for _, t := range src {
		if !seen[t.ID] {
			lost++
		}
	}
	if lost == 0 && changed == 0 && extra == 0 {
		return fmt.Sprintf("по значению, с выдачей %s совпали все %d операций, без расхождений", best, len(src))
	}
	return fmt.Sprintf("по значению, с выдачей %s РАСХОЖДЕНИЕ: потеряно %d, изменено %d, лишних %d из %d", best, lost, changed, extra, len(src))
}

// ---- save ----

var slugRe = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func (st *state) save(_ context.Context, raw json.RawMessage) mcp.CallResult {
	a, bad := mcp.Args[struct{ Title, Content string }](raw)
	if bad != nil {
		return *bad
	}
	if strings.TrimSpace(a.Content) == "" {
		return mcp.ErrorResult("пустой отчёт: нечего сохранять")
	}
	title := strings.TrimSpace(a.Title)
	if title == "" {
		title = "отчёт"
	}
	// Имя файла строит сервер, а не модель: из заголовка остаются только
	// буквы и цифры, и файл не может уйти из каталога отчётов, что бы
	// модель ни написала в title — хоть «../../.env».
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if r := []rune(slug); len(r) > 60 {
		slug = string(r[:60])
	}
	if slug == "" {
		slug = "report"
	}
	stamp := slug + "-" + st.opts.Now().Format("20060102-150405")
	body := "# " + title + "\n\n" + strings.TrimSpace(a.Content) + "\n"
	if err := os.MkdirAll(st.opts.ReportsDir, 0o755); err != nil {
		return mcp.ErrorResult("не создать каталог отчётов: %v", err)
	}
	// Файл создаётся только новым (O_EXCL): два отчёта с одним заголовком
	// в одну секунду — обычное дело, когда сервером пользуются двое, и
	// второй не должен молча затереть первый.
	var path string
	for n := 1; ; n++ {
		name := stamp + ".md"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.md", stamp, n)
		}
		path = filepath.Join(st.opts.ReportsDir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, os.ErrExist) && n < 100 {
			continue
		}
		if err != nil {
			return mcp.ErrorResult("не записать отчёт: %v", err)
		}
		_, err = f.WriteString(body)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return mcp.ErrorResult("не записать отчёт: %v", err)
		}
		break
	}
	sum := sha256.Sum256([]byte(body))
	check := st.reportCheck(a.Content)
	r := mcp.TextResult(fmt.Sprintf("Отчёт сохранён: %s (%d байт, sha256 %s). Сверка с последней сводкой: %s.",
		filepath.ToSlash(path), len(body), hex.EncodeToString(sum[:4]), check))
	r.StructuredContent, _ = json.Marshal(map[string]any{"path": filepath.ToSlash(path), "bytes": len(body), "check": check})
	return r
}

// reportCheck — есть ли в тексте отчёта итог последней сводки. Третий шаг
// цепочки тоже передача данных: модель переписывает цифры сводки в отчёт.
func (st *state) reportCheck(content string) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.sumOrder) == 0 {
		return "сводки не было — отчёт написан без summarize_transactions"
	}
	s := st.summaries[st.sumOrder[len(st.sumOrder)-1]]
	norm := strings.NewReplacer(" ", "", " ", "", " ", "", ",", ".").Replace(content)
	for _, v := range []string{money(s.Net), fmt.Sprintf("%.0f", s.Net)} {
		if strings.Contains(norm, strings.ReplaceAll(v, " ", "")) {
			return fmt.Sprintf("итог %s RUB из %s в отчёте есть", money(s.Net), s.ID)
		}
	}
	return fmt.Sprintf("итога %s RUB из %s в отчёте НЕТ", money(s.Net), s.ID)
}

// money — сумма без копеек, если их нет: «1390», «51940.5».
func money(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	return strings.TrimSuffix(strings.TrimRight(strings.TrimRight(s, "0"), "."), ".")
}

func sortedKeys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
