# Ранбук: от ключа до записанного демо

Пошаговый порядок на один заход: сверка окружения, репетиция без расхода
токенов, боевой прогон и запись всех шагов.

## 0. Ключ

```bash
cp .env.example .env    # и вписать ключ
```

Файл `.env` в `.gitignore`. Переменная окружения, заданная в терминале,
имеет приоритет над файлом. Альтернатива — `config.local.yaml`:

```yaml
providers:
  - name: deepseek
    api_key: sk-...
```

## 1. Сверка (5 минут, но пропускать нельзя)

```bash
go run ./cmd/advent doctor
go run ./cmd/advent models
```

Что проверяем:

- [ ] `doctor` пишет «стенд готов»;
- [ ] имена моделей из `GET /models` совпадают с `config.yaml`
      (`deepseek-v4-flash`, `deepseek-v4-pro`);
- [ ] **цены в `config.yaml` заменены на реальные** с
      https://api-docs.deepseek.com/quick_start/pricing — иначе колонка
      стоимости во всех отчётах врёт, а на шаге 5 она и есть предмет сравнения;
- [ ] если имена моделей другие — поправить `config.yaml` **и**
      `scenarios/day05-models.yaml`.

## 2. Репетиция без денег

Заглушка отвечает как настоящий API (включая SSE и 400 на temperature вне
диапазона), но не тратит токены:

```bash
go run ./tools/mockllm -addr :8099
```

в другом окне:

```powershell
.\scripts\record-all.ps1 -Provider mock -DryRun    # план
.\scripts\record-all.ps1 -Provider mock            # полный прогон с записью
```

Смотрим, что видео пишутся, окно не обрезано, подписи читаемы.
Потом чистим `recordings/`, `runs/`, `reports/`.

## 3. Боевой прогон по одному шагу

Сначала прогнать каждый сценарий **без записи** и глазами посмотреть ответы —
чтобы демо не показывало неудачный результат:

```bash
go run ./cmd/advent run -scenario scenarios/day02-format.yaml -full
go run ./cmd/advent run -scenario scenarios/day03-reasoning.yaml -full
go run ./cmd/advent run -scenario scenarios/day04-temperature-creative.yaml
go run ./cmd/advent run -scenario scenarios/day04-temperature-precise.yaml
go run ./cmd/advent run -scenario scenarios/day05-models.yaml
```

На что смотреть — в `docs/days/dayNN.md`, раздел «Что нужно сделать тебе».
Коротко:

| шаг | что может пойти не так | что делать |
|---|---|---|
| 2 | вариант 3 не упёрся в лимит | уменьшить `max_tokens` |
| 3 | все четыре способа верны (44765) | взять модель послабее или `thinking: disabled` |
| 4 | ошибка на 2.5 звучит иначе | поправить `checks.expect_error` |
| 5 | имена моделей не те | поправить `config.yaml` и сценарий |

Данные продукта лежат в `data/statement.txt` и подключаются в сценарии
через `vars_files`. Правишь выписку — **пересчитай ответ** в
`checks.must_contain` у шагов 3, 4 и 5, иначе проверки станут врать.

## 4. Запись

Ветки должны быть закоммичены — скрипт переключается между ними и откажется
работать на грязном дереве.

```powershell
.\scripts\record-all.ps1
```

На выходе `recordings/dayNN-<дата>.mp4`. Отдельный шаг:

```powershell
.\scripts\record-all.ps1 -Days 3
```

## Сколько это стоит

Порядок величин при прайсе flash: один прогон сценария — единицы центов.
Самые дорогие шаги — 3-й (16 вызовов, многошаговые цепочки) и 4-й
(27 вызовов на два сценария). Все пять шагов вместе укладываются в доллар.

Точная сумма за прогон печатается в конце каждого `run` и лежит в
`runs/*.jsonl` — так что после первого же шага видна фактическая цифра.
