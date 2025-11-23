## Детали и договорённости

- Разрабатываем только консольный вариант тайм-машины; GTK-интерфейс в этой ветке не обновляем.
- Источники данных — БД с изменениями датчиков (PostgreSQL, SQLite и т.п.), где записаны только моменты перемен значений.
- Во время проигрывания удерживаем для каждого датчика последнее известное значение (`last_value`) и timestamp, чтобы интерполировать шаги, на которых обновлений не было.
- Данные для отображения публикуем в SharedMemory через HTTP `/set` (либо `stdout` для отладки); WebSocket пока не трогаем.
- Отправляем изменения батчами ограниченного размера; если дельта шага большая, делим её на несколько запросов с общим `step_id`.
- HTTP-клиент для SM умеет пул воркеров/ретраи/таймауты (см. `internal/sharedmem/http_client.go`), по умолчанию один поток.
- Основной цикл проигрывания заранее буферизует окно изменений (например, 1–5 минут вперёд), чтобы успевать подготавливать значения даже при больших периодах и количестве датчиков (до ~100k).

## HTTP API (MVP, однозадачное управление)

- Режим запуска: `timemachine --http-addr :8080 ...`, все параметры БД/SM/таблиц задаются только при запуске сервиса (не через API).
- Одна активная задача. Попытка старта, когда уже есть running/paused задача, возвращает `409`.
- Эндпоинты:
  - `POST /api/v1/job` — старт задачи. JSON body: `from` (RFC3339, обязательный), `to` (RFC3339, обязательный), `step` (duration, обязательный), `speed` (float, опционально), `window` (duration, опционально). Ответ: `{"status":"running"}`.
  - `POST /api/v1/job/pause` — переводит задачу в `paused`; ожидание в точке текущего шага.
  - `POST /api/v1/job/resume` — продолжает проигрывание с тем же шагом и скоростью.
  - `POST /api/v1/job/step/forward` — когда `paused`, выполняет один шаг вперёд (отправляет обновления за следующий шаг), оставаясь в `paused`.
  - `POST /api/v1/job/step/backward` — когда `paused`, перематывает на один шаг назад (переигрывает историю до нужного шага), остаётся в `paused`.
  - `POST /api/v1/job/seek` — перематывает на указанный `ts` (RFC3339). Перед перемоткой ставит задачу в `paused`, прогоняет историю до `ts` без отправки промежуточных шагов в SM; если в теле `apply:true`, после достижения `ts` отправляет финальное состояние одним шагом (batch-логика сохраняется) и остаётся в `paused`. При `apply:false` состояние только внутри игрока.
  - `POST /api/v1/job/apply` — когда `paused` (после seek/step), отправляет текущее состояние в SM одним шагом и остаётся в `paused`.
  - `POST /api/v1/job/stop` — мягко отменяет задачу и освобождает слот.
  - `GET /api/v1/job` — статус: `status` (`idle|running|paused|stopping|done|failed`), `params`, `started_at`, `finished_at`, `step_id`, `last_ts`, `updates_sent`, `error`.
  - `GET /api/v1/job/state` — метаданные текущего состояния (когда `paused`/`running`): `step_id`, `last_ts`, `updates_sent`; без выдачи значений датчиков (можно расширить позже).
  - `POST /api/v1/snapshot` — одноразовый расчёт состояния на момент `ts` (RFC3339, опционально `slist`). Не влияет на текущую задачу и не пишет в SM. Ответ без данных (статус/тайминги); при необходимости выдачи значений расширим.
  - `GET /healthz` — liveness.
- Семантика шага назад: без хранения снапшотов. При `step/backward` останавливаем текущий стрим и переигрываем историю от `from` до целевого шага с `speed=∞`, чтобы восстановить состояние, затем остаёмся в `paused`. Позже можно добавить кеш шагов/дельт, если потребуется частая перемотка.
- Пауза/степы реализуются через контрольный канал у `replay.Service`: блокируем ожидание следующего шага, пока не придёт `resume` или разовая команда `step`.

## Архитектура воспроизведения (рабочая версия)

