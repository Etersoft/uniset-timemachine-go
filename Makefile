TM_POSTGRES_DSN ?= postgres://admin:123@localhost:5432/uniset?sslmode=disable
CONFIG_YAML ?= config/config.yaml
SM_CONFIG_YAML ?= $(CONFIG_YAML)

.PHONY: help pg-up pg-down ch-up ch-down gen-sensors gen-db bench check-sm clean-bench

help:
	@echo "Available targets:"
	@echo "  help        - this message"
	@echo "  pg-up       - start Postgres docker and seed it"
	@echo "  pg-down     - stop Postgres docker"
	@echo "  ch-up       - start ClickHouse docker and create schema"
	@echo "  ch-down     - stop ClickHouse docker"
	@echo "  gen-sensors - generate config/generated-sensors.xml (see GEN_SENSORS_*)"
	@echo "  gen-db      - generate SQLite dataset (see GEN_DB_*)"
	@echo "  bench       - run timemachine bench using $(CONFIG_YAML) (override via BENCH_FLAGS)"
	@echo "  check-sm    - send test set/get to SharedMemory"
	@echo "  clean-bench - remove generated SQLite/ClickHouse artifacts"

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
