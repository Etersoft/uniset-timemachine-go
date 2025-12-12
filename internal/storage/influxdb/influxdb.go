package influxdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	client "github.com/influxdata/influxdb1-client/v2"

	"github.com/pv/uniset-timemachine-go/internal/storage"
)

// Resolver для работы с именами датчиков (аналогично clickhouse).
type Resolver interface {
	NameByHash(hash int64) (string, bool)
	HashByName(name string) (int64, bool)
}

// Config содержит параметры подключения к InfluxDB.
type Config struct {
	DSN      string   // influxdb://user:pass@host:8086/database
	Resolver Resolver // resolver для преобразования hash <-> name
}

// Store реализует интерфейс storage.Storage для InfluxDB 1.x.
type Store struct {
	client   client.Client
	database string
	resolver Resolver
}

const defaultWindow = 5 * time.Second

// New создает новое подключение к InfluxDB.
func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("influxdb: DSN is empty")
	}
	if cfg.Resolver == nil {
		return nil, fmt.Errorf("influxdb: resolver is nil")
	}

	addr, database, username, password, err := parseDSN(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("influxdb: parse DSN: %w", err)
	}

	c, err := client.NewHTTPClient(client.HTTPConfig{
		Addr:     addr,
		Username: username,
		Password: password,
		Timeout:  30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("influxdb: create client: %w", err)
	}

	// Проверяем подключение
	_, _, err = c.Ping(10 * time.Second)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("influxdb: ping: %w", err)
	}

	log.Printf("influxdb: connected to %s, database=%s", addr, database)

	return &Store{
		client:   c,
		database: database,
		resolver: cfg.Resolver,
	}, nil
}

// Close закрывает соединение с InfluxDB.
func (s *Store) Close() {
	if s.client != nil {
		s.client.Close()
	}
}

// Warmup возвращает последнее известное значение каждого датчика перед from.
func (s *Store) Warmup(ctx context.Context, sensors []int64, from time.Time) ([]storage.SensorEvent, error) {
	if len(sensors) == 0 {
		return nil, nil
	}

	names, hashByName, err := s.resolveNames(sensors)
	if err != nil {
		return nil, err
	}

	events := make([]storage.SensorEvent, 0, len(sensors))

	// Для каждого датчика получаем последнее значение до from
	// InfluxDB не поддерживает эффективные multi-measurement запросы,
	// поэтому делаем запросы порциями
	for _, name := range names {
		query := fmt.Sprintf(
			`SELECT LAST(value) FROM "%s" WHERE time <= '%s'`,
			escapeIdentifier(name),
			from.UTC().Format(time.RFC3339Nano),
		)

		resp, err := s.client.Query(client.Query{
			Command:  query,
			Database: s.database,
		})
		if err != nil {
			return nil, fmt.Errorf("influxdb: warmup query for %s: %w", name, err)
		}
		if resp.Error() != nil {
			return nil, fmt.Errorf("influxdb: warmup query for %s: %w", name, resp.Error())
		}

		// Парсим результат
		for _, result := range resp.Results {
			for _, series := range result.Series {
				if len(series.Values) == 0 {
					continue
				}
				row := series.Values[0]
				if len(row) < 2 {
					continue
				}

				ts, value, err := parseRow(row)
				if err != nil {
					log.Printf("influxdb: warmup parse error for %s: %v", name, err)
					continue
				}

				hash := hashByName[series.Name]
				events = append(events, storage.SensorEvent{
					SensorID:  hash,
					Timestamp: ts,
					Value:     value,
				})
			}
		}
	}

	return events, nil
}

