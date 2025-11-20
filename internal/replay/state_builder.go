package replay

import (
	"context"
	"fmt"
	"time"

	"github.com/pv/uniset-timemachine-go/internal/storage"
)

// StateSnapshot содержит вычисленное состояние на момент StepTs.
type StateSnapshot struct {
	StepID int64
	StepTs time.Time
	Values map[int64]float64
}

// BuildState рассчитывает состояние датчиков на указанный момент времени, не выполняя отправку.
func BuildState(ctx context.Context, store storage.Storage, params Params, target time.Time) (StateSnapshot, error) {
	if !params.To.IsZero() && target.After(params.To) {
		return StateSnapshot{}, fmt.Errorf("replay: target %s is after params.To %s", target, params.To)
	}
	if params.Step <= 0 {
		return StateSnapshot{}, fmt.Errorf("replay: step must be > 0")
	}
	state := make(map[int64]*sensorState, len(params.Sensors))
	for _, id := range params.Sensors {
		state[id] = &sensorState{}
	}

	warm, err := store.Warmup(ctx, params.Sensors, target)
	if err != nil {
		return StateSnapshot{}, fmt.Errorf("replay: warmup: %w", err)
	}
	applyEvents(state, warm, true)

	req := storage.StreamRequest{
		Sensors: params.Sensors,
		From:    params.From,
		To:      target,
		Window:  params.Window,
	}
	dataCh, errCh := store.Stream(ctx, req)
	eventCh, streamErr := fanInEvents(ctx, dataCh, errCh)

	stepTs := params.From
	var stepID int64
	pending := make([]storage.SensorEvent, 0, 128)

	for !stepTs.After(target) {
		stepID++
		if ctx.Err() != nil {
			return StateSnapshot{}, ctx.Err()
		}

		pending, _ = drainEvents(eventCh, pending)
		pending = applyPending(state, pending, stepTs)

		if stepTs.Equal(target) {
			break
		}
		stepTs = stepTs.Add(params.Step)
	}

	select {
	case err := <-streamErr:
		if err != nil {
			return StateSnapshot{}, err
		}
	default:
	}

	values := make(map[int64]float64, len(state))
	for id, st := range state {
		if st.hasValue {
			values[id] = st.value
		}
	}

	return StateSnapshot{
		StepID: stepID,
		StepTs: stepTs,
		Values: values,
	}, nil
}
