package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/pv/uniset-timemachine-go/pkg/config"
)

type options struct {
	dsn       string
	table     string
	config    string
	selector  string
	sensors   int
	duration  time.Duration
	start     string
	nodename  string
	producer  string
	batchSize int
	sqlOutput string // если задано, пишем SQL в файл вместо подключения к CH
	truncate  bool
}

// sensorGenerator генерирует события для одного датчика
type sensorGenerator struct {
	name     string
	iotype   string
	rng      *rand.Rand
	value    float64
	nextTime time.Time
	endTime  time.Time

	// для аналоговых: фаза стабильности
	phaseEnd time.Time
	baseVal  float64
}

func main() {
	opts := parseFlags()
	cfg, err := config.Load(opts.config)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ids, err := cfg.Resolve(opts.selector)
	if err != nil {
		log.Fatalf("resolve selector: %v", err)
	}
	if opts.sensors > 0 && len(ids) > opts.sensors {
		ids = ids[:opts.sensors]
	}

	sensors := make([]sensorInfo, 0, len(ids))
	for _, id := range ids {
		name, ok := cfg.NameByID(id)
		if !ok || name == "" {
			log.Fatalf("name not found for sensor %d", id)
		}
		meta := cfg.SensorMeta[name]
		iotype := meta.IOType
		if iotype == "" {
			// попробуем определить по имени
			iotype = guessIOType(name)
		}
		sensors = append(sensors, sensorInfo{name: name, iotype: iotype})
	}
	if len(sensors) == 0 {
		log.Fatalf("no sensors resolved for selector %s", opts.selector)
	}

	startTs, err := time.Parse(time.RFC3339, opts.start)
	if err != nil {
		log.Fatalf("invalid --start: %v", err)
	}
	endTs := startTs.Add(opts.duration)

	// SQL output mode
	if opts.sqlOutput != "" {
		if err := generateSQL(opts, sensors, startTs, endTs); err != nil {
			log.Fatalf("generate SQL: %v", err)
		}
		return
	}

	// Direct CH insert mode
	ctx := context.Background()
	conn, table, err := openClickhouse(ctx, opts.dsn, opts.table)
	if err != nil {
		log.Fatalf("clickhouse connect: %v", err)
	}
	defer conn.Close()

	if opts.truncate {
		if err := conn.Exec(ctx, fmt.Sprintf("TRUNCATE TABLE %s", table)); err != nil {
			log.Fatalf("truncate table: %v", err)
		}
		log.Printf("truncated table %s", table)
	}

	batchSize := opts.batchSize
	if batchSize <= 0 {
		batchSize = 10000
	}

	insertSQL := fmt.Sprintf("INSERT INTO %s (timestamp, value, name, nodename, producer)", table)
	batch, err := conn.PrepareBatch(ctx, insertSQL)
	if err != nil {
		log.Fatalf("prepare batch: %v", err)
	}
	rows := 0
	totalRows := 0

	for i, s := range sensors {
		gen := newGenerator(s.name, s.iotype, startTs, endTs, int64(i))
		for ev := gen.next(); ev != nil; ev = gen.next() {
			if err := batch.Append(ev.ts, ev.value, ev.name, opts.nodename, opts.producer); err != nil {
				log.Fatalf("append row: %v", err)
			}
			rows++
			totalRows++
			if rows >= batchSize {
				if err := batch.Send(); err != nil {
					log.Fatalf("send batch: %v", err)
				}
				batch, err = conn.PrepareBatch(ctx, insertSQL)
				if err != nil {
					log.Fatalf("prepare batch: %v", err)
				}
				rows = 0
			}
		}
	}
	if rows > 0 {
		if err := batch.Send(); err != nil {
			log.Fatalf("send batch: %v", err)
		}
	}
	log.Printf("done: inserted %d rows into %s", totalRows, table)
}

type event struct {
	ts    time.Time
	value float64
	name  string
}

func newGenerator(name, iotype string, start, end time.Time, seed int64) *sensorGenerator {
	rng := rand.New(rand.NewSource(time.Now().UnixNano() + seed*12345))
	gen := &sensorGenerator{
		name:     name,
		iotype:   strings.ToUpper(iotype),
		rng:      rng,
		nextTime: start,
		endTime:  end,
	}
	gen.init()
	return gen
}

func (g *sensorGenerator) init() {
	if g.isDiscrete() {
		// дискретный: начинаем с 0 или 1
		g.value = float64(g.rng.Intn(2))
	} else {
		// аналоговый: случайное начальное значение 0-100
		g.baseVal = g.rng.Float64() * 100
		g.value = g.baseVal
		g.phaseEnd = g.nextTime.Add(g.stablePhaseDuration())
	}
}

func (g *sensorGenerator) isDiscrete() bool {
	return g.iotype == "DI" || g.iotype == "DO"
}

