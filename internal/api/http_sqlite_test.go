package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/pv/uniset-timemachine-go/internal/replay"
	"github.com/pv/uniset-timemachine-go/internal/sharedmem"
	sqliteStore "github.com/pv/uniset-timemachine-go/internal/storage/sqlite"
)

// Интеграционный тест HTTP API с настоящим SQLite.
// Если таблица отсутствует или пуста, создаём её и генерируем минимальный набор данных.
func TestHTTPAPIWithSQLiteAutoSeed(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "api-sqlite.db")
	seedSQLiteIfEmpty(t, dbPath)

	store, err := sqliteStore.New(ctx, sqliteStore.Config{Source: dbPath})
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(store.Close)

	output := &captureClient{}
	svc := replay.Service{
		Storage: store,
		Output:  output,
	}
	mgr := NewManager(svc, []int64{10001, 10002}, nil, 50, 2*time.Second, 16, nil, true, false)
	srv := NewServer(mgr, nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("skip: tcp listen not permitted: %v", err)
	}
	ts := httptest.NewUnstartedServer(srv.mux)
	ts.Listener = ln
	ts.Start()
	defer ts.Close()

	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(3 * time.Second)
	body := map[string]any{
		"from":   from.Format(time.RFC3339),
		"to":     to.Format(time.RFC3339),
		"step":   "1s",
		"speed":  50.0,
		"window": "2s",
		"save_output": true,
	}
	postJSON(t, ts.URL+"/api/v2/job/range", body)
	if resp := postJSON(t, ts.URL+"/api/v2/job/start", map[string]any{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("start job status = %d, want 200", resp.StatusCode)
	}

	status := waitForJobFinish(t, ts.URL, 5*time.Second)
	if status.Status != "done" {
		t.Fatalf("job status = %q, want done (err=%s)", status.Status, status.Error)
	}
	if status.StepID == 0 {
		t.Fatalf("expected progress in job, got step_id=0")
	}

	payloads := output.Payloads()
	if len(payloads) == 0 {
		t.Fatalf("no payloads were sent to output client")
	}
}

func seedSQLiteIfEmpty(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	defer db.Close()

	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='main_history'`).Scan(&tables); err != nil {
		t.Fatalf("check sqlite schema: %v", err)
	}
	if tables == 0 {
		schema := `
CREATE TABLE main_history(
	sensor_id INTEGER NOT NULL,
	timestamp TEXT NOT NULL,
	time_usec INTEGER,
	value REAL NOT NULL
);`
		if _, err := db.Exec(schema); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM main_history`).Scan(&count); err != nil {
		t.Fatalf("count main_history: %v", err)
	}
	if count > 0 {
		return
	}

	start := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	rows := []struct {
		sensorID int64
		ts       time.Time
		usec     int64
		value    float64
	}{
		{sensorID: 10001, ts: start, value: 10},
		{sensorID: 10002, ts: start, value: 20},
		{sensorID: 10001, ts: start.Add(1 * time.Second), usec: 500000, value: 11},
		{sensorID: 10002, ts: start.Add(2 * time.Second), value: 22},
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO main_history(sensor_id, timestamp, time_usec, value) VALUES (?, ?, ?, ?)`)
	if err != nil {
		t.Fatalf("prepare insert: %v", err)
	}
	defer stmt.Close()

	for _, row := range rows {
		if _, err := stmt.Exec(row.sensorID, row.ts.Format(time.RFC3339), row.usec, row.value); err != nil {
			t.Fatalf("insert row: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}
}

func waitForJobFinish(t *testing.T, baseURL string, timeout time.Duration) Status {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/v2/job")
		if err != nil {
			t.Fatalf("get job status: %v", err)
		}
		var st Status
		if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
			resp.Body.Close()
			t.Fatalf("decode status: %v", err)
		}
		resp.Body.Close()
		if st.Status == "done" || st.Status == "failed" {
			return st
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("job did not finish within %s", timeout)
	return Status{}
}

type captureClient struct {
	mu       sync.Mutex
	payloads []sharedmem.StepPayload
}

func (c *captureClient) Send(_ context.Context, payload sharedmem.StepPayload) error {
	c.mu.Lock()
	c.payloads = append(c.payloads, payload)
	c.mu.Unlock()
	return nil
}

func (c *captureClient) Payloads() []sharedmem.StepPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]sharedmem.StepPayload, len(c.payloads))
	copy(out, c.payloads)
	return out
}
