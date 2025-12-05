package clickhouse

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/aviddiviner/go-murmur"
	"github.com/go-faster/city"

	"github.com/pv/uniset-timemachine-go/internal/storage"
)

// Resolver для совместимости со старым кодом (работа через name)
type Resolver interface {
	NameByHash(hash int64) (string, bool)
	HashByName(name string) (int64, bool)
}

type Config struct {
	DSN      string
	Table    string
	Resolver Resolver
}

// hashMode определяет режим работы с хешами в ClickHouse.
type hashMode int

const (
	hashModeName      hashMode = iota // fallback: работа через name (String)
	hashModeNameHID                   // работа через name_hid (CityHash64)
	hashModeUnisetHID                 // работа через uniset_hid (MurmurHash2)
)

type Store struct {
	conn     ch.Conn
	table    string
	resolver Resolver
	mode     hashMode // режим работы с хешами
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

	store := &Store{conn: conn, table: table, resolver: cfg.Resolver}

	// Определяем режим работы: сначала проверяем uniset_hid, затем name_hid, иначе name
	store.mode = store.detectHashMode(ctx)

	// Check server timezone
	store.checkTimezone(ctx)

	return store, nil
}

// checkTimezone checks the ClickHouse server timezone and logs a warning if not UTC.
func (s *Store) checkTimezone(ctx context.Context) {
	var tz string
	row := s.conn.QueryRow(ctx, "SELECT timezone()")
	if err := row.Scan(&tz); err != nil {
		log.Printf("clickhouse: WARNING: failed to check timezone: %v", err)
		return
	}
	if tz == "UTC" || tz == "Etc/UTC" {
		log.Printf("clickhouse: timezone is %s (OK)", tz)
		return
	}
	log.Printf("clickhouse: WARNING: server timezone is %q, expected UTC", tz)
	log.Printf("clickhouse: timestamps will be interpreted as UTC regardless of server timezone")
}

// detectHashMode определяет режим работы с хешами.
// Приоритет: uniset_hid (MurmurHash2) > name_hid (CityHash64) > name (String).
func (s *Store) detectHashMode(ctx context.Context) hashMode {
	parts := strings.SplitN(s.table, ".", 2)
	if len(parts) != 2 {
		return hashModeName
	}
	database, tableName := parts[0], parts[1]

	// Проверяем наличие колонки uniset_hid (предпочтительный режим для UniSet совместимости)
	query := `SELECT count() FROM system.columns WHERE database = ? AND table = ? AND name = 'uniset_hid'`
	row := s.conn.QueryRow(ctx, query, database, tableName)
	var count uint64
	if err := row.Scan(&count); err == nil && count > 0 {
		return hashModeUnisetHID
	}

	// Проверяем наличие колонки name_hid
	query = `SELECT count() FROM system.columns WHERE database = ? AND table = ? AND name = 'name_hid'`
	row = s.conn.QueryRow(ctx, query, database, tableName)
	if err := row.Scan(&count); err == nil && count > 0 {
		return hashModeNameHID
	}

	return hashModeName
}

func (s *Store) Close() {
	if s.conn != nil {
		s.conn.Close()
	}
}

func (s *Store) Warmup(ctx context.Context, sensors []int64, from time.Time) ([]storage.SensorEvent, error) {
	if len(sensors) == 0 {
		return nil, nil
	}

	if err := s.refreshFilter(ctx, sensors); err != nil {
		return nil, err
	}

	var query string
	switch s.mode {
	case hashModeUnisetHID:
		query = fmt.Sprintf(warmupSQLUnisetHID, s.table, filterTable)
	case hashModeNameHID:
		query = fmt.Sprintf(warmupSQLNameHID, s.table, filterTable)
	default:
		query = fmt.Sprintf(warmupSQLName, s.table, filterTable)
	}

	rows, err := s.conn.Query(ctx, query, ch.Named("from", from))
	if err != nil {
		return nil, fmt.Errorf("clickhouse: warmup query: %w", err)
	}
	defer rows.Close()

	events := make([]storage.SensorEvent, 0, len(sensors))
	for rows.Next() {
		var ts time.Time
		var value float64
		var hash int64

		switch s.mode {
		case hashModeUnisetHID:
			// Читаем uniset_hid и конвертируем обратно в CityHash64 через name
			var unisetHID uint32
			var name string
			if err := rows.Scan(&unisetHID, &name, &ts, &value); err != nil {
				return nil, fmt.Errorf("clickhouse: warmup scan: %w", err)
			}
			hash = int64(city.Hash64([]byte(name)))
		case hashModeNameHID:
			// Читаем name_hid напрямую
			if err := rows.Scan(&hash, &ts, &value); err != nil {
				return nil, fmt.Errorf("clickhouse: warmup scan: %w", err)
			}
		default:
			// Читаем name и конвертируем через cityhash64
			var name string
			if err := rows.Scan(&name, &ts, &value); err != nil {
				return nil, fmt.Errorf("clickhouse: warmup scan: %w", err)
			}
			hash = int64(city.Hash64([]byte(name)))
		}

		events = append(events, storage.SensorEvent{SensorID: hash, Timestamp: ts, Value: value})
	}
	return events, rows.Err()
}

