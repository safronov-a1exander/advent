package metrics

import "testing"

func TestMeanSimilarity(t *testing.T) {
	same := []string{"кофе с молоком и корицей", "кофе с молоком и корицей"}
	if got := MeanSimilarity(same); got != 1 {
		t.Errorf("одинаковые тексты: хочу 1, получил %v", got)
	}

	diff := []string{"кофе с молоком и корицей", "жареная картошка на сковороде"}
	if got := MeanSimilarity(diff); got != 0 {
		t.Errorf("непересекающиеся тексты: хочу 0, получил %v", got)
	}

	near := []string{
		"кофе с молоком и корицей",
		"кофе с молоком и кардамоном",
	}
	got := MeanSimilarity(near)
	if got <= 0 || got >= 1 {
		t.Errorf("похожие тексты: хочу строго между 0 и 1, получил %v", got)
	}
}

func TestTypeTokenRatio(t *testing.T) {
	if got := TypeTokenRatio("а а а а"); got != 0.25 {
		t.Errorf("хочу 0.25, получил %v", got)
	}
	if got := TypeTokenRatio(""); got != 0 {
		t.Errorf("пустая строка: хочу 0, получил %v", got)
	}
}

func TestAllIdentical(t *testing.T) {
	if !AllIdentical([]string{"x", " x "}) {
		t.Error("пробелы по краям не должны считаться различием")
	}
	if AllIdentical([]string{"x", "y"}) {
		t.Error("разные тексты не идентичны")
	}
}
