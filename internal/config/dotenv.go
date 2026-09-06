package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// loadDotEnv читает файл вида KEY=value рядом с проектом.
//
// Уже выставленные переменные окружения имеют приоритет: то, что задано
// в терминале, важнее файла. Отсутствие файла — не ошибка: .env лежит
// в .gitignore и на чистой копии репозитория его просто нет.
func loadDotEnv(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	for i, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: ожидал KEY=value, а получил %q", path, i+1, line)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(unquote(strings.TrimSpace(val)))
		if key == "" {
			return fmt.Errorf("%s:%d: пустое имя переменной", path, i+1)
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return err
		}
	}
	return nil
}

// unquote снимает кавычки, если значение записано как KEY="sk-..." или KEY='sk-...'.
func unquote(s string) string {
	if len(s) < 2 {
		return s
	}
	first, last := s[0], s[len(s)-1]
	if first == last && (first == '"' || first == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}
