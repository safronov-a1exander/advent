package main

import "testing"

func TestRecallReadsOnlyTheRequest(t *testing.T) {
	intro := []message{
		{Role: "system", Content: "ты ассистент"},
		{Role: "user", Content: "Привет! Меня зовут Саша, веду бюджет на сентябрь"},
		{Role: "assistant", Content: "Привет!"},
		{Role: "user", Content: "Как меня зовут и какой у меня бюджет?"},
	}
	if !recallQuestion(intro) {
		t.Fatal("вопрос про имя не распознан")
	}
	if got, want := recall(intro), "Тебя зовут Саша — ты сам сказал это раньше в нашем разговоре."; got != want {
		t.Fatalf("recall = %q, ожидали %q", got, want)
	}

	// Повторный вопрос: в истории уже есть прошлое «как меня зовут и …»,
	// и оно не должно быть принято за представление.
	again := append(append([]message(nil), intro...),
		message{Role: "assistant", Content: "Тебя зовут Саша"},
		message{Role: "user", Content: "Напомни, как меня зовут?"})
	if got, want := recall(again), "Тебя зовут Саша — ты сам сказал это раньше в нашем разговоре."; got != want {
		t.Fatalf("повторный вопрос: %q", got)
	}

	// Та же реплика без истории — как у агента, которому историю не передали.
	alone := []message{{Role: "user", Content: "Как меня зовут?"}}
	if got := recall(alone); got != "Не знаю: в этом разговоре ты не представлялся." {
		t.Fatalf("без истории заглушка не должна знать имя, ответила %q", got)
	}

	// Имя, названное ассистентом, не считается: представлялся пользователь.
	fromBot := []message{
		{Role: "assistant", Content: "меня зовут Бюджет"},
		{Role: "user", Content: "как меня зовут?"},
	}
	if got := recall(fromBot); got != "Не знаю: в этом разговоре ты не представлялся." {
		t.Fatalf("имя ассистента принято за имя пользователя: %q", got)
	}

	if recallQuestion([]message{{Role: "user", Content: "Сколько осталось?"}}) {
		t.Fatal("обычный вопрос принят за вопрос про имя")
	}
}
