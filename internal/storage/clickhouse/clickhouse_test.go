package clickhouse

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/pv/uniset-timemachine-go/internal/storage"
)

type fakeResolver struct {
	idToName map[int64]string
	nameToID map[string]int64
}

func (r *fakeResolver) NameByID(id int64) (string, bool) {
	name, ok := r.idToName[id]
	return name, ok
}

func (r *fakeResolver) IDByName(name string) (int64, bool) {
	id, ok := r.nameToID[name]
	return id, ok
}

func TestIsSource(t *testing.T) {
	tests := []struct {
		dsn  string
		want bool
	}{
		// Native protocol (supported)
		{"clickhouse://localhost:9000/db", true},
		{"ch://localhost", true},
		{"CLICKHOUSE://LOCALHOST", true},
		{"CH://host", true},
		{"clickhouse://user:pass@localhost:9000/db", true},
		{"ch://user:pass@localhost/db", true},

		// Not ClickHouse
		{"postgres://localhost/db", false},
		{"", false},
		{"sqlite://test.db", false},
		{"http://clickhouse.example.com", false},
		{"https://clickhouse.example.com", false},
		// HTTP protocol is not supported (requires database/sql interface)
		{"clickhouse-http://localhost:8123/db", false},
		{"ch-http://localhost/db", false},
	}
	for _, tt := range tests {
		if got := IsSource(tt.dsn); got != tt.want {
			t.Errorf("IsSource(%q) = %v, want %v", tt.dsn, got, tt.want)
		}
	}
}

func TestNormalizeDSN(t *testing.T) {
	tests := []struct {
		dsn  string
		want string
	}{
		// clickhouse:// - stays unchanged
		{"clickhouse://localhost:9000/db", "clickhouse://localhost:9000/db"},
		{"clickhouse://user:pass@localhost:9000/db", "clickhouse://user:pass@localhost:9000/db"},

		// ch:// -> clickhouse://
		{"ch://localhost", "clickhouse://localhost"},
		{"CH://Host:9000/DB", "clickhouse://Host:9000/DB"},
		{"ch://user:pass@localhost/db", "clickhouse://user:pass@localhost/db"},
	}
	for _, tt := range tests {
		if got := normalizeDSN(tt.dsn); got != tt.want {
			t.Errorf("normalizeDSN(%q) = %q, want %q", tt.dsn, got, tt.want)
		}
	}
}

