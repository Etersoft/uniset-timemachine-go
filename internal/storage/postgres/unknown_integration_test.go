package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/pv/uniset-timemachine-go/pkg/config"
)

// Integration test for RangeWithUnknown in Postgres.
// Requires env POSTGRES_TEST_DSN pointing to a writable test database.
func TestRangeWithUnknown_Postgres(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set; skipping integration test")
	}
	ctx := context.Background()

	reg := config.NewSensorRegistry()
	id1, id2 := int64(1), int64(2)
	if err := reg.Add(config.NewSensorKey("S1", &id1)); err != nil {
		t.Fatalf("registry add: %v", err)
	}
	if err := reg.Add(config.NewSensorKey("S2", &id2)); err != nil {
		t.Fatalf("registry add: %v", err)
	}

	store, err := New(ctx, Config{ConnString: dsn, Registry: reg})
	if err != nil {
		t.Fatalf("postgres.New: %v", err)
	}
	defer store.Close()

	// Create temp table and seed data: known IDs 1,2 and unknown 3.
	conn, err := store.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `
DROP TABLE IF EXISTS main_history;
CREATE TABLE main_history(
  sensor_id BIGINT NOT NULL,
  date DATE NOT NULL,
  time TIME NOT NULL,
  time_usec INT,
  value DOUBLE PRECISION NOT NULL
);`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	_, err = conn.Exec(ctx, `
INSERT INTO main_history(sensor_id, date, time, time_usec, value) VALUES
 (1, $1::date, $2::time, 0, 10.0),
 (2, $1::date, $3::time, 0, 20.0),
 (3, $1::date, $4::time, 0, 30.0);`,
		now.Format("2006-01-02"), "00:00:00", "00:00:01", "00:00:02")
	if err != nil {
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
