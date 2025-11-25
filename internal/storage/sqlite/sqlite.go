package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/pv/uniset-timemachine-go/internal/storage"
)

const (
	filterTable      = "tm_sensors"
	defaultWindowDur = time.Minute
)

type Config struct {
	Source string
}

type Store struct {
	db *sql.DB
}

func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.Source == "" {
		return nil, fmt.Errorf("sqlite: database path is empty")
	}
	db, err := sql.Open("sqlite", cfg.Source)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: ping: %w", err)
	}
	store := &Store{db: db}
	if err := store.ensureFilterTable(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() {
	if s.db != nil {
		s.db.Close()
	}
}

func (s *Store) Warmup(ctx context.Context, sensors []int64, from time.Time) ([]storage.SensorEvent, error) {
	if err := s.resetFilter(ctx, sensors); err != nil {
		return nil, err
	}

	args := []any{from.UnixMicro()}
	rows, err := s.db.QueryContext(ctx, warmupSQL, args...)
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
			SensorID:  sensorID,
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

			rows, err := s.db.QueryContext(ctx, windowSQL, cursor.UnixMicro(), next.UnixMicro())
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
					SensorID:  sensorID,
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin tx: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`INSERT OR REPLACE INTO %s(sensor_id) VALUES (?)`, filterTable))
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("sqlite: prepare insert: %w", err)
	}
	for _, id := range sensors {
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
WITH ranked AS (
	SELECT sensor_id,
	       timestamp AS ts,
	       COALESCE(time_usec, 0) AS usec,
	       value,
	       ROW_NUMBER() OVER (
	           PARTITION BY sensor_id
	           ORDER BY timestamp DESC, COALESCE(time_usec, 0) DESC
	       ) AS rn
	FROM main_history
	WHERE sensor_id IN (SELECT sensor_id FROM ` + filterTable + `)
	  AND (strftime('%s', timestamp) * 1000000 + COALESCE(time_usec, 0)) <= ?
)
SELECT sensor_id, ts, usec, value
FROM ranked
WHERE rn = 1;
`

const windowSQL = `
SELECT sensor_id,
       timestamp,
       COALESCE(time_usec, 0) AS usec,
       value
FROM main_history
WHERE sensor_id IN (SELECT sensor_id FROM ` + filterTable + `)
  AND (strftime('%s', timestamp) * 1000000 + COALESCE(time_usec, 0)) >= ?
  AND (strftime('%s', timestamp) * 1000000 + COALESCE(time_usec, 0)) < ?
ORDER BY timestamp, usec, sensor_id;
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
