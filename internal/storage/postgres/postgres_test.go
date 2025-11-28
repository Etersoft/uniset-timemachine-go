package postgres

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pv/uniset-timemachine-go/internal/storage"
)

func TestNewErrorsAndHelpers(t *testing.T) {
	ctx := context.Background()
	if _, err := New(ctx, Config{}); err == nil {
		t.Fatalf("expected error on empty conn string")
	}
	if !IsPostgresURL("postgres://localhost/db") || !IsPostgresURL("postgresql://host/db") {
		t.Fatalf("IsPostgresURL failed on valid inputs")
	}
	if IsPostgresURL("http://example.com") {
		t.Fatalf("IsPostgresURL false positive")
	}
	if got := sensorsAsArray([]int64{1, 2, 3}); !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Fatalf("sensorsAsArray mismatch: %#v", got)
	}
	ts := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	if got := combineTimestamp(ts, 500_000); !got.Equal(ts.Add(500 * time.Millisecond)) {
		t.Fatalf("combineTimestamp mismatch: %s", got)
	}
}

func TestStreamRangeWarmupEmptySensors(t *testing.T) {
	store := &Store{}
	// Warmup should short-circuit.
	evs, err := store.Warmup(context.Background(), nil, time.Time{})
	if err != nil || len(evs) != 0 {
		t.Fatalf("Warmup empty sensors got err=%v len=%d", err, len(evs))
	}
	// Range should error.
	if _, _, _, err := store.Range(context.Background(), nil, time.Time{}, time.Time{}); err == nil {
		t.Fatalf("Range empty sensors expected error")
	}
	// Stream should return error on channel immediately.
	_, errCh := store.Stream(context.Background(), storage.StreamRequest{})
	if err := <-errCh; err == nil {
		t.Fatalf("Stream empty sensors expected error")
	}
}

func TestStoreWarmupStreamAndRange_Postgres(t *testing.T) {
	dsn := os.Getenv("TM_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TM_POSTGRES_DSN is not set; skipping Postgres integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows := []pgHistoryRow{
		{sensorID: 50, ts: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), value: 100},
		{sensorID: 50, ts: time.Date(2024, 6, 1, 0, 0, 3, 0, time.UTC), value: 120},
		{sensorID: 51, ts: time.Date(2024, 6, 1, 0, 0, 1, 0, time.UTC), usec: 250_000, value: 200.5},
		{sensorID: 51, ts: time.Date(2024, 6, 1, 0, 0, 4, 0, time.UTC), usec: 999_000, value: 240},
	}

	setupPostgresFixtures(t, ctx, dsn, rows)

	store, err := New(ctx, Config{ConnString: dsn})
	if err != nil {
		t.Fatalf("postgres.New error: %v", err)
	}
	defer store.Close()

	sensors := []int64{50, 51}
	from := rows[0].ts
	events, err := store.Warmup(ctx, sensors, from.Add(2500*time.Millisecond))
	if err != nil {
		t.Fatalf("Warmup returned error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("Warmup expected 2 events, got %d: %#v", len(events), events)
	}
	got := map[int64]storage.SensorEvent{}
	for _, ev := range events {
		got[ev.SensorID] = ev
	}
	if ev := got[50]; ev.Value != 100 || !ev.Timestamp.Equal(rows[0].ts) {
		t.Fatalf("Warmup sensor 50 mismatch: %#v", ev)
	}
	expTs := rows[2].ts.Add(time.Duration(rows[2].usec) * time.Microsecond)
	if ev := got[51]; ev.Value != 200.5 || !ev.Timestamp.Equal(expTs) {
		t.Fatalf("Warmup sensor 51 mismatch: %#v", ev)
	}

	min, max, _, err := store.Range(ctx, sensors, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Range returned error: %v", err)
	}
	wantMax := rows[3].ts.Add(time.Duration(rows[3].usec) * time.Microsecond)
	if !min.Equal(rows[0].ts) {
		t.Fatalf("Range min mismatch: got %s", min)
	}
	if !max.Equal(wantMax) {
		t.Fatalf("Range max mismatch: got %s want %s", max, wantMax)
	}

	req := storage.StreamRequest{
		Sensors: sensors,
		From:    from,
		To:      from.Add(5 * time.Second),
		Window:  2 * time.Second,
	}
	dataCh, errCh := store.Stream(ctx, req)

	var batches [][]storage.SensorEvent
	for batch := range dataCh {
		if len(batch) == 0 {
			continue
		}
		batches = append(batches, batch)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if len(batches) == 0 {
		t.Fatalf("Stream returned no batches")
	}

	var streamed []storage.SensorEvent
	for _, batch := range batches {
		streamed = append(streamed, batch...)
	}
	if len(streamed) != len(rows) {
		t.Fatalf("expected %d events, got %d", len(rows), len(streamed))
	}
	check := []struct {
		id  int64
		ts  time.Time
		val float64
	}{
		{50, rows[0].ts, 100},
		{51, expTs, 200.5},
		{50, rows[1].ts, 120},
		{51, wantMax, 240},
	}
	for i, want := range check {
		ev := streamed[i]
		if ev.SensorID != want.id || ev.Value != want.val || !ev.Timestamp.Equal(want.ts) {
			t.Fatalf("stream event %d mismatch: %#v want %#v", i, ev, want)
		}
	}
}

type pgHistoryRow struct {
	sensorID int64
	ts       time.Time
	usec     int64
	value    float64
}

func setupPostgresFixtures(t *testing.T, ctx context.Context, dsn string, rows []pgHistoryRow) {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}

	const schema = `
CREATE TABLE IF NOT EXISTS main_history(
	sensor_id BIGINT NOT NULL,
	timestamp TIMESTAMPTZ NOT NULL,
	time_usec BIGINT DEFAULT 0,
	value DOUBLE PRECISION NOT NULL
);`
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		t.Fatalf("create schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE main_history`); err != nil {
		pool.Close()
		t.Fatalf("truncate table: %v", err)
	}
	for _, row := range rows {
		if _, err := pool.Exec(ctx, `INSERT INTO main_history(sensor_id, timestamp, time_usec, value) VALUES ($1, $2, $3, $4)`,
			row.sensorID, row.ts, row.usec, row.value); err != nil {
			pool.Close()
			t.Fatalf("insert row %#v: %v", row, err)
		}
	}

	t.Cleanup(func() {
		ctxCleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctxCleanup, `TRUNCATE main_history`)
		pool.Close()
	})
}
