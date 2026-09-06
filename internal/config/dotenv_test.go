package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	body := "# комментарий\n" +
		"\n" +
		"DEEPSEEK_API_KEY=sk-plain\n" +
		"QUOTED=\"sk-quoted\"\n" +
		"SINGLE='sk-single'\n" +
		"export EXPORTED=sk-exported\n" +
		"ALREADY_SET=from-file\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ALREADY_SET", "from-env")
	t.Setenv("DEEPSEEK_API_KEY", "")
	os.Unsetenv("DEEPSEEK_API_KEY")

	if err := loadDotEnv(path); err != nil {
		t.Fatalf("не ожидал ошибки: %v", err)
	}

	want := map[string]string{
		"DEEPSEEK_API_KEY": "sk-plain",
		"QUOTED":           "sk-quoted",
		"SINGLE":           "sk-single",
		"EXPORTED":         "sk-exported",
		// переменная окружения важнее файла
		"ALREADY_SET": "from-env",
	}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s: хочу %q, получил %q", k, v, got)
		}
		if k != "ALREADY_SET" {
			t.Cleanup(func() { os.Unsetenv(k) })
		}
	}
}

func TestLoadDotEnvMissingIsFine(t *testing.T) {
	if err := loadDotEnv(filepath.Join(t.TempDir(), "нет-такого")); err != nil {
		t.Errorf("отсутствие файла не должно быть ошибкой: %v", err)
	}
}

func TestLoadDotEnvBadLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("ЭТО НЕ ПАРА\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadDotEnv(path); err == nil {
		t.Error("ожидал ошибку на строке без знака равенства")
	}
}
