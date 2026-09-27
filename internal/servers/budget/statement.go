// Package budget — MCP-сервер «Бюджет» (день 19): поиск по выписке,
// сводка и сохранение отчёта — три инструмента, из которых агент сам
// собирает цепочку.
package budget

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Tx — одна операция выписки.
type Tx struct {
	ID       string  `json:"id"`
	Date     string  `json:"date"` // ГГГГ-ММ-ДД
	Merchant string  `json:"merchant"`
	Amount   float64 `json:"amount"` // плюс — списание, минус — возврат
	Currency string  `json:"currency"`
	Category string  `json:"category"`
}

// categories — категория по подстроке в названии продавца. Порядок важен:
// «YANDEX EDA» — кафе, а не такси, хотя тоже Яндекс.
var categories = []struct{ key, cat string }{
	{"YANDEX EDA", "кафе"}, {"DRINKIT", "кафе"},
	{"YANDEX.TAXI", "транспорт"}, {"RZD", "транспорт"},
	{"PYATEROCHKA", "продукты"}, {"SAMOKAT", "продукты"},
	{"OZON", "покупки"}, {"WILDBERRIES", "покупки"},
	{"APTEKA", "здоровье"}, {"SPORTLIFE", "здоровье"},
	{"SPOTIFY", "подписки"},
	{"TRANSFER TO OWN", "перевод"},
	{"ATM", "наличные"},
}

func categoryOf(merchant string) string {
	m := strings.ToUpper(merchant)
	for _, c := range categories {
		if strings.Contains(m, c.key) {
			return c.cat
		}
	}
	return "прочее"
}

// Load читает выписку: «ДД.ММ ПРОДАВЕЦ СУММА ВАЛЮТА» на строку.
// Года в выписке нет — он берётся из аргумента.
func Load(path string, year int) ([]Tx, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Tx
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fs := strings.Fields(line)
		if len(fs) < 4 {
			return nil, fmt.Errorf("%s:%d: не разобрать строку %q", path, n, line)
		}
		d, err := time.Parse("02.01.2006", fs[0]+"."+strconv.Itoa(year))
		if err != nil {
			return nil, fmt.Errorf("%s:%d: дата %q: %w", path, n, fs[0], err)
		}
		amount, err := strconv.ParseFloat(fs[len(fs)-2], 64)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: сумма %q: %w", path, n, fs[len(fs)-2], err)
		}
		merchant := strings.Join(fs[1:len(fs)-2], " ")
		out = append(out, Tx{
			ID: fmt.Sprintf("t%02d", n), Date: d.Format("2006-01-02"), Merchant: merchant,
			Amount: amount, Currency: fs[len(fs)-1], Category: categoryOf(merchant),
		})
	}
	return out, sc.Err()
}
