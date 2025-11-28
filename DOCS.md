# HTTP API управления

API однозадачное: в любой момент может быть только одна активная задача (running/paused). Параметры БД/SM/таблицы задаются только при запуске сервера (`timemachine --http-addr ...`), через API передаются лишь границы периода, шаг и параметры шага.

## Запуск сервера

```bash
go run ./cmd/timemachine \
  --http-addr :8080 \
  --db clickhouse://default:@localhost:9000/uniset \
  --ch-table uniset.main_history \
  --confile config/test.xml \
  --slist "Sensor?????_S" \
  --output http \
  --output http://localhost:9191/api/v01/SharedMemory \
  --sm-supplier TestProc
```

## Эндпоинты

- `GET /healthz` — liveness.
- `GET /ui/` — простой веб-интерфейс (встроенная статика).
  - API допускает CORS с `Access-Control-Allow-Origin: *`, поэтому `/ui/` можно открывать даже с `file://` или с отдельного домена; предзапросы `OPTIONS` поддерживаются.
- `GET /api/v2/ws/state` — WebSocket поток обновлений таблицы датчиков. При подключении приходит snapshot (`{type:"snapshot", step_id, step_ts, step_unix, updates:[{id,name,textname,value?,has_value?}]}`), далее дельты по шагам (`{type:"updates", step_id, step_ts, step_unix, updates:[{id,value,has_value?}]}`). Если таймстамп одинаков для всех датчиков, он передаётся в `step_ts/step_unix`, а в элементах — только `id/value`. Без upgrade вернёт `400/426`, а при отсутствующем streamer — `503`.
- `/debug/pprof/*` — стандартные endpoint’ы pprof для съёма профилей (CPU/heap/trace) во время работы.

### API v2 (pending range/seek)

- `GET /api/v2/sensors` — список датчиков (`id,name,textname,iotype`) и `count`.
- `GET /api/v2/job/sensors/count?from=...&to=...` — количество уникальных датчиков в выбранном диапазоне.
- `POST /api/v2/job/range` — сохранить диапазон/шаг/скорость/окно без старта. `GET /api/v2/job/range` — вернуть доступный min/max и `sensor_count`.
- `POST /api/v2/job/seek` — перемотка; если job не запущен, запоминает pending seek.
- `POST /api/v2/job/start` — запустить задачу, используя pending range/seek.
- `POST /api/v2/job/reset` — сбросить состояние сервера: остановить задачу, очистить pending range/seek, отправить `reset` в WebSocket.
- `POST /api/v2/job/pause|resume|stop|apply|step/forward|step/backward` — команды управления.
- `GET /api/v2/job` — статус + pending (`range_set`, `range`, `seek_set`, `seek_ts`).
- `POST /api/v2/snapshot` — одноразовый расчёт состояния на `ts` без записи в SM.

### Старт (v2)

```bash
curl -s -X POST http://localhost:8080/api/v2/job/range \
  -d '{"from":"2024-06-01T00:00:00Z","to":"2024-06-01T00:00:10Z","step":"1s","speed":1,"window":"15s"}'
curl -s -X POST http://localhost:8080/api/v2/job/start
curl -s -X POST http://localhost:8080/api/v2/job/reset
```

Ответ: `{"status":"running"}`. При активной задаче `/start` возвращает `409` с сообщением `job is already active`.

### Статус

```bash
curl -s http://localhost:8080/api/v2/job
```

Пример ответа:

```json
{
  "status": "paused",
  "params": {
    "Sensors": [10001,10002,10003],
    "From": "2024-06-01T00:00:00Z",
    "To": "2024-06-01T00:00:29Z",
    "Step": 1000000000,
    "Window": 15000000000,
    "Speed": 1,
    "BatchSize": 16
  },
  "started_at": "2025-11-20T22:35:30.195247688+03:00",
  "finished_at": "0001-01-01T00:00:00Z",
  "step_id": 23,
  "last_ts": "2024-06-01T00:00:22Z",
  "updates_sent": 69
}
```

### Пауза/возобновление/остановка

```bash
curl -X POST http://localhost:8080/api/v2/job/pause    # {"status":"ok"}
curl -X POST http://localhost:8080/api/v2/job/resume   # {"status":"ok"}
curl -X POST http://localhost:8080/api/v2/job/stop     # {"status":"ok"}
```

### Шаги

```bash
# шаг вперёд из paused
curl -X POST http://localhost:8080/api/v2/job/step/forward

# шаг назад (без отправки в SM)
curl -X POST http://localhost:8080/api/v2/job/step/backward -d '{"apply":false}'
```

### Seek

```bash
# перемотать к моменту и отправить итоговое состояние в SM
curl -X POST http://localhost:8080/api/v2/job/seek -d '{"ts":"2024-06-01T00:00:10Z","apply":true}'
```

При `apply:false` состояние остаётся только внутри проигрывателя. При seek/step назад промежуточные шаги не отправляются в SM; финальное состояние уходит одиночным шагом только если `apply=true` или вызван `/apply`.

### Apply текущего состояния

```bash
curl -X POST http://localhost:8080/api/v2/job/apply
```

Отправляет текущее состояние (в paused) в SM одним `StepPayload`, деля на батчи по `batch_size`.

### Snapshot

```bash
curl -X POST http://localhost:8080/api/v2/snapshot -d '{"ts":"2024-06-01T00:00:05Z"}'
```

Не влияет на текущую задачу и не пишет в SM. Ответ: `{"ts":"2024-06-01T00:00:05Z","duration_ms":12,"status":"ok"}`.

### Healthz

```bash
curl -s http://localhost:8080/healthz   # ok
```

## Поведение и ограничения

- Только одна активная задача (running/paused/stopping). Новый старт при активной — `409`.
- Шаг назад и seek переигрывают историю до нужного времени без промежуточных отправок в SM.
- Шаг вперёд/назад выполняются из `paused` и оставляют задачу в `paused`; при достижении начала/конца диапазона кнопки в UI блокируются.
- Snapshot/state не возвращают значения датчиков — только метаданные; при необходимости можно расширить API.
- Все сетевые/SM параметры задаются при старте процесса, не через API.
