package rag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const md = "# День 99 — проверка\n\nВетка: `day-99`\n\n## Первый раздел\n\nТекст первого раздела. Он про курсы.\n\n```bash\n# это комментарий, а не заголовок\ngo run ./cmd/advent\n```\n\n## Второй раздел\n\n### Вложенный\n\nТекст вложенного раздела про бюджет.\n"

func TestMarkdownSectionsSkipCodeFences(t *testing.T) {
	d := newDocument("docs/x.md", md)
	if d.Title != "День 99 — проверка" {
		t.Fatalf("заголовок: %q", d.Title)
	}
	var got []string
	for _, s := range sections(d) {
		got = append(got, s.path)
	}
	want := []string{"Первый раздел", "Второй раздел", "Второй раздел › Вложенный"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("разделы %q, ждали %q", got, want)
	}
}

func TestStructureKeepsSectionsAndMergesBareHeading(t *testing.T) {
	d := newDocument("docs/x.md", md)
	chunks := Split(d, Structure, Chunking{Max: 1000, Min: 40})
	// введение (заголовок и ветка) короче min и прилипает к первому разделу;
	// «## Второй раздел» без текста — к вложенному
	if len(chunks) != 2 {
		for _, c := range chunks {
			t.Logf("%s [%s] %q", c.ID, c.Section, c.Text)
		}
		t.Fatalf("чанков %d, ждали 2", len(chunks))
	}
	if !strings.Contains(chunks[0].Text, "go run") || !strings.Contains(chunks[0].Text, "Текст первого") {
		t.Fatalf("блок кода должен остаться в своём разделе: %q", chunks[0].Text)
	}
	// раздел склеенного куска — раздел его содержательной части
	if chunks[0].Section != "Первый раздел" || chunks[1].Section != "Второй раздел › Вложенный" || !strings.Contains(chunks[1].Text, "вложенного") {
		t.Fatalf("второй чанк: [%s] %q", chunks[1].Section, chunks[1].Text)
	}
	if chunks[0].ID != "docs/x.md#0" || chunks[1].ID != "docs/x.md#1" {
		t.Fatalf("id: %s, %s", chunks[0].ID, chunks[1].ID)
	}
}

func TestStructureSplitsLongSectionByParagraphs(t *testing.T) {
	para := strings.Repeat("слово ", 30) // 180 символов
	text := "# Т\n\n## Длинный\n\n" + strings.Repeat(para+"\n\n", 10)
	chunks := Split(newDocument("a.md", text), Structure, Chunking{Max: 500, Min: 50})
	if len(chunks) < 4 {
		t.Fatalf("чанков %d — длинный раздел не поделился", len(chunks))
	}
	for _, c := range chunks {
		if n := utf8.RuneCountInString(c.Text); n > 500+50 {
			t.Fatalf("чанк %s длиной %d больше потолка", c.ID, n)
		}
		if c.Section != "Длинный" {
			t.Fatalf("у куска раздела потерялся раздел: %q", c.Section)
		}
		if strings.HasPrefix(c.Text, "лово") {
			t.Fatalf("абзац разрезан посреди слова: %q", c.Text[:20])
		}
	}
}

func TestFixedOverlapsAndCoversEverything(t *testing.T) {
	text := strings.Repeat("раз два три четыре пять. ", 200) // 5000 символов
	spans := fixedSpans(text, 1000, 200)
	if spans[0][0] != 0 || spans[len(spans)-1][1] != len(text) {
		t.Fatalf("окна не покрывают текст: %v … %v", spans[0], spans[len(spans)-1])
	}
	for i := 1; i < len(spans); i++ {
		if spans[i][0] >= spans[i-1][1] {
			t.Fatalf("окна %d и %d не перекрываются: %v %v", i-1, i, spans[i-1], spans[i])
		}
		if spans[i][0] <= spans[i-1][0] {
			t.Fatalf("окно %d не продвинулось", i)
		}
	}
	if n := runes(text, spans[0]); n > 1000 {
		t.Fatalf("окно длиннее размера: %d", n)
	}
}

func TestFixedSectionIsWhereChunkStarts(t *testing.T) {
	text := "# T\n\n## Первый\n\n" + strings.Repeat("а ", 300) + "\n\n## Второй\n\n" + strings.Repeat("б ", 300)
	chunks := Split(newDocument("a.md", text), Fixed, Chunking{Size: 500, Overlap: 50})
	last := chunks[len(chunks)-1]
	if last.Section != "Второй" {
		t.Fatalf("последний чанк: раздел %q", last.Section)
	}
	if chunks[0].Section != "" {
		t.Fatalf("первый чанк начинается до разделов, а помечен %q", chunks[0].Section)
	}
}

func TestGoSectionsAreDeclarationsWithComments(t *testing.T) {
	src := "// Package x — пример.\npackage x\n\nimport \"fmt\"\n\n// Plan считает план.\nfunc Plan() {}\n\nfunc (s *Server) handle() {}\n\ntype Goal struct{}\n\nconst (\n\ta = 1\n)\n"
	d := newDocument("internal/x/x.go", src)
	var got []string
	for _, s := range sections(d) {
		got = append(got, s.path)
	}
	want := "пакет x|func Plan|func (Server) handle|type Goal|const"
	if strings.Join(got, "|") != want {
		t.Fatalf("объявления %q", got)
	}
	// комментарий над функцией — часть её раздела
	for _, s := range sections(d) {
		if s.path == "func Plan" && !strings.HasPrefix(src[s.start:], "// Plan") {
			t.Fatalf("раздел func Plan начинается не с комментария: %q", src[s.start:s.start+10])
		}
	}
}

