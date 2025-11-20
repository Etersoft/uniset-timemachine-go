package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pv/uniset-timemachine-go/pkg/config"
)

type options struct {
	dsn      string
	config   string
	selector string
	offset   int
	limit    int
	sensors  int
	points   int
	step     time.Duration
	start    string
	random   float64
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
	if opts.offset > len(ids) {
		fmt.Println("offset >= len(ids); nothing to do")
		return
	}
	ids = ids[opts.offset:]
	if opts.limit > 0 && len(ids) > opts.limit {
		ids = ids[:opts.limit]
	} else if opts.sensors > 0 && len(ids) > opts.sensors {
		ids = ids[:opts.sensors]
	}

	startTs, err := time.Parse(time.RFC3339, opts.start)
	if err != nil {
		log.Fatalf("invalid start: %v", err)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, opts.dsn)
	if err != nil {
		log.Fatalf("pg connect: %v", err)
	}
	defer pool.Close()

	rand.Seed(time.Now().UnixNano())

	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Fatalf("begin tx: %v", err)
	}
	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	stmt, err := tx.Prepare(ctx, "insert_history", "INSERT INTO main_history(sensor_id, timestamp, value) VALUES ($1, $2, $3)")
	if err != nil {
		log.Fatalf("prepare: %v", err)
	}

	total := 0
	for i, id := range ids {
		ts := startTs
		base := float64((opts.offset + i) % 1000)
		for j := 0; j < opts.points; j++ {
			val := base + float64(j%100)/100
			if opts.random > 0 {
				val += rand.Float64()*2*opts.random - opts.random
			}
			if _, err := tx.Exec(ctx, stmt.Name, id, ts, val); err != nil {
				log.Fatalf("insert: %v", err)
			}
			ts = ts.Add(opts.step)
			total++
		}
	}

	if err := tx.Commit(ctx); err != nil {
		log.Fatalf("commit: %v", err)
	}
	fmt.Printf("done: inserted %d rows (offset %d)\n", total, opts.offset)
}

func parseFlags() options {
	var opt options
	flag.StringVar(&opt.dsn, "db", "postgres://admin:123@localhost:5432/uniset?sslmode=disable", "Postgres DSN")
	flag.StringVar(&opt.config, "confile", "config/test.xml", "sensor config path")
	flag.StringVar(&opt.selector, "selector", "ALL", "sensor selector")
	flag.IntVar(&opt.offset, "offset", 0, "skip first N sensors")
	flag.IntVar(&opt.limit, "limit", 0, "limit sensors processed")
	flag.IntVar(&opt.sensors, "sensors", 0, "max sensors (deprecated)")
	flag.IntVar(&opt.points, "points", 300, "records per sensor")
	flag.DurationVar(&opt.step, "step", 200*time.Millisecond, "time step")
	flag.StringVar(&opt.start, "start", "2024-06-01T00:00:00Z", "start timestamp")
	flag.Float64Var(&opt.random, "random", 0, "random variation")
	flag.Parse()
	return opt
}
