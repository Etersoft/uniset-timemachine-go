package clickhouse

import (
	"testing"
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
