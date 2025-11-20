package replay

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/pv/uniset-timemachine-go/internal/sharedmem"
	"github.com/pv/uniset-timemachine-go/internal/storage"
)

type fakeStorage struct {
	warmup  []storage.SensorEvent
	batches [][]storage.SensorEvent
}

func (f *fakeStorage) Warmup(context.Context, []int64, time.Time) ([]storage.SensorEvent, error) {
	return f.warmup, nil
}

func (f *fakeStorage) Stream(context.Context, storage.StreamRequest) (<-chan []storage.SensorEvent, <-chan error) {
	dataCh := make(chan []storage.SensorEvent, len(f.batches))
	errCh := make(chan error, 1)
	go func() {
		defer close(dataCh)
		defer close(errCh)
		for _, b := range f.batches {
			dataCh <- b
		}
	}()
	return dataCh, errCh
}

func (f *fakeStorage) Range(context.Context, []int64) (time.Time, time.Time, error) {
	return time.Time{}, time.Time{}, nil
}

type fakeClient struct {
	payloads []sharedmem.StepPayload
}

func (c *fakeClient) Send(_ context.Context, payload sharedmem.StepPayload) error {
	c.payloads = append(c.payloads, payload)
	return nil
}

func TestServiceRunBatchesUpdates(t *testing.T) {
	start := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	st := &fakeStorage{
		warmup: []storage.SensorEvent{
			{SensorID: 1, Timestamp: start.Add(-time.Second), Value: 100},
		},
		batches: [][]storage.SensorEvent{
			{
				{SensorID: 2, Timestamp: start, Value: 50},
				{SensorID: 1, Timestamp: start.Add(time.Second), Value: 101},
			},
			{
				{SensorID: 2, Timestamp: start.Add(time.Second), Value: 55},
			},
		},
	}

	client := &fakeClient{}
	svc := Service{
		Storage: st,
		Output:  client,
	}
	params := Params{
		Sensors:   []int64{1, 2},
		From:      start,
		To:        start.Add(3 * time.Second),
		Step:      time.Second,
		Window:    time.Minute,
		Speed:     10.0,
		BatchSize: 1,
	}
	if err := svc.Run(context.Background(), params); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if len(client.payloads) != 3 {
		t.Fatalf("expected 3 payloads, got %d: %#v", len(client.payloads), client.payloads)
	}

	checkStep := func(stepID int64, expected map[int64]float64) {
		items := make(map[int64]float64)
		for _, p := range client.payloads {
			if p.StepID != stepID {
				continue
			}
			if p.BatchTotal != len(expected) {
				t.Fatalf("step %d batch total mismatch: %d", stepID, p.BatchTotal)
			}
			for _, upd := range p.Updates {
				items[upd.ID] = upd.Value
			}
		}
		if len(items) != len(expected) {
			t.Fatalf("step %d expected %d updates, got %d", stepID, len(expected), len(items))
		}
		for id, val := range expected {
			if got, ok := items[id]; !ok || got != val {
				t.Fatalf("step %d sensor %d mismatch: got %v want %v", stepID, id, got, val)
			}
		}
	}

	checkStep(1, map[int64]float64{1: 100})
	checkStep(2, map[int64]float64{1: 101, 2: 55})

	var stepIDs []int64
	for _, p := range client.payloads {
		stepIDs = append(stepIDs, p.StepID)
	}
	sort.Slice(stepIDs, func(i, j int) bool { return stepIDs[i] < stepIDs[j] })
	if !reflect.DeepEqual(stepIDs, []int64{1, 2, 2}) {
		t.Fatalf("unexpected step/order: %v", stepIDs)
	}
}
