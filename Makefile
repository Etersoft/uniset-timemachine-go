TM_POSTGRES_DSN ?= postgres://admin:123@localhost:5432/uniset?sslmode=disable
TM_CLICKHOUSE_DSN ?= clickhouse://localhost:9000/default
CONFIG_YAML ?= config/config.yaml
SM_CONFIG_YAML ?= $(CONFIG_YAML)

.PHONY: help pg-up pg-down ch-up ch-down ch-tests ch-gen-data gen-sensors gen-db bench check-sm clean-bench run

help:
	@echo "Available targets:"
	@echo "  help        - this message"
	@echo "  pg-up       - start Postgres docker and seed it"
	@echo "  pg-down     - stop Postgres docker"
	@echo "  ch-up       - start ClickHouse docker and create schema"
	@echo "  ch-down     - stop ClickHouse docker"
	@echo "  ch-tests    - run ClickHouse integration tests (starts CH if needed)"
	@echo "  ch-gen-data - generate realistic CH data (see GEN_CH_*)"
	@echo "  gen-sensors - generate config/generated-sensors.xml (see GEN_SENSORS_*)"
	@echo "  gen-db      - generate SQLite dataset (see GEN_DB_*)"
	@echo "  gen-config-example - write config/config-example.yaml"
	@echo "  coverage    - run go test with coverage profile"
	@echo "  bench       - run timemachine bench using $(CONFIG_YAML) (override via BENCH_FLAGS)"
	@echo "  check-sm    - send test set/get to SharedMemory"
	@echo "  clean-bench - remove generated SQLite/ClickHouse artifacts"
	@echo "  run         - run timemachine in HTTP control mode (see RUN_FLAGS)"

pg-up:
	@docker compose up -d postgres
	@echo "Seeding Postgres at $(TM_POSTGRES_DSN)"
	@TM_POSTGRES_DSN=$(TM_POSTGRES_DSN) ./scripts/seed-postgres.sh
	@echo "Postgres ready ($(TM_POSTGRES_DSN))"

pg-down:
	@docker compose down -v

ch-up:
	@docker compose up -d clickhouse
	@echo "Waiting for ClickHouse..."
	@sleep 5

ch-down:
	@docker compose rm -sf clickhouse

ch-tests:
	@echo "Starting ClickHouse..."
	@docker compose up -d clickhouse
	@echo "Waiting for ClickHouse to be ready..."
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		docker compose exec -T clickhouse clickhouse-client --query='SELECT 1' >/dev/null 2>&1 && break || sleep 1; \
	done
	@echo "Running ClickHouse integration tests..."
	@TM_CLICKHOUSE_DSN=$(TM_CLICKHOUSE_DSN) go test ./internal/storage/clickhouse/... -v -timeout 60s

# ClickHouse data generation
GEN_CH_SENSORS ?= 0
GEN_CH_DURATION ?= 10m
GEN_CH_SELECTOR ?= ALL
GEN_CH_SQL_OUTPUT ?=

ch-gen-data:
	@echo "Generating ClickHouse data..."
	@go run ./cmd/gen-clickhouse-data \
		--db $(TM_CLICKHOUSE_DSN) \
		--table uniset.main_history \
		--confile config/test.xml \
		--selector $(GEN_CH_SELECTOR) \
		$(if $(filter-out 0,$(GEN_CH_SENSORS)),--sensors $(GEN_CH_SENSORS),) \
		--duration $(GEN_CH_DURATION) \
		--truncate \
		$(if $(GEN_CH_SQL_OUTPUT),--sql-output $(GEN_CH_SQL_OUTPUT),)

GEN_SENSORS_OUTPUT ?= config/generated-sensors.xml
GEN_SENSORS_START ?= 10001
GEN_SENSORS_COUNT ?= 0

gen-sensors:
	@echo "Generating sensors into $(GEN_SENSORS_OUTPUT)"
	@go run ./cmd/gen-sensors-xml --output $(GEN_SENSORS_OUTPUT) --start-id $(GEN_SENSORS_START) --count $(GEN_SENSORS_COUNT)

GEN_DB_PATH ?= sqlite-large.db
GEN_DB_SELECTOR ?= "Sensor1????_S"
GEN_DB_SENSORS ?= 5000
GEN_DB_POINTS ?= 300
GEN_DB_STEP ?= 200ms
GEN_DB_RANDOM ?= 50

gen-db:
	@echo "Generating sqlite data into $(GEN_DB_PATH)"
	@go run ./cmd/gen-sqlite-data --db $(GEN_DB_PATH) --confile config/test.xml --selector $(GEN_DB_SELECTOR) --sensors $(GEN_DB_SENSORS) --points $(GEN_DB_POINTS) --step $(GEN_DB_STEP) --random $(GEN_DB_RANDOM) --reset

gen-config-example:
	@./scripts/gen-config-example.sh

COVERAGE_PROFILE ?= coverage.out

coverage:
	@echo "Running Go coverage into $(COVERAGE_PROFILE)..."
	@GOCACHE=$(PWD)/.gocache go test -covermode=count -coverprofile=$(COVERAGE_PROFILE) ./...
	@go tool cover -func=$(COVERAGE_PROFILE) | tail -n 1

BENCH_FLAGS ?=

bench:
	@echo "Running timemachine bench with config $(CONFIG_YAML) $(if $(BENCH_FLAGS),and extra flags: $(BENCH_FLAGS),)"
	@go run ./cmd/timemachine --config-yaml $(CONFIG_YAML) $(BENCH_FLAGS)

SM_TEST_URL ?= http://localhost:9191/api/v01/SharedMemory
SM_TEST_SUPPLIER ?= TestProc
SM_TEST_SENSOR ?= 10001
SM_TEST_RANDOM ?= true
SM_EXTRA_FLAGS ?=

check-sm:
	@echo "Checking SM via $(SM_TEST_URL) (config: $(SM_CONFIG_YAML))"
	@go run ./cmd/sm-test $(if $(SM_CONFIG_YAML),--config-yaml $(SM_CONFIG_YAML),) --sm-url $(SM_TEST_URL) --supplier $(SM_TEST_SUPPLIER) --sensor-id $(SM_TEST_SENSOR) --random=$(SM_TEST_RANDOM) $(SM_EXTRA_FLAGS)

clean-bench:
	@rm -f sqlite-large.db config/generated-sensors.xml

RUN_FLAGS ?= --http-addr :9090 --db sqlite://test.db --confile config/test.xml --slist "Sensor?????_S" --from 2024-06-01T00:00:00Z --to 2024-06-01T00:00:10Z --step 1s --output stdout

run:
	@echo "Running timemachine with $(RUN_FLAGS)"
	@go run ./cmd/timemachine $(RUN_FLAGS)

js-tests:
	@echo "Running Playwright tests via docker-compose..."
	@docker compose --profile tests run --rm playwright
