package memstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pv/uniset-timemachine-go/internal/storage"
)

func TestExampleStoreWarmupStreamAndRange(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 5, 0, time.UTC)
	end := start.Add(3 * time.Second)
	store := NewExampleStore([]int64{1, 2}, start, end, time.Second)

	events, err := store.Warmup(context.Background(), []int64{1, 2}, start)
	if err != nil {
		t.Fatalf("Warmup returned error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 warmup events, got %d", len(events))
	}
	if events[0].Timestamp != start.Add(-time.Second) {
		t.Fatalf("unexpected warmup timestamp: %s", events[0].Timestamp)
	}
	if events[0].Value != float64(1%10) {
		t.Fatalf("unexpected warmup value: %v", events[0].Value)
	}

	dataCh, errCh := store.Stream(context.Background(), storage.StreamRequest{})
	var batches [][]storage.SensorEvent
	for batch := range dataCh {
		batches = append(batches, batch)
	}
	if err, ok := <-errCh; ok && err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(batches))
	}
	firstTs := batches[0][0].Timestamp
	if firstTs != start {
		t.Fatalf("unexpected first timestamp: %s", firstTs)
	}
	if batches[0][0].Value != 1+float64(start.Second()) {
		t.Fatalf("unexpected value for sensor 1: %v", batches[0][0].Value)
	}

	from, to, _, err := store.Range(context.Background(), []int64{1, 2}, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Range returned error: %v", err)
	}
	if from != start || to != end {
		t.Fatalf("unexpected range: %s %s", from, to)
	}
}

func TestExampleStoreStreamContextCancel(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	store := NewExampleStore([]int64{10}, start, start.Add(5*time.Second), time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	dataCh, errCh := store.Stream(ctx, storage.StreamRequest{})

	if _, ok := <-dataCh; !ok {
		t.Fatalf("data channel closed too early")
	}
	cancel()

	for range dataCh {
	}
	err, ok := <-errCh
	if !ok {
		t.Fatalf("err channel closed without value")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
