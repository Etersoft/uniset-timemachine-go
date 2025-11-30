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
	// Test combineDateTimeUsec
	date := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	got := combineDateTimeUsec(date, "12:30:45", 500_000)
	want := time.Date(2024, 6, 1, 12, 30, 45, 500_000_000, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("combineDateTimeUsec mismatch: got %s want %s", got, want)
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
		{sensorID: 50, ts: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), usec: 0, value: 100},
		{sensorID: 50, ts: time.Date(2024, 6, 1, 0, 0, 3, 0, time.UTC), usec: 0, value: 120},
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
	// Timestamps: date+time+usec combined in UTC
	expTs51 := time.Date(2024, 6, 1, 0, 0, 1, rows[2].usec*1000, time.UTC)
	if ev := got[51]; ev.Value != 200.5 || !ev.Timestamp.Equal(expTs51) {
		t.Fatalf("Warmup sensor 51 mismatch: got %#v want ts=%s", ev, expTs51)
	}

	min, max, _, err := store.Range(ctx, sensors, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Range returned error: %v", err)
	}
	wantMax := time.Date(2024, 6, 1, 0, 0, 4, rows[3].usec*1000, time.UTC)
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
		{51, expTs51, 200.5},
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

// Test Range with date bounds (Range filters by date only, not time)
func TestRangeWithBounds_Postgres(t *testing.T) {
	dsn := os.Getenv("TM_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TM_POSTGRES_DSN is not set; skipping Postgres integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use data across multiple days to test date filtering
	rows := []pgHistoryRow{
		{sensorID: 50, ts: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), usec: 0, value: 100},
		{sensorID: 50, ts: time.Date(2024, 6, 2, 0, 0, 0, 0, time.UTC), usec: 0, value: 120},
		{sensorID: 51, ts: time.Date(2024, 6, 3, 0, 0, 0, 0, time.UTC), usec: 0, value: 200.5},
		{sensorID: 51, ts: time.Date(2024, 6, 4, 0, 0, 0, 0, time.UTC), usec: 0, value: 240},
	}

	setupPostgresFixtures(t, ctx, dsn, rows)

	store, err := New(ctx, Config{ConnString: dsn})
	if err != nil {
		t.Fatalf("postgres.New error: %v", err)
	}
	defer store.Close()

	sensors := []int64{50, 51}

	// Test Range with date bounds (from=June 2, to=June 3)
	minBound, maxBound, countBound, err := store.Range(ctx, sensors,
		time.Date(2024, 6, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 6, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Range with bounds returned error: %v", err)
	}
	// minBound should be June 2
	if !minBound.Equal(rows[1].ts) {
		t.Fatalf("Range (bounded) min mismatch: got %s want %s", minBound, rows[1].ts)
	}
	// maxBound should be June 3
	if !maxBound.Equal(rows[2].ts) {
		t.Fatalf("Range (bounded) max mismatch: got %s want %s", maxBound, rows[2].ts)
	}
	if countBound == 0 {
		t.Fatalf("Range (bounded) count should not be 0")
	}
}

// Test Stream context cancellation
func TestStreamContextCancel_Postgres(t *testing.T) {
	dsn := os.Getenv("TM_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TM_POSTGRES_DSN is not set; skipping Postgres integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows := []pgHistoryRow{
		{sensorID: 50, ts: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), usec: 0, value: 100},
	}
	setupPostgresFixtures(t, ctx, dsn, rows)

	store, err := New(ctx, Config{ConnString: dsn})
	if err != nil {
		t.Fatalf("postgres.New error: %v", err)
	}
	defer store.Close()

	// Create a canceled context for Stream
	canceledCtx, cancelNow := context.WithCancel(ctx)
	cancelNow()

	req := storage.StreamRequest{
		Sensors: []int64{50},
		From:    time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
		To:      time.Date(2024, 6, 1, 0, 10, 0, 0, time.UTC),
		Window:  time.Second,
	}
	dataCh, errCh := store.Stream(canceledCtx, req)

	// Drain data channel
	for range dataCh {
	}

	if err := <-errCh; err == nil {
		t.Fatalf("Stream with canceled context should return error")
	}
}

