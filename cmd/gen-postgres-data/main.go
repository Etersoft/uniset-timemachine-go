package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pv/uniset-timemachine-go/pkg/config"
)

type options struct {
	dsn       string
	config    string
	selector  string
	sensors   int
	duration  time.Duration
	start     string
	node      int
	batchSize int
	sqlOutput string // если задано, пишем SQL в файл вместо подключения к PG
	truncate  bool
}

// sensorGenerator генерирует события для одного датчика
type sensorGenerator struct {
	id        int64
	name      string
	iotype    string
	rng       *rand.Rand
	value     float64
	lastSaved float64 // последнее сохранённое значение (для фильтрации)
	nextTime  time.Time
	startTime time.Time
	endTime   time.Time

	// для аналоговых: фаза стабильности
	phaseEnd time.Time
	baseVal  float64

	// параметры синусоиды (уникальные для каждого датчика)
	sinAmplitude float64 // амплитуда 2-10
	sinPeriod    float64 // период 30-120 сек
	sinPhase     float64 // начальная фаза 0-2π
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
			iotype = guessIOType(name)
		}
		sensors = append(sensors, sensorInfo{id: id, name: name, iotype: iotype})
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

	// Direct PG insert mode
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, opts.dsn)
	if err != nil {
		log.Fatalf("postgres connect: %v", err)
	}
	defer pool.Close()

	if opts.truncate {
		if _, err := pool.Exec(ctx, "TRUNCATE TABLE main_history"); err != nil {
			log.Fatalf("truncate table: %v", err)
		}
		log.Printf("truncated table main_history")
	}

	batchSize := opts.batchSize
	if batchSize <= 0 {
		batchSize = 10000
	}

	// Используем COPY для быстрой вставки
	totalRows := 0
	batchRows := make([][]interface{}, 0, batchSize)

	flushBatch := func() error {
		if len(batchRows) == 0 {
			return nil
		}
		_, err := pool.CopyFrom(
			ctx,
			pgx.Identifier{"main_history"},
			[]string{"date", "time", "time_usec", "sensor_id", "value", "node"},
			pgx.CopyFromRows(batchRows),
		)
		if err != nil {
			return fmt.Errorf("copy batch: %w", err)
		}
		batchRows = batchRows[:0]
		return nil
	}

	for i, s := range sensors {
		gen := newGenerator(s.id, s.name, s.iotype, startTs, endTs, i)
		for ev := gen.next(); ev != nil; ev = gen.next() {
			date := ev.ts.Format("2006-01-02")
			timeStr := ev.ts.Format("15:04:05")
			usec := ev.ts.Nanosecond() / 1000

			batchRows = append(batchRows, []interface{}{
				date,
				timeStr,
				usec,
				ev.sensorID,
				ev.value,
				opts.node,
			})
			totalRows++

			if len(batchRows) >= batchSize {
				if err := flushBatch(); err != nil {
					log.Fatalf("flush batch: %v", err)
				}
			}
		}
	}

	if err := flushBatch(); err != nil {
		log.Fatalf("flush batch: %v", err)
	}

	log.Printf("done: inserted %d rows into main_history", totalRows)
}

type event struct {
	ts       time.Time
	value    float64
	sensorID int64
}

func newGenerator(id int64, name, iotype string, start, end time.Time, sensorIndex int) *sensorGenerator {
	// Используем хеш от имени датчика для стабильного, но уникального seed
	seed := int64(sensorIndex)*1000003 + int64(len(name))*7919
	for i, c := range name {
		seed += int64(c) * int64(i+1) * 31
	}
	rng := rand.New(rand.NewSource(seed))
	gen := &sensorGenerator{
		id:        id,
		name:      name,
		iotype:    strings.ToUpper(iotype),
		rng:       rng,
		nextTime:  start,
		startTime: start,
		endTime:   end,
	}
	gen.init(sensorIndex)
	return gen
}

func (g *sensorGenerator) init(sensorIndex int) {
	if g.isDiscrete() {
		// дискретный: начинаем с 0 или 1
		g.value = float64(g.rng.Intn(2))
	} else {
		// аналоговый: базовое значение зависит от индекса датчика
		// чтобы каждый датчик имел свой характерный диапазон
		// Распределяем по диапазону 0-1000 с шагом ~50-150 на датчик
		baseOffset := float64(sensorIndex) * (50 + g.rng.Float64()*100)
		g.baseVal = baseOffset + g.rng.Float64()*30 // добавляем случайность
		g.value = g.baseVal
		// Смещаем начало фазы случайным образом, чтобы скачки не совпадали
		initialPhaseOffset := time.Duration(g.rng.Intn(40)) * time.Second
		g.phaseEnd = g.nextTime.Add(initialPhaseOffset + g.stablePhaseDuration())

		// Параметры синусоиды — уникальные для каждого датчика
		g.sinAmplitude = 2 + g.rng.Float64()*8      // амплитуда 2-10
		g.sinPeriod = 30 + g.rng.Float64()*90       // период 30-120 сек
		g.sinPhase = g.rng.Float64() * 2 * math.Pi  // начальная фаза 0-2π
	}
}

