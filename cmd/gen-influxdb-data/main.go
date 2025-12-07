package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pv/uniset-timemachine-go/pkg/config"
)

type options struct {
	influxURL string
	database  string
	config    string
	selector  string
	sensors   int
	duration  time.Duration
	start     string
	lpOutput  string // если задано, пишем Line Protocol в файл
	drop      bool   // drop measurements перед вставкой
}

// sensorGenerator генерирует события для одного датчика
type sensorGenerator struct {
	name      string
	iotype    string
	rng       *rand.Rand
	value     float64
	lastSaved float64
	nextTime  time.Time
	startTime time.Time
	endTime   time.Time

	phaseEnd time.Time
	baseVal  float64

	sinAmplitude float64
	sinPeriod    float64
	sinPhase     float64
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
		// Пробуем сначала по hash (для датчиков с idfromfile="0"), затем по ID
		name, ok := cfg.NameByHash(id)
		if !ok || name == "" {
			name, ok = cfg.NameByID(id)
		}
		if !ok || name == "" {
			log.Fatalf("name not found for sensor %d", id)
		}
		meta := cfg.SensorMeta[name]
		iotype := meta.IOType
		if iotype == "" {
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

	// Line Protocol output mode
	if opts.lpOutput != "" {
		if err := generateLineProtocol(opts, sensors, startTs, endTs); err != nil {
			log.Fatalf("generate Line Protocol: %v", err)
		}
		return
	}

	// Direct InfluxDB insert mode
	if opts.drop {
		if err := dropMeasurements(opts, sensors); err != nil {
			log.Fatalf("drop measurements: %v", err)
		}
	}

	if err := insertData(opts, sensors, startTs, endTs); err != nil {
		log.Fatalf("insert data: %v", err)
	}
}

type event struct {
	ts    time.Time
	value float64
	name  string
}

func newGenerator(name, iotype string, start, end time.Time, sensorIndex int) *sensorGenerator {
	seed := int64(sensorIndex)*1000003 + int64(len(name))*7919
	for i, c := range name {
		seed += int64(c) * int64(i+1) * 31
	}
	rng := rand.New(rand.NewSource(seed))
	gen := &sensorGenerator{
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
		g.value = float64(g.rng.Intn(2))
	} else {
		baseOffset := float64(sensorIndex) * (50 + g.rng.Float64()*100)
		g.baseVal = baseOffset + g.rng.Float64()*30
		g.value = g.baseVal
		initialPhaseOffset := time.Duration(g.rng.Intn(40)) * time.Second
		g.phaseEnd = g.nextTime.Add(initialPhaseOffset + g.stablePhaseDuration())

		g.sinAmplitude = 2 + g.rng.Float64()*8
		g.sinPeriod = 30 + g.rng.Float64()*90
		g.sinPhase = g.rng.Float64() * 2 * math.Pi
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
			delay := time.Duration(10+g.rng.Intn(41)) * time.Second
			g.nextTime = g.nextTime.Add(delay)
			g.value = 1 - g.value
			g.lastSaved = currentValue
			return &event{ts: currentTime, value: currentValue, name: g.name}
		}

		g.nextTime = g.nextTime.Add(time.Second)

		if g.nextTime.After(g.phaseEnd) {
			jump := (g.rng.Float64()*40 - 20)
			g.baseVal += jump
			if g.baseVal < 0 {
				g.baseVal = g.rng.Float64() * 20
			}
			g.phaseEnd = g.nextTime.Add(g.stablePhaseDuration())
		}

		elapsed := currentTime.Sub(g.startTime).Seconds()
		sinValue := g.sinAmplitude * math.Sin(2*math.Pi*elapsed/g.sinPeriod+g.sinPhase)
		noise := g.rng.Float64()*2 - 1
		g.value = g.baseVal + sinValue + noise

		if math.Abs(g.value-g.lastSaved) > 0.5 {
			g.lastSaved = g.value
			return &event{ts: currentTime, value: g.value, name: g.name}
		}
	}
	return nil
}

func (g *sensorGenerator) stablePhaseDuration() time.Duration {
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
	return "AI"
}

type sensorInfo struct {
	name   string
	iotype string
}

// toLineProtocol преобразует событие в InfluxDB Line Protocol
// Format: <measurement> value=<value> <timestamp_ns>
func toLineProtocol(ev *event) string {
	return fmt.Sprintf("%s value=%.6f %d", ev.name, ev.value, ev.ts.UnixNano())
}

