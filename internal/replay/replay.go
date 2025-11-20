package replay

import (
	"context"
	"fmt"
	"time"

	"github.com/pv/uniset-timemachine-go/internal/sharedmem"
	"github.com/pv/uniset-timemachine-go/internal/storage"
)

// Params описывает настройки воспроизведения.
type Params struct {
	Sensors   []int64
	From      time.Time
	To        time.Time
	Step      time.Duration
	Window    time.Duration
	Speed     float64
	BatchSize int
}

// Service связывает storage и sharedmem.
type Service struct {
	Storage storage.Storage
	Output  sharedmem.Client
}

// Run запускает цикл воспроизведения.
func (s *Service) Run(ctx context.Context, params Params) error {
	if s.Storage == nil || s.Output == nil {
		return fmt.Errorf("replay: storage and output must be set")
	}
	if params.Step <= 0 {
		return fmt.Errorf("replay: step must be > 0")
	}
	if !params.To.After(params.From) {
		return fmt.Errorf("replay: invalid period: %s → %s", params.From, params.To)
	}

	state := make(map[int64]*sensorState, len(params.Sensors))
	for _, id := range params.Sensors {
		state[id] = &sensorState{}
	}

	warmupEvents, err := s.Storage.Warmup(ctx, params.Sensors, params.From)
	if err != nil {
		return fmt.Errorf("replay: warmup: %w", err)
	}
	applyEvents(state, warmupEvents, true)

	dataCh, errCh := s.Storage.Stream(ctx, storage.StreamRequest{
		Sensors: params.Sensors,
		From:    params.From,
		To:      params.To,
		Window:  params.Window,
	})

	eventCh, streamErr := fanInEvents(ctx, dataCh, errCh)

	stepTs := params.From
	var stepID int64
	pending := make([]storage.SensorEvent, 0, 128)

	for stepTs.Before(params.To) {
		stepID++
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		pending, _ = drainEvents(eventCh, pending)
		pending = applyPending(state, pending, stepTs)

		updates := collectUpdates(state)
		if len(updates) > 0 {
			batchSize := params.BatchSize
			if batchSize <= 0 || batchSize > len(updates) {
				batchSize = len(updates)
			}
			total := (len(updates) + batchSize - 1) / batchSize
			for i := 0; i < total; i++ {
				start := i * batchSize
				end := start + batchSize
				if end > len(updates) {
					end = len(updates)
				}
				payload := sharedmem.StepPayload{
					StepID:     stepID,
					StepTs:     stepTs.Format(time.RFC3339),
					BatchID:    i + 1,
					BatchTotal: total,
					Updates:    updates[start:end],
				}
				if err := s.Output.Send(ctx, payload); err != nil {
					return err
				}
			}
		}

		if err := waitNextStep(ctx, params.Step, params.Speed); err != nil {
			return err
		}
		stepTs = stepTs.Add(params.Step)

		select {
		case err := <-streamErr:
			if err != nil {
				return err
			}
		default:
		}
	}

	select {
	case err := <-streamErr:
		if err != nil {
			return err
		}
	default:
	}
	return nil
}

type sensorState struct {
	value    float64
	hasValue bool
	dirty    bool
}

func applyEvents(state map[int64]*sensorState, events []storage.SensorEvent, markDirty bool) {
	for _, ev := range events {
		st := state[ev.SensorID]
		if st == nil {
			st = &sensorState{}
			state[ev.SensorID] = st
		}
		st.value = ev.Value
		st.hasValue = true
		if markDirty {
			st.dirty = true
		}
	}
}

func fanInEvents(ctx context.Context, dataCh <-chan []storage.SensorEvent, errCh <-chan error) (<-chan storage.SensorEvent, <-chan error) {
	eventCh := make(chan storage.SensorEvent, 1024)
	streamErr := make(chan error, 1)

	go func() {
		defer close(eventCh)
		for {
			if dataCh == nil && errCh == nil {
				return
			}
			select {
			case batch, ok := <-dataCh:
				if !ok {
					dataCh = nil
					if errCh == nil {
						return
					}
					continue
				}
				for _, ev := range batch {
					select {
					case eventCh <- ev:
					case <-ctx.Done():
						return
					}
				}
			case err, ok := <-errCh:
				if !ok {
					errCh = nil
					continue
				}
				if err != nil {
					streamErr <- err
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return eventCh, streamErr
}

func drainEvents(eventCh <-chan storage.SensorEvent, pending []storage.SensorEvent) ([]storage.SensorEvent, bool) {
	for {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				return pending, true
			}
			pending = append(pending, ev)
		default:
			return pending, false
		}
	}
}

func applyPending(state map[int64]*sensorState, pending []storage.SensorEvent, cutoff time.Time) []storage.SensorEvent {
	idx := 0
	for idx < len(pending) && !pending[idx].Timestamp.After(cutoff) {
		ev := pending[idx]
		st := state[ev.SensorID]
		if st == nil {
			st = &sensorState{}
			state[ev.SensorID] = st
		}
		st.value = ev.Value
		st.hasValue = true
		st.dirty = true
		idx++
	}
	if idx == 0 {
		return pending
	}
	copy(pending, pending[idx:])
	return pending[:len(pending)-idx]
}

func collectUpdates(state map[int64]*sensorState) []sharedmem.SensorUpdate {
	updates := make([]sharedmem.SensorUpdate, 0)
	for id, st := range state {
		if st.dirty && st.hasValue {
			updates = append(updates, sharedmem.SensorUpdate{
				ID:    id,
				Value: st.value,
			})
			st.dirty = false
		}
	}
	return updates
}

func waitNextStep(ctx context.Context, step time.Duration, speed float64) error {
	if step <= 0 {
		return nil
	}
	if speed <= 0 {
		speed = 1
	}
	delay := time.Duration(float64(step) / speed)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
