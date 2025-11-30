package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/pv/uniset-timemachine-go/internal/storage"
)

type Resolver interface {
	NameByID(id int64) (string, bool)
	IDByName(name string) (int64, bool)
}

type Config struct {
	DSN      string
	Table    string
	Resolver Resolver
}

type Store struct {
	conn     ch.Conn
	table    string
	resolver Resolver
}

const filterTable = "tm_sensors"

func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("clickhouse: DSN is empty")
	}
	if cfg.Resolver == nil {
		return nil, fmt.Errorf("clickhouse: resolver is nil")
	}

	// Нормализуем DSN: преобразуем наши схемы в стандартные для драйвера
	normalizedDSN := normalizeDSN(cfg.DSN)

	// Используем встроенный парсер драйвера для корректной обработки всех опций
	opts, err := ch.ParseDSN(normalizedDSN)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: parse DSN: %w", err)
	}

	conn, err := ch.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: open: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("clickhouse: ping: %w", err)
	}

	database := opts.Auth.Database
	if database == "" {
		database = "default"
	}
	table := cfg.Table
	if table == "" {
		table = "main_history"
	}
	if !strings.Contains(table, ".") {
		table = fmt.Sprintf("%s.%s", database, table)
	}
	return &Store{conn: conn, table: table, resolver: cfg.Resolver}, nil
}

func (s *Store) Close() {
	if s.conn != nil {
		s.conn.Close()
	}
}

func (s *Store) Warmup(ctx context.Context, sensors []int64, from time.Time) ([]storage.SensorEvent, error) {
	names, err := s.namesForSensors(sensors)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, nil
	}
	if err := s.refreshFilter(ctx, names); err != nil {
		return nil, err
	}

	query := fmt.Sprintf(warmupSQL, s.table, filterTable)
	rows, err := s.conn.Query(ctx, query, ch.Named("from", from))
	if err != nil {
		return nil, fmt.Errorf("clickhouse: warmup query: %w", err)
	}
	defer rows.Close()

	events := make([]storage.SensorEvent, 0, len(names))
	for rows.Next() {
		var name string
		var ts time.Time
		var value float64
		if err := rows.Scan(&name, &ts, &value); err != nil {
			return nil, fmt.Errorf("clickhouse: warmup scan: %w", err)
		}
		id, ok := s.resolver.IDByName(name)
		if !ok {
			continue
		}
		events = append(events, storage.SensorEvent{SensorID: id, Timestamp: ts, Value: value})
	}
	return events, rows.Err()
}

func (s *Store) Stream(ctx context.Context, req storage.StreamRequest) (<-chan []storage.SensorEvent, <-chan error) {
	dataCh := make(chan []storage.SensorEvent)
	errCh := make(chan error, 1)

	go func() {
		defer close(dataCh)
		defer close(errCh)

		names, err := s.namesForSensors(req.Sensors)
		if err != nil {
			errCh <- err
			return
		}
		if len(names) == 0 {
			errCh <- fmt.Errorf("clickhouse: sensors list is empty")
			return
		}
		if err := s.refreshFilter(ctx, names); err != nil {
			errCh <- err
			return
		}

		window := req.Window
		if window <= 0 {
			window = defaultWindow
		}

		cursor := req.From
		for cursor.Before(req.To) {
			next := cursor.Add(window)
			if next.After(req.To) {
				next = req.To
			}

			query := fmt.Sprintf(streamSQL, s.table, filterTable)
			rows, err := s.conn.Query(ctx, query, ch.Named("from", cursor), ch.Named("to", next))
			if err != nil {
				errCh <- fmt.Errorf("clickhouse: stream query: %w", err)
				return
			}
			batch := make([]storage.SensorEvent, 0, 256)
			for rows.Next() {
				var name string
				var ts time.Time
				var value float64
				if err := rows.Scan(&name, &ts, &value); err != nil {
					rows.Close()
					errCh <- fmt.Errorf("clickhouse: stream scan: %w", err)
					return
				}
				id, ok := s.resolver.IDByName(name)
				if !ok {
					continue
				}
				batch = append(batch, storage.SensorEvent{SensorID: id, Timestamp: ts, Value: value})
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				errCh <- fmt.Errorf("clickhouse: rows err: %w", err)
				return
			}
			if len(batch) > 0 {
				select {
				case <-ctx.Done():
					errCh <- ctx.Err()
					return
				case dataCh <- batch:
				}
			}
			if !next.After(cursor) {
				break
			}
			cursor = next
		}
	}()

	return dataCh, errCh
}

