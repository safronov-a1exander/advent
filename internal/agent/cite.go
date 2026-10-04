package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/safronov-a1exander/advent/internal/llm"
	"github.com/safronov-a1exander/advent/internal/rag"
)

// Цитаты, источники и «не знаю» (день 24).
//
// На двадцать третьем дне фильтр научился не пускать в запрос посторонние
// фрагменты — и выяснилось, что этого мало. Когда фильтр отсёк всё, вопрос
// ушёл к модели голым, и она выдумала ответ с числами. А когда фрагменты
// были, проверить, взят ли ответ из них, было нечем.
//
// Режим rag_cite закрывает обе дыры:
//
//   - модель отвечает не прозой, а JSON: ответ, id фрагментов, на которые
//     он опирается, и дословные цитаты из них;
//   - код проверяет то, что можно проверить без модели: цитата дословно
//     лежит в названном фрагменте, источники — из найденных, каждое число
//     ответа есть в цитатах;
//   - если после порога не осталось ни одного фрагмента, модель не
//     спрашивают вовсе: ответ «не знаю» с просьбой уточнить собирает код.
//     Уговорить модель не выдумывать — просьба; не дать ей слова — правило.

// Citation — разобранный ответ с источниками и итог проверки.
type Citation struct {
	// Known — модель нашла ответ во фрагментах. false — сказала «не знаю».
	Known  bool     `json:"known"`
	Answer string   `json:"answer"`
	IDs    []string `json:"sources"`
	Quotes []Quote  `json:"quotes"`

	// Refused — «не знаю» сказал код: ни один фрагмент не прошёл порог,
	// и модель не спрашивали.
	Refused bool `json:"-"`
	// Malformed — ответ модели не разобрался как JSON; показан как есть.
	Malformed bool `json:"-"`
	// Foreign — id источников и цитат, которых не было среди фрагментов.
	Foreign []string `json:"-"`
	// Uncited — источники без единой цитаты.
	Uncited []string `json:"-"`
	// Ungrounded — числа ответа, которых нет ни в цитатах, ни в названных
	// фрагментах, ни в вопросе.
	Ungrounded []string `json:"-"`
}

// Quote — дословный кусок фрагмента и итог сверки.
type Quote struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	// Verbatim — цитата нашлась в своём фрагменте (без учёта пробелов,
	// кавычек и разметки).
	Verbatim bool `json:"-"`
}

// OK — ответ прошёл проверки кода: либо честное «не знаю», либо ответ
// с источниками, у каждого источника цитата, все цитаты дословные,
// все числа ответа — из источников.
func (c *Citation) OK() bool {
	if c == nil || c.Malformed {
		return false
	}
	if !c.Known {
		return true
	}
	if len(c.IDs) == 0 || len(c.Quotes) == 0 || len(c.Foreign) > 0 || len(c.Uncited) > 0 || len(c.Ungrounded) > 0 {
		return false
	}
	for _, q := range c.Quotes {
		if !q.Verbatim {
			return false
		}
	}
	return true
}

// Verbatim — сколько цитат нашлось дословно.
func (c *Citation) Verbatim() int {
	n := 0
	for _, q := range c.Quotes {
		if q.Verbatim {
			n++
		}
	}
	return n
}

// Problems — что не так, по строке на нарушение.
func (c *Citation) Problems() []string {
	if c == nil {
		return []string{"ответ без разбора"}
	}
	var out []string
	if c.Malformed {
		return []string{"ответ модели — не JSON"}
	}
	if !c.Known {
		return nil
	}
	if len(c.IDs) == 0 {
		out = append(out, "нет источников")
	}
	if len(c.Quotes) == 0 {
		out = append(out, "нет цитат")
	}
	for _, q := range c.Quotes {
		if !q.Verbatim {
			out = append(out, "цитата не из фрагмента: «"+clipRunes(oneLineText(q.Text), 60)+"»")
		}
	}
	for _, id := range c.Foreign {
		out = append(out, "источник не из найденных: "+id)
	}
	for _, id := range c.Uncited {
		out = append(out, "источник без цитаты: "+id)
	}
	if len(c.Ungrounded) > 0 {
		out = append(out, "числа не из источников: "+strings.Join(c.Ungrounded, ", "))
	}
	return out
}

