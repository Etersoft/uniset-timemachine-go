package memstore

import (
	"context"
	"time"

	"github.com/pv/uniset-timemachine-go/internal/storage"
)

// ExampleStore генерирует детерминированные значения для заданных датчиков.
type ExampleStore struct {
	sensors []int64
	from    time.Time
	to      time.Time
	step    time.Duration
}

func NewExampleStore(sensors []int64, from, to time.Time, step time.Duration) *ExampleStore {
	if from.IsZero() {
		from = time.Now().Add(-time.Hour)
	}
	if to.IsZero() || !to.After(from) {
		to = from.Add(30 * time.Minute)
	}
	if step <= 0 {
		step = time.Second
	}
	return &ExampleStore{
		sensors: append([]int64(nil), sensors...),
		from:    from,
		to:      to,
		step:    step,
	}
}

func (s *ExampleStore) Warmup(ctx context.Context, sensors []int64, from time.Time) ([]storage.SensorEvent, error) {
	events := make([]storage.SensorEvent, 0, len(sensors))
	for _, id := range sensors {
		events = append(events, storage.SensorEvent{
			SensorID:  id,
			Timestamp: from.Add(-s.step),
			Value:     float64(id % 10),
		})
	}
	return events, ctx.Err()
}

func (s *ExampleStore) Stream(ctx context.Context, req storage.StreamRequest) (<-chan []storage.SensorEvent, <-chan error) {
	dataCh := make(chan []storage.SensorEvent)
	errCh := make(chan error, 1)

	go func() {
		defer close(dataCh)
		defer close(errCh)

		for ts := s.from; ts.Before(s.to); ts = ts.Add(s.step) {
			chunk := make([]storage.SensorEvent, 0, len(s.sensors))
			for _, id := range s.sensors {
				val := float64(id%100) + float64(ts.Second())
				chunk = append(chunk, storage.SensorEvent{
					SensorID:  id,
					Timestamp: ts,
					Value:     val,
				})
			}
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			case dataCh <- chunk:
			}
		}
	}()

	return dataCh, errCh
}

func (s *ExampleStore) Range(ctx context.Context, sensors []int64, from, to time.Time) (time.Time, time.Time, int64, error) {
	return s.from, s.to, int64(len(sensors)), nil
}
