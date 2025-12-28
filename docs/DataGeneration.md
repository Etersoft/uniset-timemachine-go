# Генерация тестовых данных

Для тестирования и бенчмаркинга TimeMachine предусмотрены генераторы данных для всех поддерживаемых БД.

## Генерация конфигурации датчиков

Перед генерацией данных необходимо создать конфигурацию датчиков:

```bash
make gen-sensors GEN_SENSORS_START=10001 GEN_SENSORS_COUNT=50000
```

Это создаст файл `config/generated-sensors.xml`, который подключается к `config/test.xml` через XInclude. Паттерн `Sensor1????_S` охватывает датчики `Sensor10001_S` … `Sensor19999_S`.

## PostgreSQL

### Генератор данных

```bash
go run ./cmd/gen-postgres-data \
  --db postgres://admin:123@localhost:5432/uniset?sslmode=disable \
  --confile config/test.xml \
  --selector "Sensor?????_S" \
  --sensors 5000 \
  --duration 10m
```

Основные флаги:

| Флаг | Описание | По умолчанию |
|------|----------|--------------|
| `--db` | PostgreSQL DSN | `postgres://admin:123@localhost:5432/uniset?sslmode=disable` |
| `--confile` | Путь к конфигу датчиков | `config/test.xml` |
| `--selector` | Селектор датчиков (glob) | `ALL` |
| `--sensors` | Лимит датчиков (0 = все) | 0 |
| `--duration` | Длительность истории | 10m |
| `--start` | Начальный timestamp (RFC3339) | 7 дней назад |
| `--batch` | Размер батча вставки | 10000 |
| `--truncate` | Очистить таблицу перед вставкой | false |
| `--sql-output` | Записать SQL в файл вместо вставки | — |

## ClickHouse

### Генератор данных

```bash
go run ./cmd/gen-clickhouse-data \
  --db clickhouse://default:@localhost:9000/uniset \
  --table uniset.main_history \
  --confile config/test.xml \
  --selector "Sensor?????_S" \
  --sensors 5000 \
  --duration 10m \
  --nodename node1 \
  --producer bench
```

Основные флаги:

| Флаг | Описание | По умолчанию |
|------|----------|--------------|
| `--db` | ClickHouse DSN | `clickhouse://default:@localhost:9000/uniset` |
| `--table` | Таблица (db.table) | `uniset.main_history` |
| `--confile` | Путь к конфигу датчиков | `config/test.xml` |
| `--selector` | Селектор датчиков (glob) | `ALL` |
| `--sensors` | Лимит датчиков (0 = все) | 0 |
| `--duration` | Длительность истории | 10m |
| `--start` | Начальный timestamp (RFC3339) | 7 дней назад |
| `--nodename` | Значение колонки nodename | `node1` |
| `--producer` | Значение колонки producer | `gen-data` |
| `--batch` | Размер батча вставки | 10000 |
| `--truncate` | Очистить таблицу перед вставкой | false |
| `--sql-output` | Записать SQL в файл вместо вставки | — |

## InfluxDB

### Генератор данных

```bash
go run ./cmd/gen-influxdb-data \
  --db influxdb://localhost:8086/uniset \
  --confile config/test.xml \
  --selector "Sensor?????_S" \
  --sensors 1000 \
  --duration 30m
```

Или через make:

```bash
# С параметрами по умолчанию
make influx-gen-data

# С указанием параметров
make influx-gen-data GEN_INFLUX_SENSORS=1000 GEN_INFLUX_DURATION=30m

# Сохранить Line Protocol в файл
make influx-gen-data GEN_INFLUX_LP_OUTPUT=data.lp
```

## SQLite

### Генератор данных

```bash
go run ./cmd/gen-sqlite-data \
  --db sqlite://test.db \
  --confile config/test.xml \
  --selector "Sensor?????_S" \
  --sensors 5000 \
  --duration 10m
```

Или через make:

```bash
make gen-db GEN_DB_SENSORS=5000 GEN_DB_SELECTOR="Sensor1????_S" GEN_DB_DURATION=10m
```

## Бенчмаркинг

### Полный цикл SQLite

```bash
# 1. Генерация конфигурации
make gen-sensors GEN_SENSORS_START=10001 GEN_SENSORS_COUNT=50000

# 2. Генерация данных
make gen-db GEN_DB_SENSORS=5000 GEN_DB_SELECTOR="Sensor1????_S" GEN_DB_DURATION=10m

# 3. Запуск бенчмарка
CONFIG_YAML=config/config-sqlite.yaml make bench

# 4. Очистка
make clean-bench
```

### Полный цикл ClickHouse

```bash
# 1. Поднять контейнер
make ch-up

# 2. Генерация конфигурации
make gen-sensors GEN_SENSORS_START=10001 GEN_SENSORS_COUNT=50000

# 3. Генерация данных
go run ./cmd/gen-clickhouse-data \
  --db clickhouse://default:@localhost:9000/uniset \
  --table uniset.main_history \
  --confile config/test.xml \
  --selector "Sensor?????_S" \
  --sensors 50000 \
  --duration 10m \
  --truncate

# 4. Запуск timemachine
go run ./cmd/timemachine \
  --db clickhouse://default:@localhost:9000/uniset \
  --ch-table uniset.main_history \
  --confile config/test.xml \
  --slist "Sensor?????_S" \
  --from 2024-06-01T00:00:00Z \
  --to 2024-06-01T00:10:00Z \
  --step 150ms \
  --batch-size 500 \
  --speed 400 \
  --output stdout
```

## Проверка связи с SharedMemory

```bash
make check-sm SM_TEST_SENSOR=10001 SM_TEST_SUPPLIER=TestProc
```

Команда отправляет одиночный `/set` и проверяет `/get`. Автоматически использует `output.sm_url` и `output.sm_supplier` из `config/config.yaml`.
