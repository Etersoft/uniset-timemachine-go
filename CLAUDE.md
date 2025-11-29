# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**uniset-timemachine-go** is a Go-based sensor data replay system that reads historical sensor changes from databases (PostgreSQL, SQLite, ClickHouse) and reconstructs their states at specified time intervals. It operates in two modes:

1. **CLI Mode**: Direct playback with output to stdout or SharedMemory HTTP API
2. **HTTP API Mode**: REST API with WebSocket streaming and interactive web UI for timeline control

The system is designed for testing and debugging sensor behavior by replaying historical data from UniSet monitoring systems.

## Build and Run Commands

**IMPORTANT: Development Workflow**

**Preferred approach: Use docker-compose for running the server and databases.**

**Mandatory steps after making changes:**

1. **Code changes (Go files)**: ALWAYS run tests before committing
   ```bash
   go test ./...
   ```

2. **UI changes (`internal/api/ui/index.html`)**: MUST rebuild and restart the server
   ```bash
   # Rebuild Docker image
   docker-compose build timemachine

   # Restart the service
   docker-compose restart timemachine

   # Or rebuild and restart in one command
   docker-compose up -d --build timemachine
   ```

3. **After any changes**: Verify the application works correctly through the web UI at http://localhost:9090/ui/

### Docker Compose (Recommended)

```bash
# Start all services (Postgres, ClickHouse, TimeMachine app)
docker-compose up -d

# Start with test profile (includes Playwright tests)
docker-compose --profile tests up -d

# View logs
docker-compose logs -f timemachine

# Rebuild after code changes
docker-compose up -d --build timemachine

# Stop all services
docker-compose down

# Stop and remove volumes
docker-compose down -v
```

**TimeMachine service configuration** (from docker-compose.yml):
- HTTP server: `0.0.0.0:9090` (exposed as `localhost:9090`)
- Config: `config/test.xml`
- Sensors: `ALL`
- Output: `stdout`
- Window: `15s`
- Step: `1s`
- Control timeout: `5s`

### Basic Development (Direct Go)

```bash
# Run in HTTP control mode (default quick start)
make run

# Run with custom flags
make run RUN_FLAGS="--http-addr :8080 --db sqlite://test.db --confile config/test.xml --slist ALL"

# Run directly with Go
go run ./cmd/timemachine --http-addr :8080 --db sqlite://test.db --confile config/test.xml --slist "Sensor?????_S"
```

### Testing

```bash
# Run all tests
go test ./...

# Run tests with coverage
make coverage

# Run specific package tests
go test ./internal/replay
go test ./internal/storage/sqlite -v

# Run Playwright E2E tests (requires Docker)
make js-tests
```

### Database Setup

```bash
# PostgreSQL (Docker)
make pg-up      # Start and seed PostgreSQL
make pg-down    # Stop PostgreSQL

# ClickHouse (Docker)
make ch-up      # Start and create schema
make ch-down    # Stop ClickHouse

# Generate test data
make gen-sensors GEN_SENSORS_START=10001 GEN_SENSORS_COUNT=50000
make gen-db GEN_DB_SENSORS=5000 GEN_DB_POINTS=300
```

### Benchmarking

```bash
# Run benchmark with default config
make bench

# Run with specific config
make bench CONFIG_YAML=config/config-sqlite.yaml

# Run with additional flags
make bench BENCH_FLAGS="--show-range --step 100ms"

# Cleanup benchmark artifacts
make clean-bench
```

### SharedMemory Testing

```bash
# Test connectivity to SharedMemory API
make check-sm SM_TEST_SENSOR=10001 SM_TEST_SUPPLIER=TestProc
```

## Code Architecture

### Core Data Flow

```
CLI/HTTP Server → Config → Storage → Replay Engine → Output Client
                              ↓
                    (Postgres/SQLite/ClickHouse)
                              ↓
                    (Event streaming with warmup)
                              ↓
                    (State reconstruction + batching)
                              ↓
                    (HTTP Client → SharedMemory / stdout)
```