func generateLineProtocol(opts options, sensors []sensorInfo, start, end time.Time) error {
	f, err := os.Create(opts.lpOutput)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	defer w.Flush()

	fmt.Fprintf(w, "# Generated sensor data for InfluxDB (Line Protocol)\n")
	fmt.Fprintf(w, "# Sensors: %d, Duration: %v, Start: %s\n", len(sensors), opts.duration, opts.start)
	fmt.Fprintf(w, "# Database: %s\n\n", opts.database)

	totalRows := 0
	for i, s := range sensors {
		gen := newGenerator(s.name, s.iotype, start, end, i)
		for ev := gen.next(); ev != nil; ev = gen.next() {
			fmt.Fprintln(w, toLineProtocol(ev))
			totalRows++
		}
	}

	log.Printf("done: wrote %d rows to %s", totalRows, opts.lpOutput)
	return nil
}

func dropMeasurements(opts options, sensors []sensorInfo) error {
	log.Printf("dropping %d measurements...", len(sensors))
	for _, s := range sensors {
		query := fmt.Sprintf("DROP MEASUREMENT \"%s\"", s.name)
		if err := execInfluxQuery(opts.influxURL, opts.database, query); err != nil {
			// Ignore errors (measurement may not exist)
			log.Printf("drop %s: %v (ignored)", s.name, err)
		}
	}
	return nil
}

func insertData(opts options, sensors []sensorInfo, start, end time.Time) error {
	const batchSize = 5000

	var batch strings.Builder
	rows := 0
	totalRows := 0

	flushBatch := func() error {
		if batch.Len() == 0 {
			return nil
		}
		if err := writeInfluxData(opts.influxURL, opts.database, batch.String()); err != nil {
			return err
		}
		batch.Reset()
		rows = 0
		return nil
	}

	for i, s := range sensors {
		gen := newGenerator(s.name, s.iotype, start, end, i)
		for ev := gen.next(); ev != nil; ev = gen.next() {
			if batch.Len() > 0 {
				batch.WriteString("\n")
			}
			batch.WriteString(toLineProtocol(ev))
			rows++
			totalRows++

			if rows >= batchSize {
				if err := flushBatch(); err != nil {
					return err
				}
			}
		}
	}

	if err := flushBatch(); err != nil {
		return err
	}

	log.Printf("done: inserted %d rows into InfluxDB", totalRows)
	return nil
}

func writeInfluxData(influxURL, database, data string) error {
	writeURL := fmt.Sprintf("%s/write?db=%s&precision=ns", influxURL, url.QueryEscape(database))
	resp, err := http.Post(writeURL, "text/plain", strings.NewReader(data))
	if err != nil {
		return fmt.Errorf("POST write: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("write failed: status %d", resp.StatusCode)
	}
	return nil
}

func execInfluxQuery(influxURL, database, query string) error {
	queryURL := fmt.Sprintf("%s/query?db=%s", influxURL, url.QueryEscape(database))
	resp, err := http.PostForm(queryURL, url.Values{"q": {query}})
	if err != nil {
		return fmt.Errorf("POST query: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("query failed: status %d", resp.StatusCode)
	}
	return nil
}

func parseFlags() options {
	var opt options
	flag.StringVar(&opt.influxURL, "url", "http://localhost:8086", "InfluxDB HTTP URL")
	flag.StringVar(&opt.database, "db", "uniset", "InfluxDB database name")
	flag.StringVar(&opt.config, "confile", "config/test.xml", "sensor config path")
	flag.StringVar(&opt.selector, "selector", "ALL", "sensor selector")
	flag.IntVar(&opt.sensors, "sensors", 0, "limit number of sensors (0 = all)")
	flag.DurationVar(&opt.duration, "duration", 10*time.Minute, "total time range to generate")
	defaultStart := time.Now().UTC().AddDate(0, 0, -7).Truncate(24*time.Hour).Format(time.RFC3339)
	flag.StringVar(&opt.start, "start", defaultStart, "start timestamp (RFC3339)")
	flag.StringVar(&opt.lpOutput, "lp-output", "", "write Line Protocol to file instead of inserting")
	flag.BoolVar(&opt.drop, "drop", false, "drop measurements before insert")
	flag.Parse()
	return opt
}
