package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/pv/uniset-timemachine-go/pkg/config"
)

// Проверяем, что RangeWithUnknown в SQLite возвращает количество датчиков,
// отсутствующих в рабочем списке/реестре.
func TestRangeWithUnknown_SQLite(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "unknown.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Добавим три датчика: два известных (id=1,2) и один неизвестный (id=3).
	if _, err := db.Exec(`
CREATE TABLE main_history (
	sensor_id INTEGER NOT NULL,
	timestamp TEXT NOT NULL,
	time_usec INTEGER,
	value REAL NOT NULL
);
INSERT INTO main_history(sensor_id, timestamp, value) VALUES
 (1, '2024-06-01T00:00:00Z', 10.0),
 (2, '2024-06-01T00:00:01Z', 20.0),
 (3, '2024-06-01T00:00:02Z', 30.0);
`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reg := config.NewSensorRegistry()
	id1, id2 := int64(1), int64(2)
	if err := reg.Add(config.NewSensorKey("S1", &id1)); err != nil {
		t.Fatalf("registry add: %v", err)
	}
	if err := reg.Add(config.NewSensorKey("S2", &id2)); err != nil {
		t.Fatalf("registry add: %v", err)
	}

	store, err := New(ctx, Config{Source: dbPath, Registry: reg})
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	defer store.Close()

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
		t.Fatalf("expected min/max to be set")
	}
}