1. **Загрузчик истории (`internal/storage`)**
   - На вход получает список `sensor_id`, границы периода (`t_start`, `t_end`) и размер окна `window`.
   - PostgreSQL-версия (`internal/storage/postgres`) использует `pgx`/`pgxpool`: для каждой порции окна выполняет
     ```sql
     SELECT sensor_id, timestamp, COALESCE(time_usec,0) AS usec, value
     FROM main_history
     WHERE sensor_id = ANY($1)
       AND (timestamp + make_interval(microseconds => COALESCE(time_usec,0))) BETWEEN $2 AND $3
     ORDER BY timestamp, usec, sensor_id;
     ```
     Возвращённые строки преобразуются в `storage.SensorEvent` с точным `time.Time`.
   - Вариант для SQLite (`internal/storage/sqlite`) использует `database/sql` + `modernc.org/sqlite`, перед выполнением запросов записывает ID датчиков во временную таблицу `tm_sensors` (чтобы не упираться в лимиты `IN`). Warmup реализован через оконную функцию `ROW_NUMBER() OVER (PARTITION BY sensor_id ORDER BY timestamp DESC, usec DESC)` с фильтром по Unix microseconds.
   - Возвращает результат через канал `chan []SensorEvent`, где каждый элемент отсортирован по timestamp внутри пачки.
   - Поддерживает два режима: начальная выборка «последнего известного значения» (поиск записи перед `t_start`) и потоковая подгрузка окон вперёд.
   - Warmup-запрос в Postgres использует `DISTINCT ON (sensor_id)` чтобы быстро получить последнее значение перед стартом периода; также реализован метод Range (MIN/MAX timestamp) для CLI-опции `--show-range`. В SQLite аналогичный запрос делается через `MIN/MAX`.

2. **Состояние воспроизведения (`replay.Service`)**
   - Для каждого датчика держит `sensorState` с последним значением и флагом `dirty`.
   - Применяет тёплый старт `Warmup`, затем на каждом шаге досливает pending-события `<= step_ts`, маркируя соответствующие датчики грязными.
   - Собирает обновления только из `dirty` датчиков и сбрасывает флаги после отправки.

3. **Цикл шагов и управление (`replay.Service`)**
   - Обрабатывает команды `pause/resume/stop/step/seek/apply` через управляющий канал; `step backward/seek` восстанавливает состояние через кеш снапшотов и догонку событий, при необходимости пересобирает через `BuildState`.
   - На каждом шаге делит дельту по `batch_size`, вызывает `Output.Send` для каждой пачки, обновляет статистику `step_id/last_ts/updates_sent` и ждёт задержку `step/speed`.
   - Логи попаданий кеша можно включить флагом `Service.LogCache`.

4. **Клиент SharedMemory (`internal/sharedmem`)**
   - Экспортирует интерфейс `type Client interface { Send(ctx context.Context, payload StepPayload) error }`.
   - Реализации: `HTTPClient` (SharedMemory `/set`) и `StdoutClient` (печать для тестов); WebSocket нет.
   - `StepPayload` включает `StepID`, `StepTs`, `BatchID`, `BatchTotal` и список `SensorUpdate {ID, Value}`; `replay` делит большой шаг на несколько батчей и последовательно вызывает `Send`.

5. **CLI (`cmd/timemachine`)**
   - Парсит параметры (`--db`, `--confile`, `--slist`, `--from`, `--to`, `--step`, `--window`, `--speed`, `--batch-size`, `--output`, `--sm-*`, `--ch-table`, `--config-yaml`, `--http-addr`, `--show-range`, `-v`).
   - Настраивает компоненты и запускает `Run` (CLI) либо HTTP-сервер управления (`--http-addr`).

## Формат БД (по прототипу)

- `main_sensor` — справочник датчиков.
  - `id` (PK, int/autoincrement)
  - `name` (строковый идентификатор)
  - `textname` (описание)
- `main_history` — история изменений.
  - `id` (PK, autoincrement)
  - `timestamp` (`TIMESTAMP`) — секунды.
  - `time_usec` (`INTEGER`) — микросекунды (если нужно объединять с `timestamp` → `timestamp + usec/1e6`).
  - `sensor_id` (`INTEGER`, FK на `main_sensor.id`)
  - `value` (`FLOAT`)
  - `node_id` (`INTEGER`, FK на `main_sensor.id`, хранит узел/контекст)
  - `confirm` (`INTEGER`, nullable) — время подтверждения в секундах (может быть опциональным для консольной версии).