func TestMatchPath(t *testing.T) {
	cases := []struct {
		pat, path string
		ok        bool
	}{
		{"docs/days/day0?.md", "docs/days/day07.md", true},
		{"docs/days/day0?.md", "docs/days/day21.md", false},
		{"internal/servers/**/*.go", "internal/servers/rates/cbr.go", true},
		{"internal/servers/**/*.go", "internal/servers/x.go", true},
		{"**/*_test.go", "internal/servers/rates/cbr_test.go", true},
		{"README.md", "docs/README.md", false},
	}
	for _, c := range cases {
		if got := matchPath(c.pat, c.path); got != c.ok {
			t.Errorf("%s ~ %s = %v, ждали %v", c.pat, c.path, got, c.ok)
		}
	}
}

// wordEmbedder — векторы по словам из словаря: проверяемая геометрия
// без сети.
type wordEmbedder struct{ vocab []string }

func (w wordEmbedder) Name() string { return "test" }
func (w wordEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, len(w.vocab)+1)
		v[len(w.vocab)] = 0.01
		for j, word := range w.vocab {
			v[j] = float32(strings.Count(strings.ToLower(t), word))
		}
		out[i] = Normalize(v)
	}
	return out, nil
}

func TestIndexRoundTripAndSearch(t *testing.T) {
	docs := []Document{
		newDocument("a.md", "# A\n\n## Курсы\n\nкурс лиры и курс доллара\n"),
		newDocument("b.md", "# B\n\n## Бюджет\n\nбюджет и траты за месяц\n"),
	}
	emb := wordEmbedder{vocab: []string{"курс", "бюджет", "траты"}}
	ix, err := Build(context.Background(), docs, Structure, Chunking{}, emb, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ix", "structure.json")
	if err := ix.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("временный файл остался")
	}
	back, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := back.Query(context.Background(), emb, "какой курс", 2)
	if err != nil {
		t.Fatal(err)
	}
	if hits[0].Source != "a.md" || hits[0].Score <= hits[1].Score {
		t.Fatalf("первым должен быть a.md: %+v", hits)
	}
	if hits[0].Section != "Курсы" || hits[0].Title != "A" {
		t.Fatalf("метаданные потерялись при сохранении: %+v", hits[0].Chunk)
	}
}

type otherEmbedder struct{ wordEmbedder }

func (otherEmbedder) Name() string { return "другая" }

func TestQueryRefusesForeignEmbedder(t *testing.T) {
	emb := wordEmbedder{vocab: []string{"курс"}}
	ix, _ := Build(context.Background(), []Document{newDocument("a.md", "# A\n\nкурс\n")}, Fixed, Chunking{}, emb, nil)
	if _, err := ix.Query(context.Background(), otherEmbedder{emb}, "курс", 1); err == nil {
		t.Fatal("векторы другой модели несравнимы — нужен отказ")
	}
}

func TestProbeContainsChecksWholeFact(t *testing.T) {
	p := Probe{Source: "a.md", Expect: []string{"5376 из 6570", "кэша"}}
	c := Chunk{Source: "a.md", Text: "пришли 5376 из\n6570 входных токенов из кэша"}
	if !p.Contains(c) {
		t.Fatal("перенос строки внутри факта не должен мешать")
	}
	c.Text = "пришли 5376 из 6570 входных токенов"
	if p.Contains(c) {
		t.Fatal("половина факта — не попадание")
	}
	c.Source = "b.md"
	c.Text = "5376 из 6570 из кэша"
	if p.Contains(c) {
		t.Fatal("факт из другого файла — не попадание")
	}
}

func TestStatsCountsCutsAndMixedSections(t *testing.T) {
	text := "# T\n\n## Один\n\nПервая фраза. Вторая фраза.\n\n## Два\n\nТретья фраза.\n"
	d := newDocument("a.md", text)
	ix := &Index{}
	for _, c := range Split(d, Fixed, Chunking{Size: 40, Overlap: 10}) {
		ix.Chunks = append(ix.Chunks, Entry{Chunk: c})
	}
	st := StatsOf([]Document{d}, ix)
	if st.CutMidSentence == 0 || st.Overhead <= 0 {
		t.Fatalf("окно 40 символов обязано рвать фразы и повторять текст: %+v", st)
	}
	ix.Chunks = nil
	for _, c := range Split(d, Structure, Chunking{Max: 1000, Min: 20}) {
		ix.Chunks = append(ix.Chunks, Entry{Chunk: c})
	}
	st = StatsOf([]Document{d}, ix)
	if st.CutMidSentence != 0 || st.MixedSections != 0 {
		t.Fatalf("по разделам фразы не рвутся и разделы не смешиваются: %+v", st)
	}
}