func (g *sensorGenerator) isDiscrete() bool {
	return g.iotype == "DI" || g.iotype == "DO"
}

func (g *sensorGenerator) next() *event {
	for g.nextTime.Before(g.endTime) {
		currentTime := g.nextTime
		currentValue := g.value

		if g.isDiscrete() {
			// переключение через 10-50 сек
			delay := time.Duration(10+g.rng.Intn(41)) * time.Second
			g.nextTime = g.nextTime.Add(delay)
			g.value = 1 - g.value // переключаем 0↔1
			// дискретные всегда сохраняем (это уже момент изменения)
			g.lastSaved = currentValue
			return &event{ts: currentTime, value: currentValue, sensorID: g.id}
		}

		// аналоговый: вычисляем значение каждую секунду
		g.nextTime = g.nextTime.Add(time.Second)

		// проверяем, нужен ли скачок (конец стабильной фазы)
		if g.nextTime.After(g.phaseEnd) {
			// скачок: меняем базу на ±10-30
			jump := (g.rng.Float64()*40 - 20) // -20..+20
			g.baseVal += jump
			// ограничиваем снизу (не уходим в минус)
			if g.baseVal < 0 {
				g.baseVal = g.rng.Float64() * 20
			}
			g.phaseEnd = g.nextTime.Add(g.stablePhaseDuration())
		}

		// Время от начала в секундах
		elapsed := currentTime.Sub(g.startTime).Seconds()

		// Синусоидальная составляющая
		sinValue := g.sinAmplitude * math.Sin(2*math.Pi*elapsed/g.sinPeriod+g.sinPhase)

		// Итоговое значение: база + синусоида + небольшой шум
		noise := g.rng.Float64()*2 - 1 // ±1
		g.value = g.baseVal + sinValue + noise

		// Сохраняем только при существенном изменении (> 0.5)
		if math.Abs(g.value-g.lastSaved) > 0.5 {
			g.lastSaved = g.value
			return &event{ts: currentTime, value: g.value, sensorID: g.id}
		}
		// иначе продолжаем цикл — ищем следующую точку с существенным изменением
	}
	return nil
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
	id     int64
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

	fmt.Fprintf(w, "-- Generated sensor data for PostgreSQL\n")
	fmt.Fprintf(w, "-- Sensors: %d, Duration: %v, Start: %s\n\n", len(sensors), opts.duration, opts.start)

	if opts.truncate {
		fmt.Fprintf(w, "TRUNCATE TABLE main_history;\n\n")
	}

	// Генерируем INSERT для каждого датчика пакетами
	const rowsPerInsert = 1000
	var rows []string

	flushRows := func() {
		if len(rows) == 0 {
			return
		}
		fmt.Fprintf(w, "INSERT INTO main_history (date, time, time_usec, sensor_id, value, node) VALUES\n")
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
		gen := newGenerator(s.id, s.name, s.iotype, start, end, i)
		for ev := gen.next(); ev != nil; ev = gen.next() {
			date := ev.ts.Format("2006-01-02")
			timeStr := ev.ts.Format("15:04:05")
			usec := ev.ts.Nanosecond() / 1000

			row := fmt.Sprintf("('%s', '%s', %d, %d, %.6f, %d)",
				date,
				timeStr,
				usec,
				ev.sensorID,
				ev.value,
				opts.node,
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
	flag.StringVar(&opt.dsn, "db", "postgres://admin:123@localhost:5432/uniset?sslmode=disable", "PostgreSQL DSN")
	flag.StringVar(&opt.config, "confile", "config/test.xml", "sensor config path")
	flag.StringVar(&opt.selector, "selector", "ALL", "sensor selector")
	flag.IntVar(&opt.sensors, "sensors", 0, "limit number of sensors (0 = all)")
	flag.DurationVar(&opt.duration, "duration", 10*time.Minute, "total time range to generate")
	// Default start: 7 days ago
	defaultStart := time.Now().UTC().AddDate(0, 0, -7).Truncate(24*time.Hour).Format(time.RFC3339)
	flag.StringVar(&opt.start, "start", defaultStart, "start timestamp (RFC3339)")
	flag.IntVar(&opt.node, "node", 0, "value for node column")
	flag.IntVar(&opt.batchSize, "batch", 10000, "rows per batch send (direct mode)")
	flag.StringVar(&opt.sqlOutput, "sql-output", "", "write SQL to file instead of inserting (e.g. data.sql)")
	flag.BoolVar(&opt.truncate, "truncate", false, "truncate table before insert")
	flag.Parse()
	return opt
}