// augmentCite — вопрос с фрагментами и требованием ответить JSON
// с источниками и цитатами.
func augmentCite(question string, hits []rag.Hit) string {
	var b strings.Builder
	b.WriteString("Фрагменты базы знаний, найденные по вопросу (ближайшие — первыми):\n\n")
	for _, h := range hits {
		fmt.Fprintf(&b, "id: %s — %s\n%s\n\n", h.ID, h.Header(), strings.TrimSpace(h.Text))
	}
	b.WriteString(`---
Ответь на вопрос только по этим фрагментам. Верни JSON:
{"known": true, "answer": "ответ своими словами", "sources": ["id фрагмента"], "quotes": [{"id": "id фрагмента", "text": "дословная цитата"}]}

Правила:
- в sources — только id фрагментов, на которые опирается ответ;
- на каждый источник — хотя бы одна цитата; цитата — дословный кусок этого фрагмента, одно-два предложения, без пересказа и сокращений;
- в answer — только то, что есть в цитатах: на каждое утверждение ответа приведи цитату, из которой оно взято; чего не процитировал — не пиши;
- каждое число из answer должно быть в цитатах;
- если во фрагментах ответа нет — "known": false, в answer — «Не знаю» и уточняющий вопрос, sources и quotes пустые.

Вопрос: `)
	b.WriteString(question)
	return b.String()
}

// ParseCitation разбирает ответ модели и сверяет его с фрагментами.
//
// Числа ответа сверяются не только с цитатами, а с названными фрагментами
// целиком, их заголовками и самим вопросом: «в дне 17» и «60 000» из вопроса —
// не выдумка. Выдумка — число, которого нет нигде из того, на что ответ
// ссылается.
func ParseCitation(content, question string, hits []rag.Hit) *Citation {
	c := &Citation{}
	raw := strings.TrimSpace(content)
	// модели иногда заворачивают JSON в ```json … ```
	raw = strings.TrimPrefix(strings.TrimPrefix(raw, "```json"), "```")
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "```")
	if err := json.Unmarshal([]byte(raw), c); err != nil {
		return &Citation{Known: true, Answer: content, Malformed: true}
	}
	byID := map[string]rag.Hit{}
	for _, h := range hits {
		byID[h.ID] = h
	}
	quoted := map[string]bool{}
	for i, q := range c.Quotes {
		h, ok := byID[q.ID]
		if !ok {
			c.Foreign = appendOnce(c.Foreign, q.ID)
			continue
		}
		c.Quotes[i].Verbatim = strings.Contains(squashQuote(h.Text), squashQuote(q.Text)) && strings.TrimSpace(q.Text) != ""
		quoted[q.ID] = true
	}
	for _, id := range c.IDs {
		if _, ok := byID[id]; !ok {
			c.Foreign = appendOnce(c.Foreign, id)
			continue
		}
		if !quoted[id] {
			c.Uncited = append(c.Uncited, id)
		}
	}
	if c.Known {
		var all strings.Builder
		all.WriteString(question)
		for _, q := range c.Quotes {
			all.WriteString(" " + q.Text)
		}
		for _, id := range c.IDs {
			if h, ok := byID[id]; ok {
				all.WriteString(" " + h.Header() + " " + h.Source + " " + h.Text)
			}
		}
		have := map[string]bool{}
		for _, n := range numbersIn(all.String()) {
			have[n] = true
		}
		for _, n := range numbersIn(c.Answer) {
			if !have[n] {
				c.Ungrounded = appendOnce(c.Ungrounded, n)
			}
		}
	}
	return c
}

// Render — то, что увидит пользователь и что ляжет в историю: ответ,
// источники с разделами, цитаты и итог проверки.
func (c *Citation) Render(hits []rag.Hit) string {
	if c.Malformed {
		return c.Answer
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(c.Answer))
	if !c.Known {
		return b.String()
	}
	byID := map[string]rag.Hit{}
	for _, h := range hits {
		byID[h.ID] = h
	}
	if len(c.IDs) > 0 {
		b.WriteString("\n\nИсточники:")
		for _, id := range c.IDs {
			line := "\n- " + id
			if h, ok := byID[id]; ok && h.Section != "" {
				line += " — " + h.Section
			}
			b.WriteString(line)
		}
	}
	if len(c.Quotes) > 0 {
		b.WriteString("\n\nЦитаты:")
		for _, q := range c.Quotes {
			mark := ""
			if !q.Verbatim {
				mark = " ✗ нет во фрагменте"
			}
			fmt.Fprintf(&b, "\n- [%s] «%s»%s", q.ID, oneLineText(q.Text), mark)
		}
	}
	if p := c.Problems(); len(p) > 0 {
		b.WriteString("\n\n⚠ Проверка: " + strings.Join(p, "; "))
	} else {
		fmt.Fprintf(&b, "\n\n✓ Проверка: цитат %d из %d дословно во фрагментах, числа ответа — из источников", c.Verbatim(), len(c.Quotes))
	}
	return b.String()
}

