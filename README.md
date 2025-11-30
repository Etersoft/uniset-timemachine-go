# uniset-timemachine-go

Консольный проигрыватель истории датчиков на Go. Читает изменения из PostgreSQL/SQLite/ClickHouse, интерполирует состояния на шаге и публикует их в SharedMemory либо stdout. Поддерживает HTTP-режим управления v2 (pending range/seek, старт через `/api/v2/job/start`) поверх того же ядра.

## Внутренние модули
- `cmd/timemachine` — CLI и HTTP-режим (`--http-addr`), разбор флагов.
- `pkg/config` — загрузка XML/JSON UniSet, маппинг имён датчиков и наборов.
- `internal/storage` — адаптеры Postgres/SQLite/ClickHouse/memstore, warmup/stream/range.
- `internal/replay` — цикл воспроизведения, управление шагами, батчи обновлений.
- `internal/sharedmem` — клиенты SM (HTTP) и stdout для тестов.
- `internal/api` — однозадачный HTTP API управления проигрывателем.

## Конфигурация

`pkg/config` умеет читать два формата:
- UniSet XML (`config/test.xml`) — берём датчики из секции `<sensors><item id="..." name="..." textname="..."/>`.
- Упрощённый JSON (`config/example.json`) — удобно для быстрых тестов и моков.

CLI принимает флаг `--confile` с путём к XML/JSON и `--slist` с именем набора (или `ALL`, или списком имён). Дополнительно поддерживаются glob-паттерны (`--slist "Sensor100*"` выберет все сенсоры из блока `Sensor100XX_S`). Для XML наборы не задаются и `--slist` должен перечислять имена вручную/по шаблону или использовать `ALL`. Выход по умолчанию — `stdout`; для SharedMemory используйте `--output http://...`:

- `--output http://...` — базовый URL SharedMemory (пример `http://localhost:9191/api/v01/SharedMemory`). `stdout` — вывод в стандартный поток.
- `--sm-supplier` — имя процесса (по умолчанию `TimeMachine`, используйте реальное имя при включённой проверке прав).
- `--sm-param-mode` — формат параметров (`id` или `name`, по умолчанию `id`; режим `name` берёт имена из конфигурации).
- `--sm-param-prefix` — префикс перед ID (по умолчанию `id`, установите пустую строку `--sm-param-prefix ""`, чтобы отправлять только числа).
- `-v/--verbose` — логировать каждый запрос `/set` и ответ SM.

Чтобы вернуться к консольному выводу, укажите `--output stdout`.

### HTTP-режим управления

Запустите сервер управления с теми же параметрами БД/SM, что и CLI:

```bash
go run ./cmd/timemachine --http-addr :8080 --db ... --confile config/test.xml --slist "Sensor?????_S" --output http://localhost:9191/api/v01/SharedMemory --sm-supplier TestProc
```

Быстрый пример (демо ClickHouse + stdout + UI на 9090):

```bash
go run ./cmd/timemachine --http-addr 127.0.0.1:9090 --output stdout \
  --db clickhouse://default:@localhost:9000/uniset --ch-table uniset.main_history \
  --confile config/test.xml --slist ALL --ws-batch-time 100ms
```

Далее управляйте через HTTP v2: `/api/v2/job/sensors` (рабочий список датчиков) → `/api/v2/job/range` (save диапазон) → `/api/v2/job/start` (старт), `pause/resume/stop/seek/step/apply`, `snapshot`, статус `/api/v2/job`, подсчёт датчиков `/api/v2/job/sensors/count`. Словарь датчиков (`id/name/textname/iotype`) доступен по `/api/v2/sensors`. Подробное описание эндпоинтов и примеров запросов см. в `DOCS.md`.
Встроенный UI доступен по `/ui/`: использует WebSocket `/api/v2/ws/state`, включает кнопки Smoke/Flow для быстрого сценария, индикатор «идёт тестирование…», вкладку «Графики» (Chart.js/uPlot), подсказки по датчикам и кнопку «Загрузить» для выбора рабочего списка (из файла или списка доступных датчиков).

Управление защищено сессиями: все управляющие запросы требуют заголовок `X-TM-Session`. `GET /api/v2/session` **не** забирает управление — только возвращает статус (`session`, `is_controller`, `controller_present`, `control_timeout_sec`, `can_claim`, `ping=1` — keepalive). Захват управления только явным `POST /api/v2/session/claim` (успех, если контроллер пуст или просрочен; таймаут — `--control-timeout`, `0` — не отдавать). При чужом токене управляющие вызовы вернут `403 control locked`. UI по умолчанию автоклеймит только при первой загрузке, если контроллера нет; в остальных случаях показывает кнопку «Забрать управление» после таймаута.