В Go-наборах достаточно полей `sensor_id`, `timestamp`+`time_usec`, `value`, `node_id`.

### Требования к чтению истории

- **Warmup**: для каждого датчика нужен последний `main_history` перед `t_start`. Запрос:  
  `SELECT DISTINCT ON (sensor_id) sensor_id, timestamp, time_usec, value FROM main_history WHERE sensor_id IN (...) AND (timestamp, time_usec) <= (:from_ts, :from_usec) ORDER BY sensor_id, timestamp DESC, time_usec DESC;`
- **Окна**: основной запрос, который стримит изменения пакетами по `timestamp`.  
  `SELECT sensor_id, timestamp, time_usec, value FROM main_history WHERE sensor_id IN (...) AND (timestamp, time_usec) >= (:window_start) AND (timestamp, time_usec) < (:window_end) ORDER BY timestamp, time_usec, sensor_id LIMIT :batch`.
- **Индексы**: нужен составной индекс `(sensor_id, timestamp, time_usec)` и, по возможности, BRIN по `timestamp` для больших объёмов.
- **Кондиции**: чётко задаём `LIMIT` и `ORDER BY` с пагинацией по `(timestamp, time_usec, id)` чтобы не терять записи при длинных периодах.

Go-структуры:

```go
type Sensor struct {
	ID       int64
	Name     string
	TextName string
}

type HistoryRow struct {
	SensorID int64
	Ts       time.Time // комбинируем timestamp + usec
	Value    float64
	NodeID   int64
}
```

## Конфигурация и список датчиков

- Файл конфигурации (UniSet XML или JSON) хранит:
  - параметры подключения к БД (опционально),
  - словарь `sensors` (`name -> id`),
  - необязательные наборы (`sets`: имя → массив имён датчиков).
- Для UniSet XML используем секцию `<sensors><item id="..." name="..." textname="..."/></sensors>`; поля `textname`, `iotype` сохраняем в `SensorMeta`.
- CLI-параметр `--slist` принимает:
  - `ALL` — взять все `id` из `sensors`,
  - имя набора — раскрыть через `sets`,
  - список через запятую (например, `sensor1,sensor5`),
  - glob-паттерны (например, `Sensor100*`).
- `pkg/config` должен предоставить:

```go
type Config struct {
	Sensors map[string]int64
	Sets    map[string][]string
}

func (c *Config) Resolve(set string) ([]int64, error)
```

- `--config-yaml` позволяет задать значения флагов через структурированный YAML (`database.*`, `sensors.*`, `output.*` и плоские ключи), CLI переопределяет приоритет поверх YAML.
- CLI при старте загружает конфиг, вызывает `Resolve`, затем передаёт ID в `storage`/`replay`.
- Флаг `--batch-size` задаёт максимальное количество обновлений в одном пакете отправки (по умолчанию 1024); `replay.Service` при необходимости делит один шаг на несколько `StepPayload`.
- Флаг `--show-range` обращается к `Storage.Range` и печатает доступный интервал без запуска проигрывания.

## Ближайшие технические задачи

1. **Тесты HTTP API** — покрыть `internal/api` (start/409, pause/resume/stop, step backward/seek/apply, snapshot) через `httptest.Server`, фиксируя переходы статусов и поля `step_id/last_ts`.
2. **Отправка в SM** — добавить пул воркеров и ограничение по таймаутам/ретраям для HTTP-клиента, чтобы длинные `/set` не блокировали следующий шаг; сохранить метрики (среднее время/ошибки) в логах.
3. **Кеш для step backward/seek** — хранить буфер шагов/дельт или периодические снапшоты, чтобы не пересчитывать состояние заново через `BuildState` при каждом откате/seek.
4. **YAML для serve-режима** — расширить маппинг `--config-yaml`, чтобы задавать `http-addr` (и другие серверные параметры) без длинных CLI-строк.