### Key Components

**1. Entry Point (`cmd/timemachine/main.go`)**
- Parses CLI flags and YAML config (merged, CLI takes priority)
- Initializes storage backend based on `--db` DSN
- Creates two execution paths:
  - CLI mode: `service.Run()` for direct replay
  - HTTP mode: `runHTTPServer()` for API-based control

**2. Configuration (`pkg/config/`)**
- Supports 3 formats: UniSet XML, JSON, and YAML
- Maps sensor names ↔ IDs with bidirectional lookup
- Resolves selector patterns (glob, named sets, `ALL`)
- Handles XInclude for dynamic XML composition

**3. Storage Layer (`internal/storage/`)**
- **Interface**: `Warmup()`, `Stream()`, `Range()`
- **Warmup**: Loads last known value before start time for each sensor
- **Stream**: Returns events in time windows via channels
- **Implementations**:
  - `postgres`: Uses pgx connection pool with `make_interval()` microsecond precision
  - `sqlite`: Creates temp table `tm_sensors` to bypass parameter limits
  - `clickhouse`: Native driver with temp filter tables
  - `memstore`: In-memory deterministic test data

**4. Replay Engine (`internal/replay/`)**
- **State Management**: Maintains `sensorState` (value, hasValue, dirty flag) per sensor
- **Main Loop**:
  1. Apply warmup snapshot
  2. Stream events in windows
  3. For each step: apply events ≤ current time, collect dirty sensors, batch and send
  4. Wait for frame time (step/speed)
- **Control Commands**: pause/resume/stop/seek/step via control channel
- **Seeking/Backward**: Uses state cache (`stateCache`) to avoid full replay
- **State Builder**: `BuildState()` computes state at specific timestamp without sending

**5. Output Clients (`internal/sharedmem/`)**
- **Interface**: `Send(ctx, StepPayload) error`
- **HTTPClient**: Worker pool, retries, timeout handling for SharedMemory `/set`
- **StdoutClient**: Debug output to console
- **StepPayload**: Contains step_id, timestamp, batch info, sensor updates

**6. HTTP API & Manager (`internal/api/`)**
- **Manager**: Single-job state machine (idle→running→paused→done)
- **Session Control**: UUID tokens with timeout-based leases (`--control-timeout`)
- **Endpoints**:
  - `/api/v2/session` - get/refresh token, claim control
  - `/api/v2/sensors` - full sensor dictionary
  - `/api/v2/job/sensors` - working sensor list (mutable)
  - `/api/v2/job/range` - set replay parameters (pending)
  - `/api/v2/job/start` - start with pending range
  - `/api/v2/job/{pause,resume,stop,seek,apply,step/*}` - playback control
  - `/api/v2/ws/state` - WebSocket live state stream
  - `/ui/` - embedded web UI
- **WebSocket Protocol**: Messages with `type: snapshot|updates|reset`, includes control status metadata

**7. Web UI (`internal/api/ui/index.html`)**
- Real-time sensor table via WebSocket
- Timeline controls (play/pause/step/seek)
- Sensor selection and working list management
- Chart visualization (Chart.js/uPlot)
- Smoke/Flow test scenarios
- Control lock indicator (shows when other session has control)

### Critical Architecture Details

**Warmup Phase**:
- Before replay starts, load the last known value for each sensor before `from` time
- This ensures correct interpolation even if sensor hasn't changed in years
- Uses `DISTINCT ON (sensor_id)` in Postgres or window functions in SQLite

**Window-based Streaming**:
- History is loaded in configurable windows (default 1 minute via `--window`)
- Prevents memory exhaustion with large datasets (100k+ sensors)
- Events buffered ahead of current playback position

**State Cache for Seeking**:
- Backward seeks/steps use snapshot cache to avoid full replay
- Cache stores complete sensor state at strategic points
- On cache miss, rebuilds via `BuildState()` which replays from start