func (g *sensorGenerator) next() *event {
	if !g.nextTime.Before(g.endTime) {
		return nil
	}

	ev := &event{
		ts:    g.nextTime,
		value: g.value,
		name:  g.name,
	}

	if g.isDiscrete() {
		// переключение через 10-50 сек
		delay := time.Duration(10+g.rng.Intn(41)) * time.Second
		g.nextTime = g.nextTime.Add(delay)
		g.value = 1 - g.value // переключаем 0↔1
	} else {
		// аналоговый: событие каждую секунду
		g.nextTime = g.nextTime.Add(time.Second)

		// проверяем, нужен ли скачок (конец стабильной фазы)
		if g.nextTime.After(g.phaseEnd) {
			// скачок: меняем базу на ±10-30
			jump := (g.rng.Float64()*40 - 20) // -20..+20
			g.baseVal += jump
			// ограничиваем диапазон 0-100
			if g.baseVal < 0 {
				g.baseVal = g.rng.Float64() * 20
			}
			if g.baseVal > 100 {
				g.baseVal = 80 + g.rng.Float64()*20
			}
			g.phaseEnd = g.nextTime.Add(g.stablePhaseDuration())
		}

		// плавное изменение в пределах фазы
		g.value = g.baseVal + (g.rng.Float64()*2 - 1) // ±1
	}

	return ev
}

func (g *sensorGenerator) stablePhaseDuration() time.Duration {
	// 40-60 секунд
	return time.Duration(40+g.rng.Intn(21)) * time.Second
}

func guessIOType(name string) string {
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, "DI") {
		return "DI"
	}
	if strings.HasPrefix(upper, "DO") {
		return "DO"
	}
	if strings.HasPrefix(upper, "AI") {
		return "AI"
	}
	if strings.HasPrefix(upper, "AO") {
		return "AO"
	}
	// по умолчанию аналоговый
	return "AI"
}

type sensorInfo struct {
	name   string
	iotype string
}

func generateSQL(opts options, sensors []sensorInfo, start, end time.Time) error {
	f, err := os.Create(opts.sqlOutput)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	defer w.Flush()

	fmt.Fprintf(w, "-- Generated sensor data for ClickHouse\n")
	fmt.Fprintf(w, "-- Sensors: %d, Duration: %v, Start: %s\n\n", len(sensors), opts.duration, opts.start)

	if opts.truncate {
		fmt.Fprintf(w, "TRUNCATE TABLE %s;\n\n", opts.table)
	}

	// Генерируем INSERT для каждого датчика пакетами
	const rowsPerInsert = 1000
	var rows []string

	flushRows := func() {
		if len(rows) == 0 {
			return
		}
		fmt.Fprintf(w, "INSERT INTO %s (timestamp, value, name, nodename, producer) VALUES\n", opts.table)
		for i, row := range rows {
			if i > 0 {
				fmt.Fprintf(w, ",\n")
			}
			fmt.Fprintf(w, "  %s", row)
		}
		fmt.Fprintf(w, ";\n\n")
		rows = rows[:0]
	}

	totalRows := 0
	for i, s := range sensors {
		gen := newGenerator(s.name, s.iotype, start, end, int64(i))
		for ev := gen.next(); ev != nil; ev = gen.next() {
			row := fmt.Sprintf("('%s', %.6f, '%s', '%s', '%s')",
				ev.ts.UTC().Format("2006-01-02 15:04:05.000000"),
				ev.value,
				ev.name,
				opts.nodename,
				opts.producer,
			)
			rows = append(rows, row)
			totalRows++
			if len(rows) >= rowsPerInsert {
				flushRows()
			}
		}
	}
	flushRows()

	log.Printf("done: wrote %d rows to %s", totalRows, opts.sqlOutput)
	return nil
}

func parseFlags() options {
	var opt options
	flag.StringVar(&opt.dsn, "db", "clickhouse://default:@localhost:9000/uniset", "ClickHouse DSN")
	flag.StringVar(&opt.table, "table", "uniset.main_history", "ClickHouse table (db.table)")
	flag.StringVar(&opt.config, "confile", "config/test.xml", "sensor config path")
	flag.StringVar(&opt.selector, "selector", "ALL", "sensor selector")
	flag.IntVar(&opt.sensors, "sensors", 0, "limit number of sensors (0 = all)")
	flag.DurationVar(&opt.duration, "duration", 10*time.Minute, "total time range to generate")
	flag.StringVar(&opt.start, "start", "2024-06-01T00:00:00Z", "start timestamp (RFC3339)")
	flag.StringVar(&opt.nodename, "nodename", "node1", "value for nodename column")
	flag.StringVar(&opt.producer, "producer", "gen-data", "value for producer column")
	flag.IntVar(&opt.batchSize, "batch", 10000, "rows per batch send (direct mode)")
	flag.StringVar(&opt.sqlOutput, "sql-output", "", "write SQL to file instead of inserting (e.g. data.sql)")
	flag.BoolVar(&opt.truncate, "truncate", false, "truncate table before insert")
	flag.Parse()
	return opt
}

func openClickhouse(ctx context.Context, dsn, table string) (ch.Conn, string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return nil, "", err
	}
	host := parsed.Host
	if host == "" {
		host = "localhost:9000"
	}
	if !strings.Contains(host, ":") {
		host = net.JoinHostPort(host, "9000")
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	if database == "" {
		database = "default"
	}
	username := parsed.User.Username()
	password, _ := parsed.User.Password()

	conn, err := ch.Open(&ch.Options{
		Addr: []string{host},
		Auth: ch.Auth{
			Database: database,
			Username: username,
			Password: password,
		},
	})
	if err != nil {
		return nil, "", err
	}
	if err := conn.Ping(ctx); err != nil {
		conn.Close()
		return nil, "", err
	}
	if table == "" {
		table = "main_history"
	}
	if !strings.Contains(table, ".") {
		table = fmt.Sprintf("%s.%s", database, table)
	}
	return conn, table, nil
}