// Stream возвращает канал с событиями в указанном диапазоне.
func (s *Store) Stream(ctx context.Context, req storage.StreamRequest) (<-chan []storage.SensorEvent, <-chan error) {
	dataCh := make(chan []storage.SensorEvent)
	errCh := make(chan error, 1)

	go func() {
		defer close(dataCh)
		defer close(errCh)

		if len(req.Sensors) == 0 {
			errCh <- fmt.Errorf("influxdb: sensors list is empty")
			return
		}

		names, hashByName, err := s.resolveNames(req.Sensors)
		if err != nil {
			errCh <- err
			return
		}

		window := req.Window
		if window <= 0 {
			window = defaultWindow
		}

		// Формируем regex pattern для запроса нескольких measurements
		// Для большого количества датчиков это эффективнее отдельных запросов
		pattern := buildRegexPattern(names)

		cursor := req.From
		for cursor.Before(req.To) {
			next := cursor.Add(window)
			if next.After(req.To) {
				next = req.To
			}

			query := fmt.Sprintf(
				`SELECT value FROM %s WHERE time >= '%s' AND time < '%s' ORDER BY time`,
				pattern,
				cursor.UTC().Format(time.RFC3339Nano),
				next.UTC().Format(time.RFC3339Nano),
			)

			resp, err := s.client.Query(client.Query{
				Command:  query,
				Database: s.database,
			})
			if err != nil {
				errCh <- fmt.Errorf("influxdb: stream query: %w", err)
				return
			}
			if resp.Error() != nil {
				errCh <- fmt.Errorf("influxdb: stream query: %w", resp.Error())
				return
			}

			// Собираем все события из результата
			var allEvents []storage.SensorEvent
			for _, result := range resp.Results {
				for _, series := range result.Series {
					hash, ok := hashByName[series.Name]
					if !ok {
						continue
					}

					for _, row := range series.Values {
						ts, value, err := parseRow(row)
						if err != nil {
							continue
						}
						allEvents = append(allEvents, storage.SensorEvent{
							SensorID:  hash,
							Timestamp: ts,
							Value:     value,
						})
					}
				}
			}

			// Сортируем события по времени (InfluxDB возвращает отсортированными внутри series,
			// но нам нужна сортировка по всем датчикам)
			sortEventsByTime(allEvents)

			if len(allEvents) > 0 {
				select {
				case <-ctx.Done():
					errCh <- ctx.Err()
					return
				case dataCh <- allEvents:
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

// Range возвращает минимальный и максимальный timestamp для выбранных датчиков.
func (s *Store) Range(ctx context.Context, sensors []int64, from, to time.Time) (time.Time, time.Time, int64, error) {
	if len(sensors) == 0 {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("influxdb: sensors list is empty")
	}

	names, _, err := s.resolveNames(sensors)
	if err != nil {
		return time.Time{}, time.Time{}, 0, err
	}

	pattern := buildRegexPattern(names)

	// Строим запрос с опциональными условиями по времени
	whereClause := ""
	if !from.IsZero() || !to.IsZero() {
		conditions := []string{}
		if !from.IsZero() {
			conditions = append(conditions, fmt.Sprintf("time >= '%s'", from.UTC().Format(time.RFC3339Nano)))
		}
		if !to.IsZero() {
			conditions = append(conditions, fmt.Sprintf("time <= '%s'", to.UTC().Format(time.RFC3339Nano)))
		}
		whereClause = " WHERE " + strings.Join(conditions, " AND ")
	}

	// Получаем минимальное время
	queryMin := fmt.Sprintf(`SELECT FIRST(value) FROM %s%s`, pattern, whereClause)
	respMin, err := s.client.Query(client.Query{
		Command:  queryMin,
		Database: s.database,
	})
	if err != nil {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("influxdb: range min query: %w", err)
	}
	if respMin.Error() != nil {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("influxdb: range min query: %w", respMin.Error())
	}

	// Получаем максимальное время
	queryMax := fmt.Sprintf(`SELECT LAST(value) FROM %s%s`, pattern, whereClause)
	respMax, err := s.client.Query(client.Query{
		Command:  queryMax,
		Database: s.database,
	})
	if err != nil {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("influxdb: range max query: %w", err)
	}
	if respMax.Error() != nil {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("influxdb: range max query: %w", respMax.Error())
	}

	var minTs, maxTs time.Time
	sensorsWithData := int64(0)

	// Парсим минимальное время
	for _, result := range respMin.Results {
		for _, series := range result.Series {
			if len(series.Values) > 0 && len(series.Values[0]) > 0 {
				if ts, _, err := parseRow(series.Values[0]); err == nil {
					sensorsWithData++
					if minTs.IsZero() || ts.Before(minTs) {
						minTs = ts
					}
				}
			}
		}
	}

	// Парсим максимальное время
	for _, result := range respMax.Results {
		for _, series := range result.Series {
			if len(series.Values) > 0 && len(series.Values[0]) > 0 {
				if ts, _, err := parseRow(series.Values[0]); err == nil {
					if maxTs.IsZero() || ts.After(maxTs) {
						maxTs = ts
					}
				}
			}
		}
	}

	return minTs, maxTs, sensorsWithData, nil
}

// RangeWithUnknown реализует UnknownAwareStorage.
// Для InfluxDB не считаем unknown отдельно, возвращаем 0.
func (s *Store) RangeWithUnknown(ctx context.Context, sensors []int64, from, to time.Time) (time.Time, time.Time, int64, int64, error) {
	min, max, count, err := s.Range(ctx, sensors, from, to)
	return min, max, count, 0, err
}

// resolveNames преобразует список хешей в имена measurements.
func (s *Store) resolveNames(hashes []int64) ([]string, map[string]int64, error) {
	names := make([]string, 0, len(hashes))
	hashByName := make(map[string]int64, len(hashes))
	seen := make(map[string]struct{}, len(hashes))

	for _, hash := range hashes {
		name, ok := s.resolver.NameByHash(hash)
		if !ok || name == "" {
			return nil, nil, fmt.Errorf("influxdb: name for sensor hash %d not found", hash)
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
		hashByName[name] = hash
	}
	return names, hashByName, nil
}

// buildRegexPattern создает InfluxQL regex pattern для списка measurements.
func buildRegexPattern(names []string) string {
	if len(names) == 1 {
		return fmt.Sprintf(`"%s"`, escapeIdentifier(names[0]))
	}

	// Для нескольких measurements используем regex
	escaped := make([]string, len(names))
	for i, name := range names {
		escaped[i] = escapeRegex(name)
	}
	return fmt.Sprintf(`/^(%s)$/`, strings.Join(escaped, "|"))
}

// escapeIdentifier экранирует имя для использования в двойных кавычках.
func escapeIdentifier(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

// escapeRegex экранирует спецсимволы для regex.
func escapeRegex(s string) string {
	replacer := strings.NewReplacer(
		".", `\.`,
		"*", `\*`,
		"+", `\+`,
		"?", `\?`,
		"^", `\^`,
		"$", `\$`,
		"(", `\(`,
		")", `\)`,
		"[", `\[`,
		"]", `\]`,
		"{", `\{`,
		"}", `\}`,
		"|", `\|`,
		"\\", `\\`,
	)
	return replacer.Replace(s)
}

// parseRow парсит строку результата InfluxDB.
func parseRow(row []interface{}) (time.Time, float64, error) {
	if len(row) < 2 {
		return time.Time{}, 0, fmt.Errorf("row too short")
	}

	// Первый элемент - время (RFC3339 string или json.Number)
	var ts time.Time
	switch v := row[0].(type) {
	case string:
		var err error
		ts, err = time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return time.Time{}, 0, fmt.Errorf("parse time: %w", err)
		}
	case float64:
		// Наносекунды как число
		ts = time.Unix(0, int64(v))
	default:
		return time.Time{}, 0, fmt.Errorf("unexpected time type: %T", row[0])
	}

	// Второй элемент - значение
	var value float64
	switch v := row[1].(type) {
	case float64:
		value = v
	case int64:
		value = float64(v)
	case int:
		value = float64(v)
	case json.Number:
		var err error
		value, err = v.Float64()
		if err != nil {
			return time.Time{}, 0, fmt.Errorf("parse json.Number value: %w", err)
		}
	default:
		return time.Time{}, 0, fmt.Errorf("unexpected value type: %T", row[1])
	}

	return ts, value, nil
}

// sortEventsByTime сортирует события по времени.
func sortEventsByTime(events []storage.SensorEvent) {
	// Простая сортировка пузырьком для небольших объемов
	// Для больших объемов можно использовать sort.Slice
	for i := 0; i < len(events)-1; i++ {
		for j := i + 1; j < len(events); j++ {
			if events[j].Timestamp.Before(events[i].Timestamp) {
				events[i], events[j] = events[j], events[i]
			}
		}
	}
}

// IsSource проверяет, является ли DSN InfluxDB-источником.
func IsSource(dsn string) bool {
	if dsn == "" {
		return false
	}
	lower := strings.ToLower(dsn)
	return strings.HasPrefix(lower, "influxdb://") ||
		strings.HasPrefix(lower, "influx://")
}

// parseDSN разбирает DSN в компоненты.
// Формат: influxdb://user:pass@host:8086/database
func parseDSN(dsn string) (addr, database, username, password string, err error) {
	// Нормализуем схему
	normalized := dsn
	if strings.HasPrefix(strings.ToLower(dsn), "influx://") {
		normalized = "influxdb://" + dsn[len("influx://"):]
	}

	u, err := url.Parse(normalized)
	if err != nil {
		return "", "", "", "", fmt.Errorf("invalid URL: %w", err)
	}

	// Адрес сервера
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "8086"
	}
	addr = fmt.Sprintf("http://%s:%s", host, port)

	// База данных из пути
	database = strings.TrimPrefix(u.Path, "/")
	if database == "" {
		return "", "", "", "", fmt.Errorf("database not specified in DSN")
	}

	// Аутентификация
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}

	return addr, database, username, password, nil
}
