#!/usr/bin/env bash
set -euo pipefail

out="${1:-config/config-example.yaml}"

cat >"$out" <<'YAML'
# Пример конфигурации timemachine (все основные поля).

http:
  addr: :9090  # HTTP UI/API. Пусто, если не нужен server-режим.

database:
  # Тип хранилища: clickhouse | postgres | sqlite
  type: clickhouse
  # ClickHouse
  dsn: clickhouse://default:@localhost:9000/uniset
  table: uniset.main_history
  # PostgreSQL (пример)
  # type: postgres
  # dsn: postgres://admin:123@localhost:5432/uniset?sslmode=disable
  # SQLite (пример)
  # type: sqlite
  # dsn: sqlite://sqlite-demo.db
  # Доп. параметры чтения
  window: 15s          # длительность окна подкачки
  step: 1s             # шаг интерполяции (для memstore/sqlite, если не задан через CLI)
  speed: 1             # множитель скорости проигрывания (1 — realtime)
  batch_size: 1024     # макс. обновлений в одном батче отправки
  ws_batch_time: 100ms # слайс времени для батчирования WS
  sqlite_cache_mb: 1000
  sqlite_wal: true
  sqlite_sync_off: true
  sqlite_temp_mem: true

sensors:
  config: config/test.xml
  selector: ALL        # имя набора/маска/список имён/ALL
  from: 2024-06-01T00:00:00Z
  to: 2024-06-01T00:09:55Z
  step: 1s             # шаг интерполяции
  window: 15s          # окно подкачки истории
  speed: 1

output:
  mode: stdout         # stdout | http (SharedMemory)
  sm_url: http://localhost:9191/api/v01/SharedMemory
  sm_supplier: TestProc
  sm_param_mode: id    # id | name
  sm_param_prefix: id
  batch_size: 1024
  verbose: false

logging:
  cache: false
YAML

echo "Example config written to $out"