**Batching**:
- Large state updates split into batches (`--batch-size`, default 1024)
- Single logical step may result in multiple HTTP `/set` requests
- All batches share same `step_id` with sequential `batch_id`

**Session-based Control**:
- All management endpoints require `X-TM-Session` header
- Sessions expire after `--control-timeout` of inactivity
- Other clients must use `/session/claim` to take control
- Prevents concurrent control conflicts in multi-user scenarios

## Configuration Patterns

**YAML Configuration** (`--config-yaml`):
- Provides defaults for all CLI flags
- CLI flags override YAML values
- Structure mirrors flag names with dot notation:
  - `database.dsn` → `--db`
  - `sensors.selector` → `--slist`
  - `output.sm_url` → `--output`

**Sensor Selection** (`--slist`):
- `ALL` - all sensors from config
- `"pattern*"` - glob pattern (must quote in shell)
- `set_name` - named set from JSON config
- `sensor1,sensor2` - comma-separated list

**Database Connection**:
- PostgreSQL: `--db postgres://user:pass@host/db`
- SQLite: `--db sqlite://path/to/file.db` or `--db file:test.db`
- ClickHouse: `--db clickhouse://user:pass@host:9000/db --ch-table schema.table`

## Testing Guidelines

**Integration Tests**:
- Tests with `_test.go` suffix may require database setup
- Use `TM_POSTGRES_DSN` env var for Postgres tests
- SQLite tests use temporary in-memory databases

**E2E Tests**:
- Playwright tests in `docker-compose.yaml` (profile: `tests`)
- Tests web UI functionality and WebSocket streaming
- Run via `make js-tests`

## Common Development Patterns

**Adding a New Storage Backend**:
1. Implement `storage.Storage` interface in `internal/storage/yourdb/`
2. Add warmup query (last value before start time)
3. Implement window-based streaming with channel return
4. Add DSN parsing in `cmd/timemachine/main.go`
5. Add integration tests with Docker setup

**Adding API Endpoints**:
1. Add handler in `internal/api/http.go`
2. Update `Manager` in `internal/api/manager.go` if state changes needed
3. Ensure session validation for control endpoints
4. Update `DOCS.md` with endpoint documentation
5. Add test coverage in `internal/api/http_test.go`

**Modifying Replay Logic**:
- Core loop in `internal/replay/replay.go:Run()`
- Control commands handled in `processCommand()`
- State reconstruction in `state_builder.go:BuildState()`
- Always maintain backward compatibility with state cache format

## Important Constraints

**Database Schema Assumptions**:
- `main_history` table with columns: `sensor_id`, `timestamp`, `time_usec`, `value`
- Microsecond precision via separate `time_usec` field
- Sensors stored by ID (int64), names resolved via config

**Single-Job Limitation**:
- HTTP API supports only one active replay job at a time
- New start requests return 409 if job is active
- Use `/reset` to clear state before starting new job

**Memory Considerations**:
- Window size affects memory usage (default 1 minute is safe for 100k sensors)
- State cache grows with backward seeks (cleared on new range)
- WebSocket clients buffer updates (configurable via `--ws-batch-time`)

**Time Precision**:
- All timestamps are RFC3339 format in API
- Internal precision is microseconds (time.Time)
- SQLite stores Unix microseconds as INTEGER

## Project-Specific Notes

**Russian Documentation**:
- README.md, DOCS.md, and details.md are written in Russian
- Comments in code are primarily in Russian
- This reflects the project's origin in UniSet ecosystem

**UniSet Integration**:
- Designed to integrate with UniSet SCADA system
- SharedMemory is UniSet's state synchronization mechanism
- Sensor configuration follows UniSet XML schema conventions

**Development Philosophy**:
- Console-focused (no GUI in this branch)
- Only event changes stored in DB (not periodic snapshots)
- Interpolation for gaps between recorded events
- Robust handling of large sensor counts (100k+) and long time periods
