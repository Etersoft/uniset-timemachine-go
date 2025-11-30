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

	fromDate := from.Format("2006-01-02")
	fromTime := from.Format("15:04:05")
	fromUsec := from.Nanosecond() / 1000

	rows, err := s.pool.Query(ctx, warmupSQL, sensorsAsArray(sensors), fromDate, fromTime, fromUsec)
	if err != nil {
		return nil, fmt.Errorf("postgres: warmup query: %w", err)
	}
	defer rows.Close()

	result := make([]storage.SensorEvent, 0, len(sensors))
	for rows.Next() {
		var sensorID int64
		var date time.Time
		var timeStr string
		var usec int
		var value float64
		if err := rows.Scan(&sensorID, &date, &timeStr, &usec, &value); err != nil {
			return nil, fmt.Errorf("postgres: warmup scan: %w", err)
		}
		result = append(result, storage.SensorEvent{
			SensorID:  sensorID,
			Timestamp: combineDateTimeUsec(date, timeStr, usec),
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

			cursorDate := cursor.Format("2006-01-02")
			cursorTime := cursor.Format("15:04:05")
			cursorUsec := cursor.Nanosecond() / 1000
			nextDate := next.Format("2006-01-02")
			nextTime := next.Format("15:04:05")
			nextUsec := next.Nanosecond() / 1000

			rows, err := s.pool.Query(ctx, windowSQL, sensorsAsArray(req.Sensors),
				cursorDate, cursorTime, cursorUsec,
				nextDate, nextTime, nextUsec)
			if err != nil {
				errCh <- fmt.Errorf("postgres: window query: %w", err)
				return
			}

			chunk := make([]storage.SensorEvent, 0)
			for rows.Next() {
				var sensorID int64
				var date time.Time
				var timeStr string
				var usec int
				var value float64
				if err := rows.Scan(&sensorID, &date, &timeStr, &usec, &value); err != nil {
					rows.Close()
					errCh <- fmt.Errorf("postgres: window scan: %w", err)
					return
				}
				chunk = append(chunk, storage.SensorEvent{
					SensorID:  sensorID,
					Timestamp: combineDateTimeUsec(date, timeStr, usec),
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

func combineDateTimeUsec(date time.Time, timeStr string, usec int) time.Time {
	// date уже содержит дату, timeStr содержит время в формате HH:MM:SS
	// Парсим время и комбинируем с датой
	t, err := time.Parse("15:04:05", timeStr)
	if err != nil {
		return date
	}
	return time.Date(
		date.Year(), date.Month(), date.Day(),
		t.Hour(), t.Minute(), t.Second(),
		usec*1000, // microseconds to nanoseconds
		time.UTC,
	)
}

func (s *Store) Range(ctx context.Context, sensors []int64, from, to time.Time) (time.Time, time.Time, int64, error) {
	if len(sensors) == 0 {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("postgres: sensors list is empty")
	}

	var fromDate, toDate *string

	if !from.IsZero() {
		fd := from.Format("2006-01-02")
		fromDate = &fd
	}
	if !to.IsZero() {
		td := to.Format("2006-01-02")
		toDate = &td
	}

	row := s.pool.QueryRow(ctx, rangeSQL, sensorsAsArray(sensors), fromDate, toDate)
	var minDate, maxDate *time.Time
	var minTime, maxTime *string
	var minUsec, maxUsec *int
	var count int64
	if err := row.Scan(&minDate, &minTime, &minUsec, &maxDate, &maxTime, &maxUsec, &count); err != nil {
		if err == pgx.ErrNoRows {
			return time.Time{}, time.Time{}, 0, nil
		}
		return time.Time{}, time.Time{}, 0, fmt.Errorf("postgres: range scan: %w", err)
	}

	var minTs, maxTs time.Time
	if minDate != nil && minTime != nil && minUsec != nil {
		minTs = combineDateTimeUsec(*minDate, *minTime, *minUsec)
	}
	if maxDate != nil && maxTime != nil && maxUsec != nil {
		maxTs = combineDateTimeUsec(*maxDate, *maxTime, *maxUsec)
	}

	return minTs, maxTs, count, nil
}

const warmupSQL = `
SELECT DISTINCT ON (sensor_id)
	sensor_id,
	date,
	time::text,
	time_usec,
	value
FROM main_history
WHERE sensor_id = ANY($1)
  AND (date < $2::date OR (date = $2::date AND (time < $3::time OR (time = $3::time AND time_usec <= $4))))
ORDER BY sensor_id, date DESC, time DESC, time_usec DESC;
`

const windowSQL = `
SELECT sensor_id,
       date,
       time::text,
       time_usec,
       value
FROM main_history
WHERE sensor_id = ANY($1)
  AND (date > $2::date OR (date = $2::date AND (time > $3::time OR (time = $3::time AND time_usec >= $4))))
  AND (date < $5::date OR (date = $5::date AND (time < $6::time OR (time = $6::time AND time_usec < $7))))
ORDER BY date, time, time_usec, sensor_id;
`

const rangeSQL = `
WITH filtered AS (
	SELECT date, time, time_usec
	FROM main_history
	WHERE sensor_id = ANY($1)
	  AND ($2::text IS NULL OR date >= $2::date)
	  AND ($3::text IS NULL OR date <= $3::date)
),
min_row AS (
	SELECT date, time::text AS time, time_usec
	FROM filtered
	ORDER BY date, time, time_usec
	LIMIT 1
),
max_row AS (
	SELECT date, time::text AS time, time_usec
	FROM filtered
	ORDER BY date DESC, time DESC, time_usec DESC
	LIMIT 1
)
SELECT
	(SELECT date FROM min_row) AS min_date,
	(SELECT time FROM min_row) AS min_time,
	(SELECT time_usec FROM min_row) AS min_usec,
	(SELECT date FROM max_row) AS max_date,
	(SELECT time FROM max_row) AS max_time,
	(SELECT time_usec FROM max_row) AS max_usec,
	(SELECT COUNT(*) FROM filtered) AS row_count;
`

func IsPostgresURL(db string) bool {
	return strings.HasPrefix(db, "postgres://") || strings.HasPrefix(db, "postgresql://")
}
