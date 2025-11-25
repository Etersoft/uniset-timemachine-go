package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pv/uniset-timemachine-go/internal/storage"
)

const defaultWindow = time.Minute

type Config struct {
	ConnString string
	MaxConns   int32
}

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.ConnString == "" {
		return nil, fmt.Errorf("postgres: connection string is empty")
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.ConnString)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse config: %w", err)
	}
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", err)
	}

	return &Store{
		pool: pool,
	}, nil
}

func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

func (s *Store) Warmup(ctx context.Context, sensors []int64, from time.Time) ([]storage.SensorEvent, error) {
	if len(sensors) == 0 {
		return nil, nil
	}

	rows, err := s.pool.Query(ctx, warmupSQL, sensorsAsArray(sensors), from)
	if err != nil {
		return nil, fmt.Errorf("postgres: warmup query: %w", err)
	}
	defer rows.Close()

	result := make([]storage.SensorEvent, 0, len(sensors))
	for rows.Next() {
		var sensorID int64
		var ts time.Time
		var usec int64
		var value float64
		if err := rows.Scan(&sensorID, &ts, &usec, &value); err != nil {
			return nil, fmt.Errorf("postgres: warmup scan: %w", err)
		}
		result = append(result, storage.SensorEvent{
			SensorID:  sensorID,
			Timestamp: combineTimestamp(ts, usec),
			Value:     value,
		})
	}
	return result, rows.Err()
}

func (s *Store) Stream(ctx context.Context, req storage.StreamRequest) (<-chan []storage.SensorEvent, <-chan error) {
	dataCh := make(chan []storage.SensorEvent)
	errCh := make(chan error, 1)

	go func() {
		defer close(dataCh)
		defer close(errCh)

		if len(req.Sensors) == 0 {
			errCh <- fmt.Errorf("postgres: stream sensors list is empty")
			return
		}

		window := req.Window
		if window <= 0 {
			window = defaultWindow
		}

		cursor := req.From
		for cursor.Before(req.To) {
			if err := ctx.Err(); err != nil {
				errCh <- err
				return
			}

			next := cursor.Add(window)
			if next.After(req.To) {
				next = req.To
			}

			rows, err := s.pool.Query(ctx, windowSQL, sensorsAsArray(req.Sensors), cursor, next)
			if err != nil {
				errCh <- fmt.Errorf("postgres: window query: %w", err)
				return
			}

			chunk := make([]storage.SensorEvent, 0)
			for rows.Next() {
				var sensorID int64
				var ts time.Time
				var usec int64
				var value float64
				if err := rows.Scan(&sensorID, &ts, &usec, &value); err != nil {
					rows.Close()
					errCh <- fmt.Errorf("postgres: window scan: %w", err)
					return
				}
				chunk = append(chunk, storage.SensorEvent{
					SensorID:  sensorID,
					Timestamp: combineTimestamp(ts, usec),
					Value:     value,
				})
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				errCh <- fmt.Errorf("postgres: rows err: %w", err)
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

			if next == cursor {
				break
			}
			cursor = next
		}
	}()

	return dataCh, errCh
}

func sensorsAsArray(ids []int64) any {
	return ids
}

func combineTimestamp(ts time.Time, usec int64) time.Time {
	return ts.Add(time.Duration(usec) * time.Microsecond)
}

func (s *Store) Range(ctx context.Context, sensors []int64, from, to time.Time) (time.Time, time.Time, int64, error) {
	if len(sensors) == 0 {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("postgres: sensors list is empty")
	}
	row := s.pool.QueryRow(ctx, rangeSQL, sensorsAsArray(sensors), from, to)
	var minTs, maxTs time.Time
	var minUsec, maxUsec int64
	var count int64
	if err := row.Scan(&minTs, &minUsec, &maxTs, &maxUsec, &count); err != nil {
		if err == pgx.ErrNoRows {
			return time.Time{}, time.Time{}, 0, nil
		}
		return time.Time{}, time.Time{}, 0, fmt.Errorf("postgres: range scan: %w", err)
	}
	return combineTimestamp(minTs, minUsec), combineTimestamp(maxTs, maxUsec), count, nil
}

const warmupSQL = `
SELECT DISTINCT ON (sensor_id)
	sensor_id,
	timestamp,
	COALESCE(time_usec, 0) AS time_usec,
	value
FROM main_history
WHERE sensor_id = ANY($1)
  AND (timestamp + COALESCE(time_usec, 0) * INTERVAL '1 microsecond') <= $2
ORDER BY sensor_id, timestamp DESC, time_usec DESC;
`

const windowSQL = `
SELECT sensor_id,
       timestamp,
       COALESCE(time_usec, 0) AS time_usec,
       value
FROM main_history
WHERE sensor_id = ANY($1)
  AND (timestamp + COALESCE(time_usec, 0) * INTERVAL '1 microsecond') >= $2
  AND (timestamp + COALESCE(time_usec, 0) * INTERVAL '1 microsecond') < $3
ORDER BY timestamp, time_usec, sensor_id;
`

const rangeSQL = `
WITH filtered AS (
	SELECT timestamp,
	       COALESCE(time_usec, 0) AS usec
	FROM main_history
	WHERE sensor_id = ANY($1)
	  AND ($2::timestamptz IS NULL OR timestamp >= $2)
	  AND ($3::timestamptz IS NULL OR timestamp <= $3)
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
	(SELECT usec FROM max_row) AS max_usec,
	(SELECT COUNT(DISTINCT sensor_id) FROM filtered) AS sensor_count;
`

func IsPostgresURL(db string) bool {
	return strings.HasPrefix(db, "postgres://") || strings.HasPrefix(db, "postgresql://")
}
