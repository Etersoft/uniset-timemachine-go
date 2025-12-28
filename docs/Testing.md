# Тестирование

## Unit-тесты

```bash
# Запуск всех тестов
go test ./...

# С покрытием
make coverage

# Конкретный пакет
go test ./internal/replay -v
go test ./internal/storage/sqlite -v
```

## Playwright UI тесты

Для запуска UI тестов используется Docker Compose с профилем `tests`.

### Подготовка окружения

```bash
# Пересборка и запуск тест-окружения
docker-compose --profile tests build timemachine
docker-compose --profile tests up -d --force-recreate timemachine
```

### Запуск тестов

```bash
# Все UI тесты
docker-compose --profile tests run --rm playwright

# Один тестовый файл (ВАЖНО: --entrypoint "" обязателен!)
docker-compose --profile tests run --rm --entrypoint "" playwright \
  npx playwright test ui-charts-page.spec.ts -c tests/playwright.config.ts --reporter=list

# Конкретный тест по номеру строки
docker-compose --profile tests run --rm --entrypoint "" playwright \
  npx playwright test ui-session-control-new.spec.ts:5 -c tests/playwright.config.ts --reporter=list
```

### Полный цикл

```bash
docker-compose --profile tests build timemachine && \
  docker-compose --profile tests up -d --force-recreate timemachine && \
  sleep 5 && \
  docker-compose --profile tests run --rm playwright
```

### Генерация скриншотов документации

```bash
docker-compose --profile tests run --rm --entrypoint "" playwright \
  npx playwright test doc-screenshots.spec.ts -c tests/playwright.config.ts --reporter=list
```

Скриншоты сохраняются в `docs/screenshots/`.

## Интеграционные тесты с БД

### PostgreSQL

```bash
# Поднять контейнер с тестовыми данными
make pg-up

# Запустить тесты
TM_POSTGRES_DSN="postgres://admin:123@localhost:5432/uniset?sslmode=disable" go test ./...

# Остановить
make pg-down
```

### ClickHouse

```bash
# Поднять контейнер
make ch-up

# Запустить тесты
TM_CLICKHOUSE_DSN="clickhouse://localhost:9000/default" go test ./...

# Остановить
make ch-down
```

### InfluxDB

```bash
# Поднять контейнер
make influx-up

# Сгенерировать данные
make influx-gen-data

# Остановить
make influx-down
```

## Проверка SharedMemory

```bash
make check-sm SM_TEST_SENSOR=10001 SM_TEST_SUPPLIER=TestProc
```

Отправляет одиночный `/set` и проверяет `/get`. Параметры берутся из `config/config.yaml` или задаются явно через `SM_EXTRA_FLAGS`.
