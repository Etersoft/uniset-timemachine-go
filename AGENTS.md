# Repository Guidelines

uniset-timemachine-go — консольный проигрыватель истории датчиков на Go. Он читает изменения из PostgreSQL/SQLite, интерполирует состояния на заданном шаге и отправляет обновления в SharedMemory (пока что заменён на вывод в stdout). В этой ветке занимаемся только CLI-версией; GTK-оболочка и Python-скрипты старого прототипа не трогаем.

## Оперативные ограничения
- Не коммитим изменения без явной команды пользователя.

## Структура проекта
- `cmd/timemachine/main.go` — единственная точка входа, разбирает флаги (`--db`, `--confile`, `--slist`, `--from/--to`, `--step`, `--window`, `--speed`, `--batch-size`, `--show-range`, `--output`, `--config-yaml`, `-v`) и запускает сервис проигрывания.
- `pkg/config` — загрузка XML/JSON UniSet, разрешение имён датчиков и наборов (`config/test.xml`, `config/example.json` помогают тестировать вручную; генератор `cmd/gen-sensors-xml` создаёт включаемый блок датчиков).
- `internal/storage` — интерфейс `Storage` и реализации:
  - `postgres` (pgx/pool) с Warmup/Stream/Range запросами к `main_history`.
  - `sqlite` (database/sql + modernc.org/sqlite) с временной таблицей `tm_sensors`.
  - `memstore` — детерминированные данные для тестов и запуска без БД.
  - `clickhouse` — чтение из `uniset.main_history` на MergeTree через `go-clickhouse`.
- `internal/replay` — основной цикл проигрывания: подгружает окна истории, ведёт состояние датчиков, бьёт обновления на батчи и отправляет клиенту.
- `internal/sharedmem` — интерфейс клиента SharedMemory, есть HTTP-клиент с `/set`/`/get` (поддерживает supplier=TestProc по умолчанию, ID-параметры, режим `-v`).
- `cmd/gen-clickhouse-data`, `cmd/gen-postgres-data`, `cmd/gen-sqlite-data` — сидеры для больших объёмов истории.
- `cmd/sm-test` — проверка `set/get` против реальной SM.
- `docker-compose.yml` + `scripts/` — инфраструктура БД (инит-SQL для Postgres/ClickHouse, сидеры, вспомогательные shell-скрипты).
- `vendor/` — зависимости `go mod vendor`; не редактируйте вручную.

## Команды сборки и запуска
- `go build ./cmd/timemachine` — собирает бинарь CLI; переменные по умолчанию можно хранить в `.env`.
- `go test ./...` — выполняет unit-тесты (`pkg/config`, `internal/replay`, `internal/sharedmem` и т.д.); перед пушем обязательно прогоняйте.
- `go run ./cmd/timemachine --confile config/example.json --slist ALL --db sqlite://test.db --from 2024-06-01T00:00:00Z --to 2024-06-01T00:05:00Z --step 1s` — быстрый прогон на SQLite-файле (по умолчанию отправляет `/set` на `http://localhost:9191/api/v01/SharedMemory` с `supplier=TestProc`; при необходимости задайте `--output http://...`, `--sm-supplier`, `--sm-param-mode`, `--sm-param-prefix`, `-v`). Для PostgreSQL укажите URL вида `postgres://user:pass@host/db`. Для ClickHouse используйте `clickhouse://user:pass@host:9000/uniset` и флаг `--ch-table`, если таблица отличается. `--slist` поддерживает glob-паттерны, например `--slist "Sensor100*"`.
- `go run ./cmd/timemachine --config-yaml config/config.yaml ...` — загрузить значения флагов из YAML (поддерживаются секции `database/sensors/output`, например `database.dsn -> --db`, `output -> --output`) и переопределять только отличия в командной строке.
- `make help` — список доступных целей.
- `make pg-up` / `make pg-down` — поднять/остановить Postgres в docker; `scripts/postgres-init.sql` монтируется в `/docker-entrypoint-initdb.d` и создаёт `main_history`. Скрипт `scripts/seed-postgres.sh` докидывает тестовые данные согласно `TM_POSTGRES_DSN`.
- `make ch-up` / `make ch-down` — поднять/остановить ClickHouse; `scripts/clickhouse-init.sql` автоматически создаёт схему `uniset.main_history`.
- `make gen-sensors GEN_SENSORS_START=10001 GEN_SENSORS_COUNT=50000` — сгенерировать дополнительный блок сенсоров (через XInclude в `config/test.xml`). `make clean-bench` удаляет `sqlite-large.db` и `config/generated-sensors.xml`.
- `make gen-db` — создать SQLite-набор (`sqlite-large.db`) для бенчмарков; параметры управляются `GEN_DB_*`.
- `make bench` — прогнать `timemachine` по настройкам из `CONFIG_YAML` (по умолчанию `config/config.yaml`); для точечных правок используйте `BENCH_FLAGS="--db sqlite://..."`.
- `make check-sm SM_TEST_SENSOR=10001 SM_TEST_SUPPLIER=TestProc` — вызвать `cmd/sm-test`, который делает `/set` и `/get` против SharedMemory (поддерживает `SM_CONFIG_YAML`, берёт `output.sm_url`/`output.sm_supplier` из YAML; по умолчанию сенсор 10001).
- `go run ./cmd/gen-clickhouse-data --db clickhouse://default:@localhost:9000/uniset --table uniset.main_history --confile config/test.xml --selector "Sensor?????_S" --sensors 50000 --points 300 --step 200ms --random 50 --nodename node1 --producer bench` — массово наполнить ClickHouse, после чего прогонять `timemachine --db clickhouse://...`.
- `go run ./cmd/gen-postgres-data --db postgres://admin:123@localhost:5432/uniset?sslmode=disable --confile config/test.xml --selector "Sensor?????_S" --sensors 50000 --points 300 --step 200ms --random 50` — аналогично наполнить Postgres (при необходимости задаём `--offset` батчами для >10k сенсоров).
- `go run ./cmd/timemachine --show-range --db postgres://... --confile config/test.xml --slist SensorA,SensorB` — проверить доступный диапазон и выйти.
- Компания предпочитает локальные окружения через `python3 -m venv` только для вспомогательных скриптов; сам плеер полностью на Go.

