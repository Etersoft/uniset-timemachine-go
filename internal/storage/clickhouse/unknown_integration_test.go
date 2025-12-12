package clickhouse

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/pv/uniset-timemachine-go/pkg/config"
)

type regResolver struct{ r *config.SensorRegistry }

func (rr regResolver) NameByHash(hash int64) (string, bool) {
	if k, ok := rr.r.ByHash(hash); ok {
		return k.Name, true
	}
	return "", false
}

func (rr regResolver) HashByName(name string) (int64, bool) {
	if k, ok := rr.r.ByName(name); ok {
		return k.Hash, true
	}
	return 0, false
}

// Integration test for ClickHouse RangeWithUnknown.
// Requires env CLICKHOUSE_TEST_DSN; uses a temporary Memory table.
func TestRangeWithUnknown_ClickHouse(t *testing.T) {
	dsn := os.Getenv("CLICKHOUSE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLICKHOUSE_TEST_DSN is not set; skipping integration test")
	}
	ctx := context.Background()

	reg := config.NewSensorRegistry()
	if err := reg.Add(config.NewSensorKey("S1", nil)); err != nil {
		t.Fatalf("registry add: %v", err)
	}
	if err := reg.Add(config.NewSensorKey("S2", nil)); err != nil {
		t.Fatalf("registry add: %v", err)
	}

	table := "main_history_tmp_unknown"
	store, err := New(ctx, Config{
		DSN:      dsn,
		Table:    table,
		Resolver: regResolver{reg},
	})
	if err != nil {
		t.Fatalf("clickhouse.New: %v", err)
	}
	defer store.Close()

	// Create Memory table and seed data
	if err := store.conn.Exec(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if err := store.conn.Exec(ctx, "CREATE TABLE "+table+" (name String, timestamp DateTime, value Float64) ENGINE=Memory"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := store.conn.Exec(ctx, "INSERT INTO "+table+" (name, timestamp, value) VALUES (?, ?, ?), (?, ?, ?), (?, ?, ?)",
		"S1", now, 10.0,
		"S2", now.Add(time.Second), 20.0,
		"S3", now.Add(2*time.Second), 30.0); err != nil {
		t.Fatalf("insert: %v", err)
	}

	h1 := config.HashForName("S1")
	h2 := config.HashForName("S2")
	min, max, known, unknown, err := store.RangeWithUnknown(ctx, []int64{h1, h2}, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("RangeWithUnknown: %v", err)
	}
	if known != 2 {
		t.Fatalf("known=%d want=2", known)
	}
	if unknown != 1 {
		t.Fatalf("unknown=%d want=1", unknown)
	}
	if min.IsZero() || max.IsZero() {
		t.Fatalf("expected min/max set")
	}
}
