package clickhouse

import (
	"context"
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
		{"clickhouse://localhost:9000/db", true},
		{"ch://localhost", true},
		{"postgres://localhost/db", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsSource(tt.dsn); got != tt.want {
			t.Fatalf("IsSource(%q) = %v, want %v", tt.dsn, got, tt.want)
		}
	}
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
