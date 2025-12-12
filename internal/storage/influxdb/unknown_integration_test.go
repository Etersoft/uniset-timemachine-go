package influxdb

import (
	"context"
	"os"
	"testing"
	"time"

	client "github.com/influxdata/influxdb1-client/v2"

	"github.com/pv/uniset-timemachine-go/pkg/config"
)

type regResolver struct{ r *config.SensorRegistry }

func (rr regResolver) NameByHash(hash int64) (string, bool) {
	if k, ok := rr.r.ByHash(hash); ok {
		return k.Name, true
	}
	return "", false
}

func (rr regResolver) HashByName(name string) (int64, bool) {
	if k, ok := rr.r.ByName(name); ok {
		return k.Hash, true
	}
	return 0, false
}

// Integration test for InfluxDB RangeWithUnknown. Unknown is always 0 for Influx backend.
// Requires env INFLUX_TEST_DSN.
func TestRangeWithUnknown_Influx(t *testing.T) {
	dsn := os.Getenv("INFLUX_TEST_DSN")
	if dsn == "" {
		t.Skip("INFLUX_TEST_DSN is not set; skipping integration test")
	}
	ctx := context.Background()

	reg := config.NewSensorRegistry()
	if err := reg.Add(config.NewSensorKey("S1", nil)); err != nil {
		t.Fatalf("registry add: %v", err)
	}
	if err := reg.Add(config.NewSensorKey("S2", nil)); err != nil {
		t.Fatalf("registry add: %v", err)
	}

	store, err := New(ctx, Config{DSN: dsn, Resolver: regResolver{reg}})
	if err != nil {
		t.Fatalf("influx.New: %v", err)
	}
	defer store.Close()

	// Write three points: S1, S2 (known) and S3 (unknown). Unknown should still be 0.
	bp, err := client.NewBatchPoints(client.BatchPointsConfig{Database: store.database})
	if err != nil {
		t.Fatalf("batch points: %v", err)
	}
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	for i, name := range []string{"S1", "S2", "S3"} {
		pt, err := client.NewPoint(name, nil, map[string]interface{}{"value": float64(10 * (i + 1))}, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatalf("new point: %v", err)
		}
		bp.AddPoint(pt)
	}
	if err := store.client.Write(bp); err != nil {
		t.Fatalf("write points: %v", err)
	}

	h1 := config.HashForName("S1")
	h2 := config.HashForName("S2")
	_, _, known, unknown, err := store.RangeWithUnknown(ctx, []int64{h1, h2}, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("RangeWithUnknown: %v", err)
	}
	if known != 2 {
		t.Fatalf("known=%d want=2", known)
	}
	if unknown != 0 {
		t.Fatalf("unknown=%d want=0 (Influx backend reports 0)", unknown)
	}
}
