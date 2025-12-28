# Архитектура TimeMachine

## Обзор

TimeMachine — система воспроизведения исторических данных датчиков. Читает изменения из БД (PostgreSQL, SQLite, ClickHouse, InfluxDB) и реконструирует состояние датчиков на заданные моменты времени.

### Ключевые принципы

- В БД хранятся только **моменты изменений** значений датчиков, а не периодические снимки
- Во время воспроизведения для каждого датчика удерживается последнее известное значение для интерполяции
- Данные публикуются в SharedMemory через HTTP `/set` или в stdout для отладки
- UI получает обновления через WebSocket `/api/v2/ws/state`
- Поддерживается работа с большим количеством датчиков (до 100k+)

### Потоки данных

```
CLI/HTTP Server → Config → Storage → Replay Engine → Output Client
                              ↓
                    (PostgreSQL/SQLite/ClickHouse/InfluxDB)
                              ↓
                    (Warmup + Window streaming)
                              ↓
                    (State reconstruction + batching)
                              ↓
                    (HTTP Client → SharedMemory / stdout)
```

---

## Компоненты

### 1. Загрузчик истории (`internal/storage`)

Интерфейс `Storage` реализован для четырёх БД:

#### PostgreSQL (`internal/storage/postgres`)
- Использует `pgx/pgxpool` для работы с пулом соединений
- Warmup через `DISTINCT ON (sensor_id)` для быстрого получения последнего значения перед стартом
- Микросекундная точность через `make_interval(microseconds => time_usec)`

#### SQLite (`internal/storage/sqlite`)
- Использует `modernc.org/sqlite` (pure Go, без CGO)
- Временная таблица `tm_sensors` для обхода лимитов `IN (...)`
- Warmup через оконную функцию `ROW_NUMBER() OVER (PARTITION BY ...)`

#### ClickHouse (`internal/storage/clickhouse`)
- Native протокол через `clickhouse-go/v2`
- Поддержка трёх режимов идентификации: `uniset_hid` (MurmurHash2), `name_hid` (CityHash64), `name` (String)
- Временные таблицы для фильтрации датчиков

#### InfluxDB (`internal/storage/influxdb`)
- HTTP API для InfluxDB 1.x
- Каждый датчик хранится как отдельный measurement
- Значение в поле `value`

#### Общие методы

| Метод | Описание |
|-------|----------|
| `Warmup()` | Загрузка последнего известного значения перед `t_start` для каждого датчика |
| `Stream()` | Потоковая загрузка событий в окнах через канал |
| `Range()` | Получение MIN/MAX timestamp для определения доступного диапазона |

### 2. Состояние воспроизведения (`internal/replay`)

**`replay.Service`** — основной движок воспроизведения.

#### Структуры данных

```go
type sensorState struct {
    value    float64   // текущее значение
    hasValue bool      // было ли установлено значение
    dirty    bool      // изменилось ли с последней отправки
}
```

#### Алгоритм работы

1. **Warmup**: загрузка начального состояния всех датчиков
2. **Streaming**: потоковая загрузка событий в окнах (по умолчанию 1 минута)
3. **Step loop**: для каждого шага:
   - Применить события с `ts <= step_ts`
   - Собрать dirty-датчики
   - Разбить на батчи и отправить
   - Ожидание `step/speed`

#### Управление

Команды через контрольный канал:
- `pause/resume/stop` — управление воспроизведением
- `step/forward`, `step/backward` — пошаговое перемещение
- `seek` — перемотка на указанный timestamp
- `apply` — отправка текущего состояния в SM

#### Кеширование для seek/backward

- `stateCache` хранит снимки состояния в стратегических точках
- При промахе кеша — пересборка через `BuildState()` от начала
- Флаг `LogCache` для отладки попаданий в кеш

### 3. Клиент SharedMemory (`internal/sharedmem`)

Интерфейс:
```go
type Client interface {
    Send(ctx context.Context, payload StepPayload) error
}
```

#### Реализации

| Клиент | Описание |
|--------|----------|
| `HTTPClient` | SharedMemory `/set`, пул воркеров, ретраи, таймауты |
| `StdoutClient` | Вывод в консоль для отладки |

#### Батчинг

- Большие обновления разбиваются на батчи (`--batch-size`, по умолчанию 1024)
- Все батчи одного шага имеют общий `step_id` и последовательный `batch_id`

### 4. HTTP API и Manager (`internal/api`)

**`Manager`** — стейт-машина для управления одной задачей:

```
idle → running → paused → done
         ↑         ↓
         └─────────┘
```

#### Сессии

- Все управляющие эндпоинты требуют заголовок `X-TM-Session`
- Таймаут неактивности `--control-timeout`
- `/api/v2/session/claim` для захвата управления

#### WebSocket

- Протокол: `snapshot` → `updates` → `updates` → ...
- Сообщение `reset` при сбросе задачи
- Метаданные: `controller_present`, `control_timeout_sec`

### 5. Конфигурация (`pkg/config`)

Поддерживаемые форматы:
- UniSet XML (`<sensors><item id="..." name="..." textname="..." iotype="..."/></sensors>`)
- JSON
- YAML (через `--config-yaml`)

#### Резолвинг датчиков

Параметр `--slist` принимает:
- `ALL` — все датчики из конфига
- Имя набора из `sets`
- Список через запятую: `sensor1,sensor2`
- Glob-паттерны: `Sensor100*`, `AI???_S`

---

## Схема БД

### Таблица `main_history`

| Колонка | Тип | Описание |
|---------|-----|----------|
| `sensor_id` | INT64 | ID датчика |
| `timestamp` | TIMESTAMP | Время события (секунды) |
| `time_usec` | INT64 | Микросекунды |
| `value` | FLOAT64 | Значение |

**ClickHouse** дополнительно поддерживает:
- `name` — строковое имя датчика
- `uniset_hid` — MurmurHash2 от имени (UInt32)
- `name_hid` — CityHash64 от имени (UInt64)

### Рекомендуемые индексы

```sql
-- PostgreSQL
CREATE INDEX idx_history_sensor_ts ON main_history (sensor_id, timestamp, time_usec);

-- ClickHouse
ORDER BY (timestamp, name_hid)
```

---

## CLI

```bash
timemachine [flags]
```

### Основные флаги

| Флаг | Описание |
|------|----------|
| `--db` | DSN базы данных (postgres://, sqlite://, clickhouse://, influxdb://) |
| `--confile` | Путь к файлу конфигурации (XML/JSON) |
| `--slist` | Селектор датчиков |
| `--from`, `--to` | Границы периода (RFC3339) |
| `--step` | Шаг воспроизведения (duration) |
| `--speed` | Множитель скорости |
| `--window` | Размер окна загрузки (по умолчанию 1m) |
| `--batch-size` | Размер батча отправки (по умолчанию 1024) |
| `--output` | Вывод: `stdout`, `http://...` |
| `--http-addr` | Адрес HTTP-сервера для режима управления |
| `--control-timeout` | Таймаут сессии управления |
| `--show-range` | Показать доступный диапазон и выйти |

---

## Производительность

### Оптимизации

1. **Window streaming**: загрузка данных окнами предотвращает исчерпание памяти
2. **Виртуальный скроллинг в UI**: рендеринг только видимых строк (~30-50 вместо всех)
3. **Батчинг**: разбиение больших обновлений на части
4. **Кеширование состояния**: быстрый seek назад без полного replay

### Рекомендации

- Для 100k+ датчиков используйте `--window 1m` или меньше
- При медленном SM увеличьте `--batch-size`
- Для отладки используйте `--output stdout`