## Кодстайл и практики
- Следуем стандартному стилю Go: `gofmt`/`goimports` обязательны, без ручных табов и варнингов go vet. Структуры и файлы документируем комментариями на русском или английском, если экспортируются за пакет.
- Параметры времени, контексты и каналы передаём явными аргументами; не используем глобальные переменные для конфигурации.
- В `internal/replay` избегаем блокировок на долгие операции в горячем цикле: крупные вычисления — вне критических секций, уважайте `ctx`.
- В `internal/storage` не дублируем SQL: общие константы и вспомогательные функции (нормализация timestamp, фильтры) держим рядом; настройки окон и батчей вынесены в параметры `StreamRequest`.
- Модули в `internal/` не должны экспортировать типы наружу; всё публичное API находится в `pkg/` либо `cmd/`.

## Тестирование
- Базовый минимум — `go test ./...`. Если меняли `pkg/config`, добавьте кейсы с XML/JSON; если модифицировали `internal/replay`, покрывайте поведение шагов и батчей, а для `internal/sharedmem/httpclient` пишите интеграционные тесты с `httptest.Server`.
- В среде с ограничениями песочницы используйте `GOFLAGS=-mod=mod` и запускайте команды, требующие сетевого доступа (скачивание модулей/доступ к БД/докеру), с `with_escalated_permissions: true`, чтобы избегать ошибок `import lookup disabled by -mod=vendor` и `socket: operation not permitted`.
- Перед нагрузочными тестами SharedMemory выполните `make check-sm` (делает `/set` и `/get` для сенсора 10001) и убедитесь, что SM поднят на `http://localhost:9191/api/v01/SharedMemory`.
- Изменения в хранилищах проверяйте минимум на SQLite и Postgres, а при работе с ClickHouse дополнительно прогоняйте `go run ./cmd/timemachine ... --db clickhouse://... --ch-table uniset.main_history` после `make ch-up` и наполнения `gen-clickhouse-data`.
- Для больших серий (5k/50k датчиков) пользуйтесь `make gen-sensors`, `make gen-db` либо сидерами `cmd/gen-*-data` и моделируйте шаг 150 мс, батчи по 500 элементов; фиксируйте среднее время `/set` в логах `-v`.
- Для `--show-range` и новых CLI-флагов приводите пример запуска в описании PR/коммита.
- Если правите SQL, прогоните хотя бы smoke-тесты на промежутке 5–10 минут и убедитесь, что `Warmup` возвращает последнее значение до `--from`.

## Оформление коммитов и PR
- Заголовки до ~60 символов, допускаются префиксы в квадратных скобках (`[replay] batch splitting`). В теле перечисляйте ключевые изменения и manual test plan.
- Упоминайте задачи `Fixes #NN`/`Refs #NN`, если есть, и кратко опишите влияние на схемы БД или протокол SharedMemory.
- PR сопровождаем примером CLI-команды (и выводом) для новых опций, а также ссылками на обновлённые конфиги/SQL, если таковые есть.

## Безопасность и конфигурация
- Не коммитим реальные креды БД: храните их в переменных окружения (`PGUSER`, `PGPASSWORD`, `UNLOG_DB_URL`) или локальных файлах, которые занесены в `.gitignore`.
- `test.db` и `config/test.xml` служат для отладки; не модифицируйте их без необходимости. Любые новые тестовые данные складывайте в `config/` или `scripts/`.
- При демонстрациях используйте флаг `--output stdout` или будущий `--not-save-to-sm`, чтобы не писать в продовые сегменты SharedMemory.
- SQL/Go-код должен корректно обрабатывать отсутствие данных (нулевые диапазоны времени, пустые выборки) и не паниковать на входах пользователя.