Пример YAML для serve-режима (см. `config/config.yaml`) включает адрес HTTP-сервера:

```yaml
http:
  addr: :8080
logging:
  cache: false
```
Этот блок маппится на флаг `--http-addr` при запуске с `--config-yaml config/config.yaml`.

Логи кеша воспроизведения можно включить флагом `--log-cache` или `logging.cache: true` в YAML.

### YAML с параметрами по умолчанию

Чтобы не передавать длинный список флагов каждый раз, создайте структурированный YAML (например, `config/config.yaml`):

```yaml
database:
  type: clickhouse
  dsn: clickhouse://default:@localhost:9000/uniset
  table: uniset.main_history
  step: 150ms
  window: 2s
  speed: 400

sensors:
  config: config/test.xml
  selector: "Sensor?????_S"
  from: 2024-06-01T00:00:00Z
  to: 2024-06-01T00:01:00Z

output:
  mode: http
  sm_url: http://localhost:9191/api/v01/SharedMemory
  sm_supplier: TestProc
  batch_size: 500
  verbose: true
```

Секции `database`, `sensors` и `output` автоматически маппятся на флаги (`database.dsn → --db`, `database.table → --ch-table`, `sensors.selector → --slist`, `output → --output`, `output.batch_size → --batch-size`, `output.verbose → -v`). Можно добавлять и плоские ключи (например, `batch-size: 256`). Запускайте `go run ./cmd/timemachine --config-yaml config/config.yaml ...` и переопределяйте только необходимые флаги в CLI.

Сгенерировать пример YAML со всеми основными полями можно командой:

```bash
make gen-config-example    # создаст config/config-example.yaml
go run ./cmd/timemachine --generate-config config/custom.yaml  # встроенный генератор
```
Или вручную: `./scripts/gen-config-example.sh config/custom.yaml`.

## Подключение к БД

- `--db postgres://user:pass@host/dbname` — загрузчик `internal/storage/postgres` (pgx/pool). История считывается оконными запросами с `make_interval(microseconds => time_usec)`. Поддерживает `--show-range`.
- `--db clickhouse://user:pass@host:9000/uniset` — загрузчик `internal/storage/clickhouse` (native driver). По умолчанию читает таблицу `main_history` (меняется флагом `--ch-table`, можно указать `db.table`). Ожидает, что имена датчиков из `config/test.xml` записаны в поле `name`. Поддерживает `--show-range`.
- `--db sqlite://test.db` или `--db file:test.db` — загрузчик `internal/storage/sqlite` (database/sql + modernc.org/sqlite). Список датчиков переносится во временную таблицу `tm_sensors`, чтобы не упираться в лимит параметров `IN`, есть команда `--show-range`.
- Без `--db` используется `memstore` с детерминированными данными для тестов.
- Параметр `--window` задаёт длительность окна предварительной выборки (по умолчанию 1 минута). Флаг `--batch-size` ограничивает количество обновлений в одном пакете отправки (по умолчанию 1024).

### Локальный PostgreSQL через Docker

Для интеграционных тестов и ручных прогонов можно поднять Postgres командой:

```bash
make pg-up
CCACHE_DISABLE=1 GOCACHE=$(pwd)/.gocache go test ./...
```

Цель `make pg-up` поднимает контейнер (`docker compose up -d postgres`), экспортирует данные в `main_history` через `scripts/seed-postgres.sh` и сообщает используемый DSN (можно переопределить `TM_POSTGRES_DSN=postgres://... make pg-up`). Для остановки и удаления volume выполните `make pg-down`.

CLI можно запускать с тем же DSN: `go run ./cmd/timemachine --db "$TM_POSTGRES_DSN" ...`. Скрипт `scripts/seed-postgres.sh` переписывает таблицу и кладёт предсказуемые значения датчиков `Input50_S`/`Input51_S`/`Input52_S`.
При первом старте контейнера автоматически выполняется `scripts/postgres-init.sql`, который создаёт базу `uniset` и таблицу `main_history` с нужными колонками.

### ClickHouse через Docker

Чтобы протестировать ClickHouse хранилище, поднимите контейнер и создайте схему:

```bash
make ch-up
```

Команда запускает `clickhouse/clickhouse-server`, ждёт старта и создаёт базу `uniset` с таблицей `main_history` (точно по схеме, приведённой выше). После этого можно подключаться `--db clickhouse://default:@localhost:9000/uniset --ch-table uniset.main_history`.
Остановить сервис можно командой `make ch-down`.

Для генерации, заполнения Postgres и бенча:

