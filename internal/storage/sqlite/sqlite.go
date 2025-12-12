package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/pv/uniset-timemachine-go/internal/storage"
	"github.com/pv/uniset-timemachine-go/pkg/config"
)

const (
	filterTable      = "tm_sensors"
	defaultWindowDur = time.Minute
)

type Config struct {
	Source   string
	Pragmas  Pragmas
	Registry *config.SensorRegistry // реестр датчиков для конвертации hash↔configID
}

// Pragmas настраивают кеш и режимы SQLite.
type Pragmas struct {
	CacheMB    int  // cache_size в мегабайтах (<=0 чтобы пропустить)
	WAL        bool // journal_mode=WAL
	SyncOff    bool // synchronous=OFF
	TempMemory bool // temp_store=MEMORY
}

type Store struct {
	db         *sql.DB
	stmtWarmup *sql.Stmt
	stmtWindow *sql.Stmt
	registry   *config.SensorRegistry
}

// RangeWithUnknown реализует UnknownAwareStorage: дополнительно считает неизвестные датчики в окне.
func (s *Store) RangeWithUnknown(ctx context.Context, sensors []int64, from, to time.Time) (time.Time, time.Time, int64, int64, error) {
	minTs, maxTs, count, err := s.Range(ctx, sensors, from, to)
	if err != nil {
		return minTs, maxTs, count, 0, err
	}
	// Если нет реестра, считаем, что неизвестных нет (legacy режим).
	if s.registry == nil {
		return minTs, maxTs, count, 0, nil
	}
	// Считаем общее число уникальных sensor_id в окне без фильтра и сравниваем с числом известных
	// (по рабочему списку). Если в истории есть sensor_id, отсутствующие в конфиге, они попадут
	// в unknown (all - known).
	args := []interface{}{}
	var where string
	if !from.IsZero() {
		args = append(args, from.Format(time.RFC3339Nano))
		where += " AND (strftime('%s', timestamp) * 1000000 + COALESCE(time_usec, 0)) >= strftime('%s', ?) * 1000000"
	}
	if !to.IsZero() {
		args = append(args, to.Format(time.RFC3339Nano))
		where += " AND (strftime('%s', timestamp) * 1000000 + COALESCE(time_usec, 0)) <= strftime('%s', ?) * 1000000"
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(DISTINCT sensor_id) FROM main_history WHERE 1=1 %s`, where), args...).Scan(&total); err != nil {
		return minTs, maxTs, count, 0, fmt.Errorf("sqlite: unknown sensors count: %w", err)
	}
	unknown := total - count
	if unknown < 0 {
		unknown = 0
	}
	return minTs, maxTs, count, unknown, nil
}

func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.Source == "" {
		return nil, fmt.Errorf("sqlite: database path is empty")
	}

	// SQLite требует ID в конфиге для работы с таблицей main_history
	if cfg.Registry != nil && !cfg.Registry.HasIDs() {
		return nil, fmt.Errorf("sqlite: config must have sensor IDs (idfromfile != 0 for all sensors)")
	}

	db, err := sql.Open("sqlite", cfg.Source)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: ping: %w", err)
	}
	if err := applyPragmas(ctx, db, cfg.Pragmas); err != nil {
		db.Close()
		return nil, err
	}
	store := &Store{db: db, registry: cfg.Registry}
	if err := store.ensureFilterTable(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.ensureIndexes(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.prepareStatements(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() {
	if s.stmtWarmup != nil {
		s.stmtWarmup.Close()
	}
	if s.stmtWindow != nil {
		s.stmtWindow.Close()
	}
	if s.db != nil {
		s.db.Close()
	}
}

// hashToConfigIDs конвертирует hashes в configIDs для SQL запросов.
func (s *Store) hashToConfigIDs(hashes []int64) ([]int64, error) {
	if s.registry == nil {
		return hashes, nil // legacy mode - hashes уже являются configIDs
	}
	result := make([]int64, 0, len(hashes))
	for _, h := range hashes {
		key, ok := s.registry.ByHash(h)
		if !ok {
			return nil, fmt.Errorf("sqlite: sensor hash %d not found in registry", h)
		}
		if key.ID == nil {
			return nil, fmt.Errorf("sqlite: sensor %q has no config ID", key.Name)
		}
		result = append(result, *key.ID)
	}
	return result, nil
}

// configIDToHash конвертирует configID из результата SQL в hash.
func (s *Store) configIDToHash(configID int64) int64 {
	if s.registry == nil {
		return configID // legacy mode
	}
	if key, ok := s.registry.ByConfigID(configID); ok {
		return key.Hash
	}
	return configID // fallback
}

func (s *Store) Warmup(ctx context.Context, sensors []int64, from time.Time) ([]storage.SensorEvent, error) {
	// resetFilter уже конвертирует hashes в configIDs
	if err := s.resetFilter(ctx, sensors); err != nil {
		return nil, err
	}

	args := []any{from.UnixMicro()}
	rows, err := s.stmtWarmup.QueryContext(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: warmup query: %w", err)
	}
	defer rows.Close()

	var events []storage.SensorEvent
	for rows.Next() {
		var sensorID int64
		var ts string
		var usec sql.NullInt64
		var value float64
		if err := rows.Scan(&sensorID, &ts, &usec, &value); err != nil {
			return nil, fmt.Errorf("sqlite: warmup scan: %w", err)
		}
		parsed, err := parseTimestamp(ts, usec.Int64)
		if err != nil {
			return nil, err
		}
		events = append(events, storage.SensorEvent{
			SensorID:  s.configIDToHash(sensorID), // конвертируем в hash
			Timestamp: parsed,
			Value:     value,
		})
	}
	return events, rows.Err()
}

func (s *Store) Stream(ctx context.Context, req storage.StreamRequest) (<-chan []storage.SensorEvent, <-chan error) {
	dataCh := make(chan []storage.SensorEvent)
	errCh := make(chan error, 1)

	go func() {
		defer close(dataCh)
		defer close(errCh)

		// resetFilter уже конвертирует hashes в configIDs
		if err := s.resetFilter(ctx, req.Sensors); err != nil {
			errCh <- err
			return
		}

		window := req.Window
		if window <= 0 {
			window = defaultWindowDur
		}

		cursor := req.From
		for cursor.Before(req.To) {
			next := cursor.Add(window)
			if next.After(req.To) {
				next = req.To
			}

			rows, err := s.stmtWindow.QueryContext(ctx, cursor.UnixMicro(), next.UnixMicro())
			if err != nil {
				errCh <- fmt.Errorf("sqlite: window query: %w", err)
				return
			}

			chunk := make([]storage.SensorEvent, 0, 128)
			for rows.Next() {
				var sensorID int64
				var ts string
				var usec sql.NullInt64
				var value float64
				if err := rows.Scan(&sensorID, &ts, &usec, &value); err != nil {
					rows.Close()
					errCh <- fmt.Errorf("sqlite: window scan: %w", err)
					return
				}
				parsed, err := parseTimestamp(ts, usec.Int64)
				if err != nil {
					rows.Close()
					errCh <- err
					return
				}
				chunk = append(chunk, storage.SensorEvent{
					SensorID:  s.configIDToHash(sensorID), // конвертируем в hash
					Timestamp: parsed,
					Value:     value,
				})
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				errCh <- fmt.Errorf("sqlite: rows err: %w", err)
				return
			}

			if len(chunk) > 0 {
				select {
				case <-ctx.Done():
					errCh <- ctx.Err()
					return
				case dataCh <- chunk:
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

func (s *Store) ensureFilterTable(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`CREATE TEMP TABLE IF NOT EXISTS %s(sensor_id INTEGER PRIMARY KEY)`, filterTable))
	if err != nil {
		return fmt.Errorf("sqlite: init filter table: %w", err)
	}
	return nil
}

func (s *Store) ensureIndexes(ctx context.Context) error {
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_main_history_sensor_ts ON main_history(sensor_id, timestamp, time_usec)`,
	}
	for _, q := range indexes {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("sqlite: create index: %w", err)
		}
	}
	return nil
}

