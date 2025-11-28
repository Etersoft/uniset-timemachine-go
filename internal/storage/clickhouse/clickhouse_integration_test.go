//go:build integration

package clickhouse

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/pv/uniset-timemachine-go/internal/storage"
)

// Integration test for ClickHouse Store. Requires CH_TEST_DSN env var.
func TestIntegrationStoreWarmupStreamRange(t *testing.T) {
	dsn := os.Getenv("CH_TEST_DSN")
	if dsn == "" {
		t.Skip("CH_TEST_DSN is not set; skipping ClickHouse integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resolver := &fakeResolver{
		idToName: map[int64]string{1: "S1", 2: "S2"},
		nameToID: map[string]int64{"S1": 1, "S2": 2},
	}

	store, err := New(ctx, Config{DSN: dsn, Resolver: resolver})
	if err != nil {
		t.Fatalf("clickhouse.New: %v", err)
	}
	defer store.Close()

	// Prepare test data.
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	rows := []struct {
		name  string
		ts    time.Time
		value float64
	}{
		{"S1", now, 10},
		{"S2", now.Add(time.Second), 20},
		{"S1", now.Add(2 * time.Second), 11},
	}

	// Insert rows directly using connection.
	conn := store.conn
	if err := conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS main_history (name String, timestamp DateTime, value Float64) ENGINE=MergeTree ORDER BY (name, timestamp)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := conn.Exec(ctx, "TRUNCATE TABLE main_history"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	batch, err := conn.PrepareBatch(ctx, "INSERT INTO main_history (name, timestamp, value)")
	if err != nil {
		t.Fatalf("prepare batch: %v", err)
	}
	for _, r := range rows {
		if err := batch.Append(r.name, r.ts, r.value); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("send batch: %v", err)
	}

	// Warmup should return last values before from.
	from := now.Add(1500 * time.Millisecond)
	evs, err := store.Warmup(ctx, []int64{1, 2}, from)
	if err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("Warmup expected 2 events, got %d", len(evs))
	}

	// Range should see both sensors.
	minTs, maxTs, cnt, err := store.Range(ctx, []int64{1, 2}, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if cnt < 2 {
		t.Fatalf("Range count = %d, want >=2", cnt)
	}
	if minTs.After(now) || maxTs.Before(rows[2].ts) {
		t.Fatalf("Range min/max mismatch: %s %s", minTs, maxTs)
	}

	// Stream over full window.
	req := storage.StreamRequest{
		Sensors: []int64{1, 2},
		From:    now,
		To:      now.Add(3 * time.Second),
		Window:  time.Second,
	}
	dataCh, errCh := store.Stream(ctx, req)
	var streamed []storage.SensorEvent
loop:
	for {
		select {
		case batch, ok := <-dataCh:
			if !ok {
				break loop
			}
			streamed = append(streamed, batch...)
		case err := <-errCh:
			if err != nil {
				t.Fatalf("Stream err: %v", err)
			}
		}
	}
	if len(streamed) != len(rows) {
		t.Fatalf("Streamed %d events, want %d", len(streamed), len(rows))
	}
}
