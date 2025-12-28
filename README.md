# uniset-timemachine-go

Сервер воспроизведения истории датчиков с веб-интерфейсом. Читает изменения из PostgreSQL/SQLite/ClickHouse/InfluxDB и реконструирует состояние датчиков на заданные моменты времени.

![Главный экран TimeMachine](docs/screenshots/01-main-overview.png)

## Возможности

- Воспроизведение истории датчиков с настраиваемой скоростью
- Веб-интерфейс с управлением через браузер
- REST API для интеграции
- WebSocket-стриминг обновлений в реальном времени
- Поддержка 100k+ датчиков с виртуальным скроллингом
- Графики значений датчиков (Chart.js)
- Публикация в SharedMemory или stdout

## Быстрый старт

### Docker Compose (рекомендуется)

```bash
# Запуск всех сервисов
docker-compose up -d

# Открыть UI
open http://localhost:9090/ui/

# Остановка
docker-compose down
```

### Локальный запуск

```bash
# С демо-данными (memstore)
go run ./cmd/timemachine --http-addr :9090 --output stdout \
  --confile config/test.xml --slist ALL

# С ClickHouse
go run ./cmd/timemachine --http-addr :9090 --output stdout \
  --db clickhouse://default:@localhost:9000/uniset \
  --ch-table uniset.main_history \
  --confile config/test.xml --slist ALL
```

После запуска откройте http://localhost:9090/ui/

## Веб-интерфейс

| Вкладка | Описание |
|---------|----------|
| **Управление** | Play/pause/stop, seek, скорость, установка диапазона |
| **Датчики** | Таблица значений с фильтрацией |
| **Графики** | Визуализация в реальном времени |

![Таблица датчиков](docs/screenshots/04-sensors-table.png)

![Графики](docs/screenshots/07-charts-with-sensor.png)

## Подключение к БД

| Тип | DSN |
|-----|-----|
| PostgreSQL | `--db postgres://user:pass@host/dbname` |
| ClickHouse | `--db clickhouse://user:pass@host:9000/db` |
| SQLite | `--db sqlite://path/to/file.db` |
| InfluxDB | `--db influxdb://host:8086/database` |
| Демо | без `--db` (встроенные данные) |

## Основные флаги

| Флаг | Описание |
|------|----------|
| `--http-addr` | Адрес HTTP-сервера (например `:9090`) |
| `--db` | DSN базы данных |
| `--confile` | Путь к конфигурации датчиков (XML/JSON) |
| `--slist` | Селектор датчиков (`ALL`, паттерн, список) |
| `--output` | Вывод: `stdout` или `http://...` (SharedMemory) |
| `--step` | Шаг воспроизведения (например `1s`) |
| `--speed` | Множитель скорости |

Полный список: `go run ./cmd/timemachine --help`

## Документация

- [HTTP API](docs/API.md) — REST API и WebSocket
- [Архитектура](docs/Architecture.md) — внутреннее устройство
- [Генерация данных](docs/DataGeneration.md) — генераторы тестовых данных
- [Тестирование](docs/Testing.md) — запуск тестов

## Разработка

```bash
# Тесты
go test ./...

# UI тесты (Playwright)
docker-compose --profile tests run --rm playwright
```

---

При разработке использовался [Claude Code](https://claude.ai/code).
