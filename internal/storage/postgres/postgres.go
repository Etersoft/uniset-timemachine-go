package postgres

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pv/uniset-timemachine-go/internal/storage"
	"github.com/pv/uniset-timemachine-go/pkg/config"
)

const defaultWindow = time.Minute

type Config struct {
	ConnString string
	MaxConns   int32
	Registry   *config.SensorRegistry // реестр датчиков для конвертации hash↔configID
}

type Store struct {
	pool     *pgxpool.Pool
	registry *config.SensorRegistry
}

// RangeWithUnknown реализует UnknownAwareStorage: считает количество датчиков вне конфигурации
// в выбранном окне [from,to]. Unknown вычисляется как (все distinct sensor_id в окне) - (известные).
// Если словаря нет, считаем unknown=0.
func (s *Store) RangeWithUnknown(ctx context.Context, sensors []int64, from, to time.Time) (time.Time, time.Time, int64, int64, error) {
	minTs, maxTs, _, err := s.Range(ctx, sensors, from, to)
	if err != nil {
		return minTs, maxTs, 0, 0, err
	}

	// Конвертируем рабочий список в configID для подсчёта известных.
	configIDs, err := s.hashToConfigIDs(sensors)
	if err != nil {
		return minTs, maxTs, 0, 0, err
	}

	whereKnown := []string{"sensor_id = ANY($1)"}
	argsKnown := []any{sensorsAsArray(configIDs)}
	argPos := 2
	if !from.IsZero() {
		whereKnown = append(whereKnown,
			fmt.Sprintf("(date > $%d::date OR (date = $%d::date AND (time > $%d::time OR (time = $%d::time AND time_usec >= $%d))))",
				argPos, argPos, argPos+1, argPos+1, argPos+2))
		argsKnown = append(argsKnown, from.Format("2006-01-02"), from.Format("15:04:05"), from.Nanosecond()/1000)
		argPos += 3
	}
	if !to.IsZero() {
		whereKnown = append(whereKnown,
			fmt.Sprintf("(date < $%d::date OR (date = $%d::date AND (time < $%d::time OR (time = $%d::time AND time_usec <= $%d))))",
				argPos, argPos, argPos+1, argPos+1, argPos+2))
		argsKnown = append(argsKnown, to.Format("2006-01-02"), to.Format("15:04:05"), to.Nanosecond()/1000)
		argPos += 3
	}
	queryKnown := "SELECT COUNT(DISTINCT sensor_id) FROM main_history WHERE " + strings.Join(whereKnown, " AND ")
	var known int64
	if err := s.pool.QueryRow(ctx, queryKnown, argsKnown...).Scan(&known); err != nil {
		return minTs, maxTs, 0, 0, fmt.Errorf("postgres: count known sensors: %w", err)
	}

	// Unknown считаем только если есть словарь конфигурации.
	if s.registry == nil {
		return minTs, maxTs, known, 0, nil
	}

	// Считаем все distinct sensor_id в окне без фильтра по рабочему списку.
	whereAll := make([]string, 0, 2)
	argsAll := []any{}
	argPosAll := 1
	if !from.IsZero() {
		whereAll = append(whereAll,
			fmt.Sprintf("(date > $%d::date OR (date = $%d::date AND (time > $%d::time OR (time = $%d::time AND time_usec >= $%d))))",
				argPosAll, argPosAll, argPosAll+1, argPosAll+1, argPosAll+2))
		argsAll = append(argsAll, from.Format("2006-01-02"), from.Format("15:04:05"), from.Nanosecond()/1000)
		argPosAll += 3
	}
	if !to.IsZero() {
		whereAll = append(whereAll,
			fmt.Sprintf("(date < $%d::date OR (date = $%d::date AND (time < $%d::time OR (time = $%d::time AND time_usec <= $%d))))",
				argPosAll, argPosAll, argPosAll+1, argPosAll+1, argPosAll+2))
		argsAll = append(argsAll, to.Format("2006-01-02"), to.Format("15:04:05"), to.Nanosecond()/1000)
		argPosAll += 3
	}
	queryAll := "SELECT COUNT(DISTINCT sensor_id) FROM main_history"
	if len(whereAll) > 0 {
		queryAll += " WHERE " + strings.Join(whereAll, " AND ")
	}

	var totalDistinct int64
	if err := s.pool.QueryRow(ctx, queryAll, argsAll...).Scan(&totalDistinct); err != nil {
		return minTs, maxTs, known, 0, fmt.Errorf("postgres: count distinct sensors: %w", err)
	}
	unknown := totalDistinct - known
	if unknown < 0 {
		unknown = 0
	}
	return minTs, maxTs, known, unknown, nil
}