func TestNewDSNParsing(t *testing.T) {
	// These tests verify DSN parsing without actually connecting.
	// They should fail on ping, but parsing should succeed.
	ctx := context.Background()

	tests := []struct {
		name    string
		dsn     string
		wantErr string
	}{
		{
			name:    "host without port",
			dsn:     "clickhouse://somehost/testdb",
			wantErr: "ping", // Should fail on ping, not parsing
		},
		{
			name:    "host with port",
			dsn:     "clickhouse://somehost:9999/testdb",
			wantErr: "ping",
		},
		{
			name:    "empty database defaults to default",
			dsn:     "clickhouse://somehost:9000",
			wantErr: "ping",
		},
		{
			name:    "with credentials",
			dsn:     "clickhouse://user:pass@somehost:9000/testdb",
			wantErr: "ping",
		},
		{
			name:    "ch scheme converted to clickhouse",
			dsn:     "ch://somehost:9000/testdb",
			wantErr: "ping",
		},
	}

	resolver := &fakeResolver{
		idToName: map[int64]string{1: "Test"},
		nameToID: map[string]int64{"Test": 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(ctx, Config{DSN: tt.dsn, Resolver: resolver})
			if err == nil {
				t.Fatalf("expected error for DSN %q", tt.dsn)
			}
			// Verify it's a connection error, not a parsing error
			if tt.wantErr != "" && !contains(err.Error(), tt.wantErr) && !contains(err.Error(), "dial") && !contains(err.Error(), "connect") {
				t.Errorf("unexpected error type for DSN %q: %v", tt.dsn, err)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestNamesForSensors(t *testing.T) {
	resolver := &fakeResolver{
		idToName: map[int64]string{
			1: "S1",
			2: "S2",
		},
		nameToID: map[string]int64{
			"S1": 1,
			"S2": 2,
		},
	}
	store := &Store{resolver: resolver}

	names, err := store.namesForSensors([]int64{1, 2, 1})
	if err != nil {
		t.Fatalf("namesForSensors returned error: %v", err)
	}
	if len(names) != 2 || names[0] != "S1" || names[1] != "S2" {
		t.Fatalf("unexpected names: %v", names)
	}
}

func TestNamesForSensorsMissing(t *testing.T) {
	store := &Store{resolver: &fakeResolver{idToName: map[int64]string{1: "S1"}, nameToID: map[string]int64{"S1": 1}}}
	if _, err := store.namesForSensors([]int64{1, 3}); err == nil {
		t.Fatalf("expected error for missing sensor name")
	}
}

func TestNewErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := New(ctx, Config{DSN: "", Resolver: &fakeResolver{}}); err == nil {
		t.Fatalf("expected error for empty DSN")
	}
	if _, err := New(ctx, Config{DSN: "clickhouse://localhost:9000", Resolver: nil}); err == nil {
		t.Fatalf("expected error for nil resolver")
	}
	if _, err := New(ctx, Config{DSN: "://bad", Resolver: &fakeResolver{}}); err == nil {
		t.Fatalf("expected error for invalid DSN")
	}
}

func TestRangeNamesError(t *testing.T) {
	store := &Store{resolver: &fakeResolver{}}
	if _, _, _, err := store.Range(context.Background(), []int64{42}, time.Time{}, time.Time{}); err == nil {
		t.Fatalf("expected error when resolver misses name")
	}
}

func TestStreamNamesError(t *testing.T) {
	store := &Store{resolver: &fakeResolver{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, errCh := store.Stream(ctx, storage.StreamRequest{Sensors: []int64{42}, From: time.Now(), To: time.Now().Add(time.Second)})
	if err := <-errCh; err == nil {
		t.Fatalf("expected error when resolver misses name")
	}
}

func TestWarmupNamesError(t *testing.T) {
	store := &Store{resolver: &fakeResolver{}}
	if _, err := store.Warmup(context.Background(), []int64{42}, time.Now()); err == nil {
		t.Fatalf("expected error when resolver misses name")
	}
}

func TestClickhouseEmptySensorsBranches(t *testing.T) {
	store := &Store{resolver: &fakeResolver{}}

	// Warmup with empty sensors should short-circuit without error.
	evs, err := store.Warmup(context.Background(), nil, time.Now())
	if err != nil || len(evs) != 0 {
		t.Fatalf("warmup empty sensors err=%v len=%d", err, len(evs))
	}

	// Stream with empty sensors should emit explicit error.
	_, errCh := store.Stream(context.Background(), storage.StreamRequest{Sensors: nil, From: time.Now(), To: time.Now().Add(time.Second)})
	if err := <-errCh; err == nil {
		t.Fatalf("stream empty sensors expected error")
	}

	// Range with empty sensors should return error before touching DB.
	if _, _, _, err := store.Range(context.Background(), nil, time.Time{}, time.Time{}); err == nil {
		t.Fatalf("range empty sensors expected error")
	}
}

// Integration tests require running ClickHouse server.
// Set TM_CLICKHOUSE_DSN environment variable to run these tests.
// Example: TM_CLICKHOUSE_DSN=clickhouse://localhost:9000/uniset go test -v

func TestStoreWarmupStreamAndRange_Clickhouse(t *testing.T) {
	dsn := os.Getenv("TM_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Skip("TM_CLICKHOUSE_DSN is not set; skipping ClickHouse integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Test sensor data
	rows := []chHistoryRow{
		{name: "TestSensor_A", ts: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), value: 100},
		{name: "TestSensor_A", ts: time.Date(2024, 6, 1, 0, 0, 3, 0, time.UTC), value: 120},
		{name: "TestSensor_B", ts: time.Date(2024, 6, 1, 0, 0, 1, 0, time.UTC), value: 200.5},
		{name: "TestSensor_B", ts: time.Date(2024, 6, 1, 0, 0, 4, 0, time.UTC), value: 240},
	}

	resolver := &fakeResolver{
		idToName: map[int64]string{
			50: "TestSensor_A",
			51: "TestSensor_B",
		},
		nameToID: map[string]int64{
			"TestSensor_A": 50,
			"TestSensor_B": 51,
		},
	}

	setupClickhouseFixtures(t, ctx, dsn, rows)

	store, err := New(ctx, Config{DSN: dsn, Resolver: resolver, Table: "tm_test_history"})
	if err != nil {
		t.Fatalf("clickhouse.New error: %v", err)
	}
	defer store.Close()

	sensors := []int64{50, 51}
	from := rows[0].ts

	// Test Warmup
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
	if ev := got[51]; ev.Value != 200.5 || !ev.Timestamp.Equal(rows[2].ts) {
		t.Fatalf("Warmup sensor 51 mismatch: %#v", ev)
	}

	// Test Range without time bounds
	min, max, count, err := store.Range(ctx, sensors, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Range returned error: %v", err)
	}
	if !min.Equal(rows[0].ts) {
		t.Fatalf("Range min mismatch: got %s want %s", min, rows[0].ts)
	}
	if !max.Equal(rows[3].ts) {
		t.Fatalf("Range max mismatch: got %s want %s", max, rows[3].ts)
	}
	if count != 2 {
		t.Fatalf("Range count mismatch: got %d want 2", count)
	}

	// Test Range with time bounds (from=1s, to=4s includes rows[1], rows[2], rows[3])
	minBound, maxBound, countBound, err := store.Range(ctx, sensors, from.Add(time.Second), from.Add(4*time.Second))
	if err != nil {
		t.Fatalf("Range with bounds returned error: %v", err)
	}
	// minBound should be rows[2] @ 00:00:01
	if !minBound.Equal(rows[2].ts) {
		t.Fatalf("Range (bounded) min mismatch: got %s want %s", minBound, rows[2].ts)
	}
	// maxBound should be rows[3] @ 00:00:04
	if !maxBound.Equal(rows[3].ts) {
		t.Fatalf("Range (bounded) max mismatch: got %s want %s", maxBound, rows[3].ts)
	}
	_ = countBound

	// Test Stream
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
}

func TestStreamContextCancel(t *testing.T) {
	dsn := os.Getenv("TM_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Skip("TM_CLICKHOUSE_DSN is not set; skipping ClickHouse integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resolver := &fakeResolver{
		idToName: map[int64]string{50: "TestSensor_A"},
		nameToID: map[string]int64{"TestSensor_A": 50},
	}

	rows := []chHistoryRow{
		{name: "TestSensor_A", ts: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), value: 100},
	}
	setupClickhouseFixtures(t, ctx, dsn, rows)

	store, err := New(ctx, Config{DSN: dsn, Resolver: resolver, Table: "tm_test_history"})
	if err != nil {
		t.Fatalf("clickhouse.New error: %v", err)
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

func TestClose(t *testing.T) {
	dsn := os.Getenv("TM_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Skip("TM_CLICKHOUSE_DSN is not set; skipping ClickHouse integration test")
	}

	ctx := context.Background()
	resolver := &fakeResolver{
		idToName: map[int64]string{50: "TestSensor_A"},
		nameToID: map[string]int64{"TestSensor_A": 50},
	}

	store, err := New(ctx, Config{DSN: dsn, Resolver: resolver})
	if err != nil {
		t.Fatalf("clickhouse.New error: %v", err)
	}

	// Close should not panic
	store.Close()
}

func TestCloseNilConn(t *testing.T) {
	store := &Store{conn: nil}
	// Should not panic on nil conn
	store.Close()
}

type chHistoryRow struct {
	name  string
	ts    time.Time
	value float64
}

func setupClickhouseFixtures(t *testing.T, ctx context.Context, dsn string, rows []chHistoryRow) {
	t.Helper()

	resolver := &fakeResolver{}
	store, err := New(ctx, Config{DSN: dsn, Resolver: resolver})
	if err != nil {
		t.Fatalf("clickhouse.New for setup: %v", err)
	}

	// Create test table
	createTable := `
CREATE TABLE IF NOT EXISTS tm_test_history (
    timestamp DateTime64(9,'UTC') DEFAULT now(),
    value Float64,
    name LowCardinality(String),
    nodename LowCardinality(String),
    producer LowCardinality(String)
) ENGINE = MergeTree
ORDER BY (timestamp, name)`
	if err := store.conn.Exec(ctx, createTable); err != nil {
		store.Close()
		t.Fatalf("create test table: %v", err)
	}

	// Truncate existing data
	if err := store.conn.Exec(ctx, "TRUNCATE TABLE tm_test_history"); err != nil {
		store.Close()
		t.Fatalf("truncate test table: %v", err)
	}

	// Insert test data
	batch, err := store.conn.PrepareBatch(ctx, "INSERT INTO tm_test_history (timestamp, value, name, nodename, producer)")
	if err != nil {
		store.Close()
		t.Fatalf("prepare batch: %v", err)
	}
	for _, row := range rows {
		if err := batch.Append(row.ts, row.value, row.name, "testnode", "testproducer"); err != nil {
			store.Close()
			t.Fatalf("append row: %v", err)
		}
	}
	if err := batch.Send(); err != nil {
		store.Close()
		t.Fatalf("send batch: %v", err)
	}
	store.Close()

	t.Cleanup(func() {
		ctxCleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cleanStore, err := New(ctxCleanup, Config{DSN: dsn, Resolver: &fakeResolver{}})
		if err != nil {
			return
		}
		_ = cleanStore.conn.Exec(ctxCleanup, "TRUNCATE TABLE tm_test_history")
		cleanStore.Close()
	})
}