```bash
make gen-sensors GEN_SENSORS_START=10001 GEN_SENSORS_COUNT=50000
go run ./cmd/gen-postgres-data --db postgres://admin:123@localhost:5432/uniset?sslmode=disable \
  --confile config/test.xml --selector "Sensor?????_S" --sensors 50000 --points 300 --step 200ms --random 50
go run ./cmd/timemachine --db postgres://admin:123@localhost:5432/uniset?sslmode=disable \
  --confile config/test.xml --slist "Sensor?????_S" --from 2024-06-01T00:00:00Z --to 2024-06-01T00:01:00Z \
  --step 150ms --batch-size 500 --speed 400 --output http://localhost:9191/api/v01/SharedMemory --sm-supplier TestProc -v
```

Для генерации крупных данных и прогона с реальной SM:

```bash
make gen-sensors GEN_SENSORS_START=10001 GEN_SENSORS_COUNT=50000
go run ./cmd/gen-clickhouse-data --db clickhouse://default:@localhost:9000/uniset \
  --table uniset.main_history --confile config/test.xml --selector "Sensor?????_S" \
  --sensors 50000 --points 300 --step 200ms --random 50 --nodename node1 --producer bench
go run ./cmd/timemachine --db clickhouse://default:@localhost:9000/uniset --ch-table uniset.main_history \
  --confile config/test.xml --slist "Sensor?????_S" --from 2024-06-01T00:00:00Z --to 2024-06-01T00:01:00Z \
  --step 150ms --batch-size 500 --speed 400 --output http://localhost:9191/api/v01/SharedMemory \
  --sm-supplier TestProc -v
```

## Тестирование

### Playwright UI тесты

Для запуска UI тестов с session control используется Docker Compose с профилем `tests`:

```bash
# Пересборка и запуск тест-окружения (timemachine с --control-timeout 5s)
docker-compose --profile tests build timemachine
docker-compose --profile tests up -d --force-recreate timemachine

# Запуск всех UI тестов (требует запущенного timemachine контейнера)
docker-compose --profile tests run --rm playwright npx playwright test -c tests/playwright.config.ts --reporter=list

# Запуск только тестов session control
docker-compose --profile tests run --rm playwright npx playwright test tests/ui-session-control-new.spec.ts -c tests/playwright.config.ts --reporter=list

# Полный цикл: пересборка + запуск + тесты
docker-compose --profile tests build timemachine && \
  docker-compose --profile tests up -d --force-recreate timemachine && \
  sleep 5 && \
  docker-compose --profile tests run --rm playwright npx playwright test tests/ui-session-control.spec.ts -c tests/playwright.config.ts --reporter=list
```

Для отладки можно запустить конкретный тест по номеру строки:

```bash
docker-compose --profile tests run --rm playwright npx playwright test 'tests/ui-session-control-new.spec.ts:5' -c tests/playwright.config.ts --reporter=list
```

### Генерация крупных наборов для SQLite

Для нагрузочного теста используйте генератор:

```bash
make gen-sensors GEN_SENSORS_START=10001 GEN_SENSORS_COUNT=50000
make gen-db GEN_DB_SENSORS=5000 GEN_DB_SELECTOR="Sensor1????_S" GEN_DB_POINTS=300 GEN_DB_STEP=200ms GEN_DB_RANDOM=50
CONFIG_YAML=config/config-sqlite.yaml make bench
make clean-bench
```

`make bench` теперь читает параметры из `CONFIG_YAML` (по умолчанию `config/config.yaml`). Хотите другой сценарий — создайте отдельный YAML (например, `config/config-sqlite.yaml` с `db: sqlite://sqlite-large.db`) и передайте `CONFIG_YAML=config/config-sqlite.yaml make bench`. При необходимости добавляйте единичные флаги через `BENCH_FLAGS`, например `BENCH_FLAGS="--show-range" make bench`. `--batch-size` задаёт, сколько обновлений помещается в один `/set`: при ~5000 датчиках каждая итерация разбивается примерно на 10 запросов по 500 записей. Паттерн `Sensor1????_S` охватывает все специальные датчики `Sensor10001_S` … `Sensor15099_S` из `config/test.xml`. Файл `config/generated-sensors.xml` подключён в `config/test.xml` через XInclude, поэтому достаточно пересоздать его командой `make gen-sensors`.

Для быстрой проверки связи с SM используйте `make check-sm SM_TEST_SENSOR=10001 SM_TEST_SUPPLIER=TestProc`, он отправляет одиночный `/set` и проверяет `/get`. Цель автоматически пробует подхватить `SM_CONFIG_YAML` (по умолчанию `config/config.yaml`), чтобы взять `output.sm_url` и `output.sm_supplier` без явных флагов; при необходимости задайте `SM_EXTRA_FLAGS="--value 42"` или выключите YAML `SM_CONFIG_YAML=`. После запуска нагрузочного теста выполните `make clean-bench`, чтобы удалить `sqlite-large.db` и сгенерированный список сенсоров.