// Test Close
func TestClose_Postgres(t *testing.T) {
	dsn := os.Getenv("TM_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TM_POSTGRES_DSN is not set; skipping Postgres integration test")
	}

	ctx := context.Background()
	store, err := New(ctx, Config{ConnString: dsn})
	if err != nil {
		t.Fatalf("postgres.New error: %v", err)
	}

	// Close should not panic
	store.Close()
}

// Test Close with nil pool
func TestCloseNilPool(t *testing.T) {
	store := &Store{pool: nil}
	// Should not panic on nil pool
	store.Close()
}

// Test Warmup returns empty for empty time range
func TestWarmupEmptyResult_Postgres(t *testing.T) {
	dsn := os.Getenv("TM_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TM_POSTGRES_DSN is not set; skipping Postgres integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows := []pgHistoryRow{
		{sensorID: 50, ts: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), usec: 0, value: 100},
	}
	setupPostgresFixtures(t, ctx, dsn, rows)

	store, err := New(ctx, Config{ConnString: dsn})
	if err != nil {
		t.Fatalf("postgres.New error: %v", err)
	}
	defer store.Close()

	// Warmup before any data exists
	events, err := store.Warmup(ctx, []int64{50}, time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Warmup returned error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Warmup expected 0 events for time before data, got %d", len(events))
	}
}

// Test Stream returns empty for non-existent sensors
func TestStreamNonExistentSensor_Postgres(t *testing.T) {
	dsn := os.Getenv("TM_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TM_POSTGRES_DSN is not set; skipping Postgres integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows := []pgHistoryRow{
		{sensorID: 50, ts: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), usec: 0, value: 100},
	}
	setupPostgresFixtures(t, ctx, dsn, rows)

	store, err := New(ctx, Config{ConnString: dsn})
	if err != nil {
		t.Fatalf("postgres.New error: %v", err)
	}
	defer store.Close()

	req := storage.StreamRequest{
		Sensors: []int64{999}, // Non-existent sensor
		From:    time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
		To:      time.Date(2024, 6, 1, 0, 1, 0, 0, time.UTC),
		Window:  time.Minute,
	}
	dataCh, errCh := store.Stream(ctx, req)

	var eventCount int
	for batch := range dataCh {
		eventCount += len(batch)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("Stream expected 0 events for non-existent sensor, got %d", eventCount)
	}
}

type pgHistoryRow struct {
	sensorID int64
	ts       time.Time
	usec     int
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
	id BIGSERIAL PRIMARY KEY NOT NULL,
	date DATE NOT NULL,
	time TIME NOT NULL,
	time_usec INT NOT NULL CHECK (time_usec >= 0),
	sensor_id INT NOT NULL,
	value DOUBLE PRECISION NOT NULL,
	node INT NOT NULL DEFAULT 0,
	confirm INT DEFAULT NULL
);`
	// Drop and recreate for clean state
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS main_history`); err != nil {
		pool.Close()
		t.Fatalf("drop table: %v", err)
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		t.Fatalf("create schema: %v", err)
	}
	for _, row := range rows {
		date := row.ts.Format("2006-01-02")
		timeStr := row.ts.Format("15:04:05")
		if _, err := pool.Exec(ctx, `INSERT INTO main_history(date, time, time_usec, sensor_id, value, node) VALUES ($1, $2, $3, $4, $5, 0)`,
			date, timeStr, row.usec, row.sensorID, row.value); err != nil {
			pool.Close()
			t.Fatalf("insert row %#v: %v", row, err)
		}
	}

	t.Cleanup(func() {
		ctxCleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctxCleanup, `DROP TABLE IF EXISTS main_history`)
		pool.Close()
	})
}
