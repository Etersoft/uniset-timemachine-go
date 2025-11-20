package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/url"
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
	points    int
	step      time.Duration
	start     string
	random    float64
	nodename  string
	producer  string
	batchSize int
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

	names := make([]string, 0, len(ids))
	for _, id := range ids {
		name, ok := cfg.NameByID(id)
		if !ok || name == "" {
			log.Fatalf("name not found for sensor %d", id)
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		log.Fatalf("no sensors resolved for selector %s", opts.selector)
	}

	startTs, err := time.Parse(time.RFC3339, opts.start)
	if err != nil {
		log.Fatalf("invalid --start: %v", err)
	}

	ctx := context.Background()
	conn, table, err := openClickhouse(ctx, opts.dsn, opts.table)
	if err != nil {
		log.Fatalf("clickhouse connect: %v", err)
	}
	defer conn.Close()

	rand.Seed(time.Now().UnixNano())
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
	for _, name := range names {
		ts := startTs
		base := float64(rand.Intn(1000))
		for i := 0; i < opts.points; i++ {
			val := base + float64(i%100)/100
			if opts.random > 0 {
				val += rand.Float64()*2*opts.random - opts.random
			}
			if err := batch.Append(ts, val, name, opts.nodename, opts.producer); err != nil {
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
			ts = ts.Add(opts.step)
		}
	}
	if rows > 0 {
		if err := batch.Send(); err != nil {
			log.Fatalf("send batch: %v", err)
		}
	}
	log.Printf("done: inserted %d rows into %s", totalRows, table)
}

func parseFlags() options {
	var opt options
	flag.StringVar(&opt.dsn, "db", "clickhouse://default:@localhost:9000/uniset", "ClickHouse DSN")
	flag.StringVar(&opt.table, "table", "uniset.main_history", "ClickHouse table (db.table)")
	flag.StringVar(&opt.config, "confile", "config/test.xml", "sensor config path")
	flag.StringVar(&opt.selector, "selector", "ALL", "sensor selector")
	flag.IntVar(&opt.sensors, "sensors", 0, "limit number of sensors (0 = all)")
	flag.IntVar(&opt.points, "points", 300, "records per sensor")
	flag.DurationVar(&opt.step, "step", 200*time.Millisecond, "step between records")
	flag.StringVar(&opt.start, "start", "2024-06-01T00:00:00Z", "start timestamp (RFC3339)")
	flag.Float64Var(&opt.random, "random", 0, "random variation (+/- range)")
	flag.StringVar(&opt.nodename, "nodename", "node1", "value for nodename column")
	flag.StringVar(&opt.producer, "producer", "bench", "value for producer column")
	flag.IntVar(&opt.batchSize, "batch", 10000, "rows per batch send")
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
