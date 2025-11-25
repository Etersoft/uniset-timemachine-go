package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/pv/uniset-timemachine-go/internal/storage"
)

func TestStoreWarmupStreamAndRange(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	// Специальные сенсоры из config/test.xml: Sensor10001_S, Sensor10002_S
	rows := []historyRow{
		{sensorID: 10001, ts: start, value: 10001},
		{sensorID: 10001, ts: start.Add(6 * time.Second), value: 10011},
		{sensorID: 10002, ts: start.Add(2 * time.Second), usec: 500000, value: 20022},
		{sensorID: 10002, ts: start.Add(6 * time.Second), usec: 100, value: 20033},
		{sensorID: 99999, ts: start.Add(3 * time.Second), value: 999}, // должен игнорироваться фильтром
	}

	src := prepareSQLiteDB(t, rows)
	store, err := New(ctx, Config{Source: src})
	if err != nil {
		t.Fatalf("sqlite.New error: %v", err)
	}
	t.Cleanup(store.Close)

	sensors := []int64{10001, 10002}
	warmupFrom := start.Add(5 * time.Second)

	events, err := store.Warmup(ctx, sensors, warmupFrom)
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

	if ev := got[10001]; ev.Value != 10001 || !ev.Timestamp.Equal(start) {
		t.Fatalf("Warmup sensor 10001 mismatch: %#v", ev)
	}
	expTs := start.Add(2*time.Second + 500*time.Millisecond)
	if ev := got[10002]; ev.Value != 20022 || !ev.Timestamp.Equal(expTs) {
		t.Fatalf("Warmup sensor 10002 mismatch: %#v", ev)
	}

	min, max, count, err := store.Range(ctx, sensors, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Range returned error: %v", err)
	}
	if !min.Equal(start) {
		t.Fatalf("Range min mismatch: %s", min)
	}
	if !max.Equal(start.Add(6*time.Second + 100*time.Microsecond)) {
		t.Fatalf("Range max mismatch: %s", max)
	}
	if count != int64(len(sensors)) {
		t.Fatalf("Range count mismatch: %d", count)
	}

	req := storage.StreamRequest{
		Sensors: sensors,
		From:    start,
		To:      start.Add(7 * time.Second),
		Window:  3 * time.Second,
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

	if len(batches) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(batches))
	}
	if len(batches[0]) != 2 || len(batches[1]) != 2 {
		t.Fatalf("unexpected batch sizes: %#v", batches)
	}

	var streamed []storage.SensorEvent
	for _, b := range batches {
		streamed = append(streamed, b...)
	}
	if len(streamed) != 4 {
		t.Fatalf("expected 4 streamed events, got %d", len(streamed))
	}
	checkOrder := []struct {
		id  int64
		ts  time.Time
		val float64
	}{
		{10001, start, 10001},
		{10002, expTs, 20022},
		{10001, start.Add(6 * time.Second), 10011},
		{10002, start.Add(6*time.Second + 100*time.Microsecond), 20033},
	}
	for i, want := range checkOrder {
		ev := streamed[i]
		if ev.SensorID != want.id || ev.Value != want.val || !ev.Timestamp.Equal(want.ts) {
			t.Fatalf("stream event %d mismatch: %#v want %#v", i, ev, want)
		}
	}
}

type historyRow struct {
	sensorID int64
	ts       time.Time
	usec     int64
	value    float64
}

func prepareSQLiteDB(t *testing.T, rows []historyRow) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "history.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}

	schema := `
CREATE TABLE main_history(
	sensor_id INTEGER NOT NULL,
	timestamp TEXT NOT NULL,
	time_usec INTEGER,
	value REAL NOT NULL
);
`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		t.Fatalf("create schema: %v", err)
	}

	stmt, err := db.Prepare(`INSERT INTO main_history(sensor_id, timestamp, time_usec, value) VALUES (?, ?, ?, ?)`)
	if err != nil {
		db.Close()
		t.Fatalf("prepare insert: %v", err)
	}
	defer stmt.Close()

	for _, row := range rows {
		ts := row.ts.Format(time.RFC3339)
		if _, err := stmt.Exec(row.sensorID, ts, row.usec, row.value); err != nil {
			db.Close()
			t.Fatalf("insert row: %v", err)
		}
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	return path
}