func (s *Store) Range(ctx context.Context, sensors []int64, from, to time.Time) (time.Time, time.Time, int64, error) {
	names, err := s.namesForSensors(sensors)
	if err != nil {
		return time.Time{}, time.Time{}, 0, err
	}
	if len(names) == 0 {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("clickhouse: sensors list is empty")
	}
	if err := s.refreshFilter(ctx, names); err != nil {
		return time.Time{}, time.Time{}, 0, err
	}

	query := fmt.Sprintf(`
SELECT min(timestamp) AS min_ts,
       max(timestamp) AS max_ts,
       count(DISTINCT name) AS sensor_count
FROM %s
WHERE name IN (SELECT name FROM %s)
`, s.table, filterTable)
	var args []any
	if !from.IsZero() {
		query += "  AND timestamp >= ?\n"
		args = append(args, from)
	}
	if !to.IsZero() {
		query += "  AND timestamp <= ?\n"
		args = append(args, to)
	}
	row := s.conn.QueryRow(ctx, query, args...)
	var minTs, maxTs time.Time
	var count uint64
	if err := row.Scan(&minTs, &maxTs, &count); err != nil {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("clickhouse: range scan: %w", err)
	}
	return minTs, maxTs, int64(count), nil
}

func (s *Store) namesForSensors(ids []int64) ([]string, error) {
	names := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		name, ok := s.resolver.NameByID(id)
		if !ok || name == "" {
			return nil, fmt.Errorf("clickhouse: name for sensor %d not found", id)
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names, nil
}

const defaultWindow = 5 * time.Second

func (s *Store) refreshFilter(ctx context.Context, names []string) error {
	if len(names) == 0 {
		return nil
	}
	if err := s.conn.Exec(ctx, fmt.Sprintf("CREATE TEMPORARY TABLE IF NOT EXISTS %s (name String)", filterTable)); err != nil {
		return fmt.Errorf("clickhouse: create filter table: %w", err)
	}
	if err := s.conn.Exec(ctx, fmt.Sprintf("TRUNCATE TABLE %s", filterTable)); err != nil {
		return fmt.Errorf("clickhouse: truncate filter table: %w", err)
	}
	batch, err := s.conn.PrepareBatch(ctx, fmt.Sprintf("INSERT INTO %s (name)", filterTable))
	if err != nil {
		return fmt.Errorf("clickhouse: prepare filter batch: %w", err)
	}
	for _, name := range names {
		if err := batch.Append(name); err != nil {
			return fmt.Errorf("clickhouse: append filter name: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("clickhouse: send filter batch: %w", err)
	}
	return nil
}

const warmupSQL = `
SELECT
    name,
    argMax(timestamp, timestamp) AS ts,
    argMax(value, timestamp) AS value
FROM %s
WHERE name IN (SELECT name FROM %s)
  AND timestamp <= @from
GROUP BY name;
`

const streamSQL = `
SELECT name, timestamp, value
FROM %s
WHERE name IN (SELECT name FROM %s)
  AND timestamp >= @from
  AND timestamp < @to
ORDER BY timestamp, name;
`

// IsSource возвращает true, если DSN указывает на ClickHouse.
// Поддерживается только native протокол (порт 9000):
// - clickhouse://host:9000/db
// - ch://host:9000/db
//
// HTTP протокол не поддерживается в текущей реализации, так как
// clickhouse-go v2 поддерживает HTTP только через database/sql интерфейс.
func IsSource(dsn string) bool {
	if dsn == "" {
		return false
	}
	lower := strings.ToLower(dsn)
	return strings.HasPrefix(lower, "clickhouse://") ||
		strings.HasPrefix(lower, "ch://")
}

// normalizeDSN преобразует пользовательские схемы в формат, понятный драйверу:
// - ch:// -> clickhouse:// (native protocol, port 9000)
func normalizeDSN(dsn string) string {
	lower := strings.ToLower(dsn)

	// ch:// -> clickhouse:// (драйвер понимает clickhouse://)
	if strings.HasPrefix(lower, "ch://") {
		return "clickhouse://" + dsn[len("ch://"):]
	}

	// clickhouse:// оставляем как есть
	return dsn
}
