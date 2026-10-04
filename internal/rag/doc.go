// Package rag — поиск по своей базе знаний (неделя 5, дни 21–25).
//
// До сих пор всё, что модель знала о стенде, она знала из промпта или
// из ответа инструмента. Здесь появляется третий источник — документы,
// которые лежат рядом с кодом: заметки по дням, README, ранбук, исходники
// своих MCP-серверов. Модель их никогда не видела, и в промпт целиком они
// не влезут — их нужно сначала нарезать, превратить в векторы и сложить
// в индекс, а потом доставать по смыслу вопроса.
//
// Пакет ничего не знает о провайдере эмбеддингов: векторы приходят через
// интерфейс Embedder, а у стенда это OpenAI-совместимый /v1/embeddings —
// llama.cpp, Ollama, облако или заглушка.
package rag

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Kind — тип документа: от него зависит, как резать по структуре.
type Kind string

const (
	KindMarkdown Kind = "md"
	KindGo       Kind = "go"
	KindText     Kind = "txt"
)

// Document — один файл базы знаний.
type Document struct {
	// Source — путь относительно корня проекта, через «/»: по нему
	// ответ ссылается на документ, и он не должен зависеть от ОС.
	Source string
	// Title — заголовок: «# …» у markdown, «пакет …» у кода.
	Title string
	Kind  Kind
	Text  string
}

// Sources — какие файлы попадают в базу: шаблоны путей относительно
// корня и исключения. «**» в шаблоне — любое число каталогов.
type Sources struct {
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

// Load читает документы базы. Порядок — по пути: индекс, собранный
// дважды из тех же файлов, должен совпасть байт в байт.
func Load(root string, src Sources) ([]Document, error) {
	if len(src.Include) == 0 {
		return nil, fmt.Errorf("в rag.sources.include пусто — нечего индексировать")
	}
	seen := map[string]bool{}
	var docs []Document
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if seen[rel] || !matchAny(src.Include, rel) || matchAny(src.Exclude, rel) {
			return nil
		}
		seen[rel] = true
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		docs = append(docs, newDocument(rel, string(b)))
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("ни один файл не подошёл под rag.sources.include")
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Source < docs[j].Source })
	return docs, nil
}

func newDocument(source, text string) Document {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	d := Document{Source: source, Text: text, Kind: KindText, Title: path.Base(source)}
	switch strings.ToLower(path.Ext(source)) {
	case ".md":
		d.Kind = KindMarkdown
		for _, line := range strings.Split(text, "\n") {
			if t, ok := strings.CutPrefix(line, "# "); ok {
				d.Title = strings.TrimSpace(t)
				break
			}
		}
	case ".go":
		d.Kind = KindGo
		for _, line := range strings.Split(text, "\n") {
			if p, ok := strings.CutPrefix(line, "package "); ok {
				d.Title = "пакет " + strings.TrimSpace(p) + " — " + path.Base(source)
				break
			}
		}
	}
	return d
}

// matchAny — подходит ли путь хоть под один шаблон.
func matchAny(patterns []string, rel string) bool {
	for _, p := range patterns {
		if matchPath(p, rel) {
			return true
		}
	}
	return false
}

// matchPath — path.Match по сегментам, где «**» съедает любое число
// каталогов, в том числе ноль.
func matchPath(pattern, rel string) bool {
	return matchSegs(strings.Split(pattern, "/"), strings.Split(rel, "/"))
}

func matchSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(segs); i++ {
				if matchSegs(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], segs[0]); !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}