func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.ConnString == "" {
		return nil, fmt.Errorf("postgres: connection string is empty")
	}

	// PostgreSQL требует ID в конфиге для работы с таблицей main_history
	if cfg.Registry != nil && !cfg.Registry.HasIDs() {
		return nil, fmt.Errorf("postgres: config must have sensor IDs (idfromfile != 0 for all sensors)")
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

	// Check and set timezone to UTC
	if err := ensureUTCTimezone(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	// Проверяем возможность создавать временные таблицы (нужно для фильтрации сенсоров)
	if err := ensureTempTable(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}

	return &Store{
		pool:     pool,
		registry: cfg.Registry,
	}, nil
}

// ensureUTCTimezone checks the database timezone and sets session timezone to UTC if needed.
func ensureUTCTimezone(ctx context.Context, pool *pgxpool.Pool) error {
	var tz string
	if err := pool.QueryRow(ctx, "SHOW timezone").Scan(&tz); err != nil {
		return fmt.Errorf("postgres: failed to check timezone: %w", err)
	}
	if tz == "UTC" || tz == "Etc/UTC" {
		log.Printf("postgres: timezone is %s (OK)", tz)
		return nil
	}
	log.Printf("postgres: WARNING: database timezone is %q, expected UTC", tz)

	// Set session timezone to UTC for this connection pool
	// Note: This affects all connections in the pool via AfterConnect hook
	// For pgxpool we need to use BeforeAcquire or configure at connection string level
	// For simplicity, just log a warning - the data format used in queries is timezone-agnostic
	log.Printf("postgres: data will be interpreted as UTC regardless of server timezone")
	return nil
}

// ensureTempTable проверяет возможность создавать временные таблицы в текущей БД.
// Мы не используем эту таблицу в запросах, но проверяем права на случай, если
// в дальнейшем она понадобится (аналогично SQLite/ClickHouse фильтрам).
func ensureTempTable(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("postgres: acquire connection for temp table check: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `CREATE TEMP TABLE IF NOT EXISTS tm_sensors_tmp(sensor_id BIGINT) ON COMMIT DROP`); err != nil {
		return fmt.Errorf("postgres: create temp table: %w", err)
	}
	if _, err := conn.Exec(ctx, `TRUNCATE tm_sensors_tmp`); err != nil {
		return fmt.Errorf("postgres: truncate temp table: %w", err)
	}
	return nil
}

func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
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
			return nil, fmt.Errorf("postgres: sensor hash %d not found in registry", h)
		}
		if key.ID == nil {
			return nil, fmt.Errorf("postgres: sensor %q has no config ID", key.Name)
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
	if len(sensors) == 0 {
		return nil, nil
	}

	// Конвертируем hashes в configIDs для SQL запроса
	configIDs, err := s.hashToConfigIDs(sensors)
	if err != nil {
		return nil, err
	}

	fromDate := from.Format("2006-01-02")
	fromTime := from.Format("15:04:05")
	fromUsec := from.Nanosecond() / 1000

	rows, err := s.pool.Query(ctx, warmupSQL, sensorsAsArray(configIDs), fromDate, fromTime, fromUsec)
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
			SensorID:  s.configIDToHash(sensorID), // конвертируем в hash
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

		// Конвертируем hashes в configIDs для SQL запроса
		configIDs, err := s.hashToConfigIDs(req.Sensors)
		if err != nil {
			errCh <- err
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

			rows, err := s.pool.Query(ctx, windowSQL, sensorsAsArray(configIDs),
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
					SensorID:  s.configIDToHash(sensorID), // конвертируем в hash
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

	// Конвертируем hashes в configIDs для SQL запроса
	configIDs, err := s.hashToConfigIDs(sensors)
	if err != nil {
		return time.Time{}, time.Time{}, 0, err
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

	row := s.pool.QueryRow(ctx, rangeSQL, sensorsAsArray(configIDs), fromDate, toDate)
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
