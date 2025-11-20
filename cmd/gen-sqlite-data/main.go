package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"time"

	_ "modernc.org/sqlite"

	"github.com/pv/uniset-timemachine-go/pkg/config"
)

type options struct {
	dbPath      string
	configPath  string
	selector    string
	startID     int64
	maxSensors  int
	points      int
	step        time.Duration
	startTS     string
	reset       bool
	randomRange float64
}

func main() {
	opts := parseFlags()
	rand.Seed(time.Now().UnixNano())

	sensorIDs, err := loadSensorIDs(opts)
	if err != nil {
		log.Fatalf("load sensors: %v", err)
	}
	if len(sensorIDs) == 0 {
		log.Fatal("no sensors to generate")
	}

	start, err := time.Parse(time.RFC3339, opts.startTS)
	if err != nil {
		log.Fatalf("invalid --start: %v", err)
	}

	db, err := sql.Open("sqlite", opts.dbPath)
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	if err := ensureSchema(db); err != nil {
		log.Fatalf("ensure schema: %v", err)
	}
	if opts.reset {
		if _, err := db.Exec(`DELETE FROM main_history`); err != nil {
			log.Fatalf("clear table: %v", err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		log.Fatalf("begin tx: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO main_history(sensor_id, timestamp, time_usec, value) VALUES (?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		log.Fatalf("prepare insert: %v", err)
	}

	total := len(sensorIDs) * opts.points
	var inserted int
	for _, sensorID := range sensorIDs {
		ts := start
		for i := 0; i < opts.points; i++ {
			if _, err := stmt.Exec(sensorID, ts.UTC().Format(time.RFC3339), 0, valueFor(sensorID, i, opts.randomRange)); err != nil {
				stmt.Close()
				tx.Rollback()
				log.Fatalf("insert sensor %d: %v", sensorID, err)
			}
			ts = ts.Add(opts.step)
			inserted++
			if inserted%10000 == 0 {
				log.Printf("inserted %d/%d rows", inserted, total)
			}
		}
	}

	stmt.Close()
	if err := tx.Commit(); err != nil {
		log.Fatalf("commit tx: %v", err)
	}
	log.Printf("done: inserted %d rows for %d sensors into %s", inserted, len(sensorIDs), opts.dbPath)
}

func parseFlags() options {
	var opt options
	flag.StringVar(&opt.dbPath, "db", "test-large.db", "path to sqlite database file")
	flag.StringVar(&opt.configPath, "confile", "", "optional sensor config (XML/JSON) to derive IDs from")
	flag.StringVar(&opt.selector, "selector", "", "sensor selector/pattern (used with --confile)")
	flag.Int64Var(&opt.startID, "start-id", 1, "first sensor ID if config is not provided")
	flag.IntVar(&opt.maxSensors, "sensors", 1000, "number of sensors to generate (ignored if configuration has fewer sensors)")
	flag.IntVar(&opt.points, "points", 1000, "records per sensor")
	flag.DurationVar(&opt.step, "step", time.Second, "time delta between records")
	flag.StringVar(&opt.startTS, "start", "2024-06-01T00:00:00Z", "start timestamp (RFC3339)")
	flag.BoolVar(&opt.reset, "reset", true, "clear existing data in main_history")
	flag.Float64Var(&opt.randomRange, "random", 0, "if >0, add random variation (-range..+range) to sensor values")
	flag.Parse()
	return opt
}

func loadSensorIDs(opt options) ([]int64, error) {
	if opt.configPath != "" {
		cfg, err := config.Load(opt.configPath)
		if err != nil {
			return nil, err
		}
		selector := opt.selector
		if selector == "" {
			selector = "ALL"
		}
		all, err := cfg.Resolve(selector)
		if err != nil {
			return nil, err
		}
		if opt.maxSensors > 0 && len(all) > opt.maxSensors {
			return all[:opt.maxSensors], nil
		}
		return all, nil
	}

	if opt.maxSensors <= 0 {
		return nil, fmt.Errorf("--sensors must be > 0 when config is not provided")
	}
	sensors := make([]int64, 0, opt.maxSensors)
	for i := 0; i < opt.maxSensors; i++ {
		sensors = append(sensors, opt.startID+int64(i))
	}
	return sensors, nil
}

func ensureSchema(db *sql.DB) error {
	schema := `
CREATE TABLE IF NOT EXISTS main_history(
	sensor_id INTEGER NOT NULL,
	timestamp TEXT NOT NULL,
	time_usec INTEGER,
	value REAL NOT NULL
);`
	_, err := db.Exec(schema)
	return err
}

func valueFor(sensorID int64, idx int, randomRange float64) float64 {
	base := float64(sensorID%1000) + float64(idx%100)/100
	if randomRange <= 0 {
		return base
	}
	return base + rand.Float64()*2*randomRange - randomRange
}