// dontKnow — ответ кода, когда ни один фрагмент не прошёл порог. Ближайшие
// разделы подсказываются, только если они хотя бы на полпути к порогу:
// на вопрос про борщ подсказывать нечего.
func dontKnow(cfg Config, res *rag.Result) string {
	o := cfg.ragOptions()
	best, threshold, scale := float32(0), o.MinScore, "сходство"
	for _, h := range res.Candidates {
		s := h.Score
		if o.Rerank {
			s = h.Rerank
		}
		if s > best {
			best = s
		}
	}
	if o.Rerank {
		threshold, scale = o.MinRerank, "оценка реранкера"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Не знаю: в базе знаний нет фрагментов, которые отвечают на этот вопрос (лучшая %s — %.2f при пороге %.2f).", scale, best, threshold)
	var near []string
	seen := map[string]bool{}
	for _, h := range bestFirst(res.Candidates, o.Rerank) {
		s := h.Score
		if o.Rerank {
			s = h.Rerank
		}
		if s < threshold/2 || len(near) == 2 {
			break
		}
		name := h.Header()
		if !seen[name] {
			seen[name] = true
			near = append(near, "«"+name+"»")
		}
	}
	if len(near) > 0 {
		b.WriteString(" Ближе всего в базе: " + strings.Join(near, ", ") + " — вы об этом?")
	}
	b.WriteString(" Уточните, пожалуйста, вопрос: о каком дне, сервере или замере речь.")
	return b.String()
}

func bestFirst(hits []rag.Hit, byRerank bool) []rag.Hit {
	out := append([]rag.Hit(nil), hits...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, b := out[j-1].Score, out[j].Score
			if byRerank {
				a, b = out[j-1].Rerank, out[j].Rerank
			}
			if b <= a {
				break
			}
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// squashQuote — текст для сверки цитаты: без разметки markdown, кавычек
// и лишних пробелов, в нижнем регистре, ё = е. Модель вправе убрать
// звёздочки жирного и заменить кавычки, но не слова.
func squashQuote(s string) string {
	r := strings.NewReplacer("**", "", "`", "", "«", "", "»", "", "\"", "", "„", "", "“", "", "ё", "е", "Ё", "е", "—", "-", "–", "-", " ", " ", " ", " ")
	s = strings.ToLower(r.Replace(s))
	s = strings.Join(strings.Fields(s), " ")
	return strings.Trim(s, " .…")
}

var numberRe = regexp.MustCompile(`\d[\d   ]*(?:[.,]\d+)?`)

// numbersIn — числа текста без пробелов внутри и с точкой вместо запятой:
// «21 135» и «21135», «0,0046» и «0.0046» — одно и то же. Однозначные
// числа не считаются: «в 2 раза» и «2 из 3» слишком часто пересказ,
// а не факт из документа.
func numbersIn(s string) []string {
	var out []string
	for _, m := range numberRe.FindAllString(s, -1) {
		n := strings.NewReplacer(" ", "", " ", "", " ", "", ",", ".").Replace(strings.TrimSpace(m))
		if len(strings.TrimLeft(strings.Split(n, ".")[0], "0")) < 2 && !strings.Contains(n, ".") {
			continue
		}
		out = append(out, n)
	}
	return out
}

func appendOnce(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

// judgeSystem — инструкция судьи: совпадает ли смысл ответа с цитатами.
// Это то, что код проверить не может: цитата может быть дословной,
// а ответ — утверждать по ней другое.
const judgeSystem = `Ты проверяешь ответ ассистента базы знаний. Тебе дают ответ и цитаты, на которые он ссылается.
Реши, подтверждают ли цитаты каждое утверждение ответа. Пересказ своими словами допустим; новые факты, числа и выводы, которых в цитатах нет, — нет.
Верни JSON: {"supported": true|false, "unsupported": "что в ответе не подтверждено цитатами, или пустая строка"}`

// Verdict — решение судьи.
type Verdict struct {
	Supported   bool   `json:"supported"`
	Unsupported string `json:"unsupported"`
}

// JudgeCitation — служебный вызов судьи над ответом с цитатами (день 24).
// Расход идёт в счётчики агента и в журнал, как у остальных служебных
// вызовов.
func (a *Agent) JudgeCitation(ctx context.Context, c *Citation) (*Verdict, float64, error) {
	if c == nil || !c.Known || c.Malformed {
		return nil, 0, fmt.Errorf("судить нечего: ответа с цитатами нет")
	}
	var b strings.Builder
	b.WriteString("Ответ:\n" + strings.TrimSpace(c.Answer) + "\n\nЦитаты:\n")
	for _, q := range c.Quotes {
		fmt.Fprintf(&b, "- [%s] %s\n", q.ID, oneLineText(q.Text))
	}
	cfg := a.Config()
	req := llm.Request{
		Model: cfg.Model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: judgeSystem},
			{Role: llm.RoleUser, Content: b.String()},
		},
		Temperature:    llm.F(0),
		Thinking:       &llm.Thinking{Type: "disabled"},
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}
	resp, err := a.client.Chat(ctx, req)
	a.record(req, resp, err, "судья цитат", true)
	a.accountAux(resp, err)
	if err != nil {
		return nil, 0, err
	}
	var v Verdict
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.Content)), &v); err != nil {
		return nil, resp.CostUSD, fmt.Errorf("судья ответил не JSON: %w", err)
	}
	return &v, resp.CostUSD, nil
}