func (s *Store) Stream(ctx context.Context, req storage.StreamRequest) (<-chan []storage.SensorEvent, <-chan error) {
	dataCh := make(chan []storage.SensorEvent)
	errCh := make(chan error, 1)

	go func() {
		defer close(dataCh)
		defer close(errCh)

		if len(req.Sensors) == 0 {
			errCh <- fmt.Errorf("clickhouse: sensors list is empty")
			return
		}
		if err := s.refreshFilter(ctx, req.Sensors); err != nil {
			errCh <- err
			return
		}

		window := req.Window
		if window <= 0 {
			window = defaultWindow
		}

		var query string
		switch s.mode {
		case hashModeUnisetHID:
			query = fmt.Sprintf(streamSQLUnisetHID, s.table, filterTable)
		case hashModeNameHID:
			query = fmt.Sprintf(streamSQLNameHID, s.table, filterTable)
		default:
			query = fmt.Sprintf(streamSQLName, s.table, filterTable)
		}

		cursor := req.From
		for cursor.Before(req.To) {
			next := cursor.Add(window)
			if next.After(req.To) {
				next = req.To
			}

			rows, err := s.conn.Query(ctx, query, ch.Named("from", cursor), ch.Named("to", next))
			if err != nil {
				errCh <- fmt.Errorf("clickhouse: stream query: %w", err)
				return
			}
			batch := make([]storage.SensorEvent, 0, 256)
			for rows.Next() {
				var ts time.Time
				var value float64
				var hash int64

				switch s.mode {
				case hashModeUnisetHID:
					var unisetHID uint32
					var name string
					if err := rows.Scan(&unisetHID, &name, &ts, &value); err != nil {
						rows.Close()
						errCh <- fmt.Errorf("clickhouse: stream scan: %w", err)
						return
					}
					hash = int64(city.Hash64([]byte(name)))
				case hashModeNameHID:
					if err := rows.Scan(&hash, &ts, &value); err != nil {
						rows.Close()
						errCh <- fmt.Errorf("clickhouse: stream scan: %w", err)
						return
					}
				default:
					var name string
					if err := rows.Scan(&name, &ts, &value); err != nil {
						rows.Close()
						errCh <- fmt.Errorf("clickhouse: stream scan: %w", err)
						return
					}
					hash = int64(city.Hash64([]byte(name)))
				}

				batch = append(batch, storage.SensorEvent{SensorID: hash, Timestamp: ts, Value: value})
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
	if len(sensors) == 0 {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("clickhouse: sensors list is empty")
	}
	if err := s.refreshFilter(ctx, sensors); err != nil {
		return time.Time{}, time.Time{}, 0, err
	}

	var query string
	switch s.mode {
	case hashModeUnisetHID:
		query = fmt.Sprintf(`
SELECT min(timestamp) AS min_ts,
       max(timestamp) AS max_ts,
       count(DISTINCT uniset_hid) AS sensor_count
FROM %s
WHERE uniset_hid IN (SELECT uniset_hid FROM %s)
`, s.table, filterTable)
	case hashModeNameHID:
		query = fmt.Sprintf(`
SELECT min(timestamp) AS min_ts,
       max(timestamp) AS max_ts,
       count(DISTINCT name_hid) AS sensor_count
FROM %s
WHERE name_hid IN (SELECT name_hid FROM %s)
`, s.table, filterTable)
	default:
		query = fmt.Sprintf(`
SELECT min(timestamp) AS min_ts,
       max(timestamp) AS max_ts,
       count(DISTINCT name) AS sensor_count
FROM %s
WHERE name IN (SELECT name FROM %s)
`, s.table, filterTable)
	}

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

// hashesToNames конвертирует hashes в names через resolver (для режима без name_hid).
func (s *Store) hashesToNames(hashes []int64) ([]string, error) {
	names := make([]string, 0, len(hashes))
	seen := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		name, ok := s.resolver.NameByHash(hash)
		if !ok || name == "" {
			return nil, fmt.Errorf("clickhouse: name for sensor hash %d not found", hash)
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

// refreshFilter заполняет временную таблицу для фильтрации по датчикам.
// В зависимости от режима использует uniset_hid (UInt32), name_hid (Int64), или name (String).
func (s *Store) refreshFilter(ctx context.Context, hashes []int64) error {
	if len(hashes) == 0 {
		return nil
	}

	switch s.mode {
	case hashModeUnisetHID:
		return s.refreshFilterUnisetHID(ctx, hashes)
	case hashModeNameHID:
		return s.refreshFilterNameHID(ctx, hashes)
	default:
		return s.refreshFilterName(ctx, hashes)
	}
}

// refreshFilterUnisetHID заполняет фильтр по uniset_hid (MurmurHash2 32-bit).
// Конвертирует CityHash64 hashes в MurmurHash2 через resolver.
func (s *Store) refreshFilterUnisetHID(ctx context.Context, hashes []int64) error {
	// Конвертируем hashes в uniset_hid через name
	names, err := s.hashesToNames(hashes)
	if err != nil {
		return err
	}

	unisetHIDs := make([]uint32, 0, len(names))
	for _, name := range names {
		unisetHIDs = append(unisetHIDs, murmur.MurmurHash2([]byte(name), 0))
	}

	if err := s.conn.Exec(ctx, fmt.Sprintf("CREATE TEMPORARY TABLE IF NOT EXISTS %s (uniset_hid UInt32)", filterTable)); err != nil {
		return fmt.Errorf("clickhouse: create filter table: %w", err)
	}
	if err := s.conn.Exec(ctx, fmt.Sprintf("TRUNCATE TABLE %s", filterTable)); err != nil {
		return fmt.Errorf("clickhouse: truncate filter table: %w", err)
	}
	batch, err := s.conn.PrepareBatch(ctx, fmt.Sprintf("INSERT INTO %s (uniset_hid)", filterTable))
	if err != nil {
		return fmt.Errorf("clickhouse: prepare filter batch: %w", err)
	}
	for _, hid := range unisetHIDs {
		if err := batch.Append(hid); err != nil {
			return fmt.Errorf("clickhouse: append filter uniset_hid: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("clickhouse: send filter batch: %w", err)
	}
	return nil
}

// refreshFilterNameHID заполняет фильтр по name_hid (Int64).
func (s *Store) refreshFilterNameHID(ctx context.Context, hashes []int64) error {
	if err := s.conn.Exec(ctx, fmt.Sprintf("CREATE TEMPORARY TABLE IF NOT EXISTS %s (name_hid Int64)", filterTable)); err != nil {
		return fmt.Errorf("clickhouse: create filter table: %w", err)
	}
	if err := s.conn.Exec(ctx, fmt.Sprintf("TRUNCATE TABLE %s", filterTable)); err != nil {
		return fmt.Errorf("clickhouse: truncate filter table: %w", err)
	}
	batch, err := s.conn.PrepareBatch(ctx, fmt.Sprintf("INSERT INTO %s (name_hid)", filterTable))
	if err != nil {
		return fmt.Errorf("clickhouse: prepare filter batch: %w", err)
	}
	for _, hash := range hashes {
		if err := batch.Append(hash); err != nil {
			return fmt.Errorf("clickhouse: append filter hash: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("clickhouse: send filter batch: %w", err)
	}
	return nil
}

// refreshFilterName заполняет фильтр по name (String) - fallback режим.
func (s *Store) refreshFilterName(ctx context.Context, hashes []int64) error {
	// Конвертируем hashes в names через resolver
	names, err := s.hashesToNames(hashes)
	if err != nil {
		return err
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

// SQL для режима uniset_hid (UInt32, MurmurHash2)
// Возвращаем uniset_hid и name для конвертации обратно в CityHash64
const warmupSQLUnisetHID = `
SELECT
    uniset_hid,
    name,
    argMax(timestamp, timestamp) AS ts,
    argMax(value, timestamp) AS value
FROM %s
WHERE uniset_hid IN (SELECT uniset_hid FROM %s)
  AND timestamp <= @from
GROUP BY uniset_hid, name;
`

const streamSQLUnisetHID = `
SELECT uniset_hid, name, timestamp, value
FROM %s
WHERE uniset_hid IN (SELECT uniset_hid FROM %s)
  AND timestamp >= @from
  AND timestamp < @to
ORDER BY timestamp, uniset_hid;
`

// SQL для режима name_hid (Int64)
const warmupSQLNameHID = `
SELECT
    name_hid,
    argMax(timestamp, timestamp) AS ts,
    argMax(value, timestamp) AS value
FROM %s
WHERE name_hid IN (SELECT name_hid FROM %s)
  AND timestamp <= @from
GROUP BY name_hid;
`

const streamSQLNameHID = `
SELECT name_hid, timestamp, value
FROM %s
WHERE name_hid IN (SELECT name_hid FROM %s)
  AND timestamp >= @from
  AND timestamp < @to
ORDER BY timestamp, name_hid;
`

// SQL для режима name (String) - fallback
const warmupSQLName = `
SELECT
    name,
    argMax(timestamp, timestamp) AS ts,
    argMax(value, timestamp) AS value
FROM %s
WHERE name IN (SELECT name FROM %s)
  AND timestamp <= @from
GROUP BY name;
`

const streamSQLName = `
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