func (s *Store) prepareStatements(ctx context.Context) error {
	var err error
	s.stmtWarmup, err = s.db.PrepareContext(ctx, warmupSQL)
	if err != nil {
		return fmt.Errorf("sqlite: prepare warmup: %w", err)
	}
	s.stmtWindow, err = s.db.PrepareContext(ctx, windowSQL)
	if err != nil {
		return fmt.Errorf("sqlite: prepare window: %w", err)
	}
	return nil
}

func (s *Store) resetFilter(ctx context.Context, sensors []int64) error {
	if err := s.ensureFilterTable(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s`, filterTable)); err != nil {
		return fmt.Errorf("sqlite: failed to clear filter: %w", err)
	}
	if len(sensors) == 0 {
		return nil
	}

	// Конвертируем hashes в configIDs для SQL запросов
	configIDs, err := s.hashToConfigIDs(sensors)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin tx: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`INSERT OR REPLACE INTO %s(sensor_id) VALUES (?)`, filterTable))
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("sqlite: prepare insert: %w", err)
	}
	for _, id := range configIDs {
		if _, err := stmt.ExecContext(ctx, id); err != nil {
			stmt.Close()
			tx.Rollback()
			return fmt.Errorf("sqlite: insert sensor %d: %w", id, err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit filter tx: %w", err)
	}
	return nil
}

func applyPragmas(ctx context.Context, db *sql.DB, p Pragmas) error {
	var pragmas []string
	if p.WAL {
		pragmas = append(pragmas, `PRAGMA journal_mode=WAL`)
	}
	if p.SyncOff {
		pragmas = append(pragmas, `PRAGMA synchronous=OFF`)
	}
	if p.TempMemory {
		pragmas = append(pragmas, `PRAGMA temp_store=MEMORY`)
	}
	if p.CacheMB > 0 {
		// отрицательное значение — задаёт размер в килобайтах
		cacheKB := -p.CacheMB * 1024
		pragmas = append(pragmas, fmt.Sprintf(`PRAGMA cache_size=%d`, cacheKB))
	}
	for _, p := range pragmas {
		if _, err := db.ExecContext(ctx, p); err != nil {
			log.Printf("sqlite: pragma failed (%s): %v", p, err)
			// не фейлим init, просто логируем
		}
	}
	return nil
}

func parseTimestamp(raw string, usec int64) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
	}
	var parsed time.Time
	var err error
	for _, layout := range layouts {
		parsed, err = time.Parse(layout, strings.TrimSpace(raw))
		if err == nil {
			return parsed.Add(time.Duration(usec) * time.Microsecond), nil
		}
	}
	return time.Time{}, fmt.Errorf("sqlite: unknown timestamp format %q: %v", raw, err)
}

const warmupSQL = `
WITH base AS (
	SELECT sensor_id,
	       timestamp AS ts,
	       COALESCE(time_usec, 0) AS usec,
	       (strftime('%s', timestamp) * 1000000 + COALESCE(time_usec, 0)) AS ts_micro,
	       value
	FROM main_history
	WHERE sensor_id IN (SELECT sensor_id FROM ` + filterTable + `)
),
ranked AS (
	SELECT sensor_id,
	       ts,
	       usec,
	       value,
	       ROW_NUMBER() OVER (
	           PARTITION BY sensor_id
	           ORDER BY ts_micro DESC
	       ) AS rn
	FROM base
	WHERE ts_micro <= ?
)
SELECT sensor_id, ts, usec, value
FROM ranked
WHERE rn = 1;
`

const windowSQL = `
WITH base AS (
	SELECT sensor_id,
	       timestamp,
	       COALESCE(time_usec, 0) AS usec,
	       (strftime('%s', timestamp) * 1000000 + COALESCE(time_usec, 0)) AS ts_micro,
	       value
	FROM main_history
	WHERE sensor_id IN (SELECT sensor_id FROM ` + filterTable + `)
)
SELECT sensor_id,
       timestamp,
       usec,
       value
FROM base
WHERE ts_micro >= ?
  AND ts_micro < ?
ORDER BY ts_micro, sensor_id;
`

func (s *Store) Range(ctx context.Context, sensors []int64, from, to time.Time) (time.Time, time.Time, int64, error) {
	if err := s.resetFilter(ctx, sensors); err != nil {
		return time.Time{}, time.Time{}, 0, err
	}
	args := []interface{}{}
	var where string
	if !from.IsZero() {
		args = append(args, from.Format(time.RFC3339Nano))
		where += " AND (strftime('%s', timestamp) * 1000000 + COALESCE(time_usec, 0)) >= strftime('%s', ?) * 1000000"
	}
	if !to.IsZero() {
		args = append(args, to.Format(time.RFC3339Nano))
		where += " AND (strftime('%s', timestamp) * 1000000 + COALESCE(time_usec, 0)) <= strftime('%s', ?) * 1000000"
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(rangeSQL, where), args...)
	var minTs, maxTs sql.NullString
	var minUsec, maxUsec sql.NullInt64
	if err := row.Scan(&minTs, &minUsec, &maxTs, &maxUsec); err != nil {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("sqlite: range scan: %w", err)
	}
	if !minTs.Valid || !maxTs.Valid {
		return time.Time{}, time.Time{}, 0, nil
	}
	var count int64
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(countSQL, where), args...).Scan(&count); err != nil {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("sqlite: sensor count: %w", err)
	}
	minTime, err := parseTimestamp(minTs.String, minUsec.Int64)
	if err != nil {
		return time.Time{}, time.Time{}, 0, err
	}
	maxTime, err := parseTimestamp(maxTs.String, maxUsec.Int64)
	if err != nil {
		return time.Time{}, time.Time{}, 0, err
	}
	return minTime, maxTime, count, nil
}

const rangeSQL = `
WITH filtered AS (
	SELECT timestamp,
	       COALESCE(time_usec, 0) AS usec
	FROM main_history
	WHERE sensor_id IN (SELECT sensor_id FROM ` + filterTable + `)
	%s
),
min_row AS (
	SELECT timestamp, usec
	FROM filtered
	ORDER BY timestamp, usec
	LIMIT 1
),
max_row AS (
	SELECT timestamp, usec
	FROM filtered
	ORDER BY timestamp DESC, usec DESC
	LIMIT 1
)
SELECT
	(SELECT timestamp FROM min_row) AS min_ts,
	(SELECT usec FROM min_row) AS min_usec,
	(SELECT timestamp FROM max_row) AS max_ts,
	(SELECT usec FROM max_row) AS max_usec;
`

const countSQL = `
SELECT COUNT(DISTINCT sensor_id) FROM main_history WHERE sensor_id IN (SELECT sensor_id FROM ` + filterTable + `) %s;
`

func IsSource(src string) bool {
	if src == "" {
		return false
	}
	lower := strings.ToLower(src)
	switch {
	case strings.HasPrefix(lower, "sqlite://"),
		strings.HasPrefix(lower, "file:"),
		strings.HasSuffix(lower, ".db"),
		src == ":memory:":
		return true
	default:
		return false
	}
}

func NormalizeSource(src string) string {
	if strings.HasPrefix(src, "sqlite://") {
		return strings.TrimPrefix(src, "sqlite://")
	}
	return src
}
