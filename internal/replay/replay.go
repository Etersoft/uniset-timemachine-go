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
	return s.run(ctx, params, nil)
}

// RunWithControl запускает цикл воспроизведения с возможностью паузы/шагов.
func (s *Service) RunWithControl(ctx context.Context, params Params, ctrl Control) error {
	return s.run(ctx, params, &ctrl)
}

func (s *Service) run(ctx context.Context, params Params, ctrl *Control) error {
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

	streamCtx, streamCancel := context.WithCancel(ctx)
	defer func() {
		if streamCancel != nil {
			streamCancel()
		}
	}()
	dataCh, errCh := s.Storage.Stream(streamCtx, storage.StreamRequest{
		Sensors: params.Sensors,
		From:    params.From,
		To:      params.To,
		Window:  params.Window,
	})

	eventCh, streamErr := fanInEvents(streamCtx, dataCh, errCh)

	stepTs := params.From
	var stepID int64
	pending := make([]storage.SensorEvent, 0, 128)
	paused := false
	stepOnce := false

	for stepTs.Before(params.To) {
		stepID++
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if ctrl != nil {
			if err := handleCommands(ctx, s, params, ctrl, &state, &stepTs, &stepID, &streamCancel, &eventCh, &streamErr, &pending, &paused, &stepOnce); err != nil {
				return err
			}
		}

		if paused {
			if ctrl != nil {
				if err := waitWhilePaused(ctx, s, params, ctrl, &state, &stepTs, &stepID, &streamCancel, &eventCh, &streamErr, &pending, &paused, &stepOnce); err != nil {
					return err
				}
			}
			continue
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

		if ctrl != nil && ctrl.OnStep != nil {
			ctrl.OnStep(StepInfo{
				StepID:       stepID,
				StepTs:       stepTs,
				UpdatesCount: len(updates),
			})
		}

		if stepOnce {
			paused = true
			stepOnce = false
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

func handleCommands(
	ctx context.Context,
	s *Service,
	params Params,
	ctrl *Control,
	state *map[int64]*sensorState,
	stepTs *time.Time,
	stepID *int64,
	streamCancel *context.CancelFunc,
	eventCh *<-chan storage.SensorEvent,
	streamErr *<-chan error,
	pending *[]storage.SensorEvent,
	paused *bool,
	stepOnce *bool,
) error {
	for {
		select {
		case cmd := <-ctrl.Commands:
			var respErr error
			switch cmd.Type {
			case CommandPause:
				*paused = true
			case CommandResume:
				*paused = false
			case CommandStop:
				respErr = ErrStopped{}
			case CommandStepForward:
				*stepOnce = true
				*paused = false
			case CommandStepBackward:
				target := (*stepTs).Add(-params.Step)
				if target.Before(params.From) {
					target = params.From
				}
				if err := rebuildState(ctx, s, params, target, state); err != nil {
					respErr = err
					break
				}
				if err := restartStream(ctx, s, params, target, streamCancel, eventCh, streamErr, pending); err != nil {
					respErr = err
					break
				}
				*stepTs = target
				if target.Equal(params.From) {
					*stepID = 1
				} else {
					*stepID = int64(target.Sub(params.From)/params.Step) + 1
				}
				*paused = true
				if cmd.Apply {
					if err := sendFullSnapshot(ctx, s, params, *state, stepID, stepTs); err != nil {
						respErr = err
					}
				}
			case CommandSeek:
				if cmd.TS.IsZero() {
					respErr = fmt.Errorf("seek: TS is required")
					break
				}
				if cmd.TS.Before(params.From) || cmd.TS.After(params.To) {
					respErr = fmt.Errorf("seek: target %s is outside range %s-%s", cmd.TS, params.From, params.To)
					break
				}
				if err := rebuildState(ctx, s, params, cmd.TS, state); err != nil {
					respErr = err
					break
				}
				if err := restartStream(ctx, s, params, cmd.TS, streamCancel, eventCh, streamErr, pending); err != nil {
					respErr = err
					break
				}
				*stepTs = cmd.TS
				*stepID = int64(cmd.TS.Sub(params.From)/params.Step) + 1
				*paused = true
				if cmd.Apply {
					if err := sendFullSnapshot(ctx, s, params, *state, stepID, stepTs); err != nil {
						respErr = err
					}
				}
			case CommandApply:
				respErr = sendFullSnapshot(ctx, s, params, *state, stepID, stepTs)
			default:
			}
			if cmd.Resp != nil {
				select {
				case cmd.Resp <- respErr:
				default:
				}
			}
			if respErr != nil {
				return respErr
			}
		default:
			return nil
		}
	}
}

func waitWhilePaused(
	ctx context.Context,
	s *Service,
	params Params,
	ctrl *Control,
	state *map[int64]*sensorState,
	stepTs *time.Time,
	stepID *int64,
	streamCancel *context.CancelFunc,
	eventCh *<-chan storage.SensorEvent,
	streamErr *<-chan error,
	pending *[]storage.SensorEvent,
	paused *bool,
	stepOnce *bool,
) error {
	for *paused {
		p, _ := drainEvents(*eventCh, *pending)
		*pending = p
		select {
		case <-ctx.Done():
			return ctx.Err()
		case cmd := <-ctrl.Commands:
			var respErr error
			switch cmd.Type {
			case CommandResume:
				*paused = false
			case CommandPause:
				// stay paused
			case CommandStop:
				respErr = ErrStopped{}
			case CommandStepForward:
				*stepOnce = true
				*paused = false
			case CommandStepBackward:
				target := (*stepTs).Add(-params.Step)
				if target.Before(params.From) {
					target = params.From
				}
				if err := rebuildState(ctx, s, params, target, state); err != nil {
					respErr = err
					break
				}
				if err := restartStream(ctx, s, params, target, streamCancel, eventCh, streamErr, pending); err != nil {
					respErr = err
					break
				}
				*stepTs = target
				if target.Equal(params.From) {
					*stepID = 1
				} else {
					*stepID = int64(target.Sub(params.From)/params.Step) + 1
				}
				*paused = true
				if cmd.Apply {
					if err := sendFullSnapshot(ctx, s, params, *state, stepID, stepTs); err != nil {
						respErr = err
					}
				}
			case CommandSeek:
				if cmd.TS.IsZero() {
					respErr = fmt.Errorf("seek: TS is required")
					break
				}
				if cmd.TS.Before(params.From) || cmd.TS.After(params.To) {
					respErr = fmt.Errorf("seek: target %s is outside range %s-%s", cmd.TS, params.From, params.To)
					break
				}
				if err := rebuildState(ctx, s, params, cmd.TS, state); err != nil {
					respErr = err
					break
				}
				if err := restartStream(ctx, s, params, cmd.TS, streamCancel, eventCh, streamErr, pending); err != nil {
					respErr = err
					break
				}
				*stepTs = cmd.TS
				*stepID = int64(cmd.TS.Sub(params.From)/params.Step) + 1
				*paused = true
				if cmd.Apply {
					if err := sendFullSnapshot(ctx, s, params, *state, stepID, stepTs); err != nil {
						respErr = err
					}
				}
			case CommandApply:
				respErr = sendFullSnapshot(ctx, s, params, *state, stepID, stepTs)
			}
			if cmd.Resp != nil {
				select {
				case cmd.Resp <- respErr:
				default:
				}
			}
			if respErr != nil {
				return respErr
			}
		default:
		}
	}
	return nil
}

func rebuildState(
	ctx context.Context,
	s *Service,
	params Params,
	target time.Time,
	state *map[int64]*sensorState,
) error {
	snapshot, err := BuildState(ctx, s.Storage, Params{
		Sensors: params.Sensors,
		From:    params.From,
		To:      target,
		Step:    params.Step,
		Window:  params.Window,
	}, target)
	if err != nil {
		return err
	}
	newState := make(map[int64]*sensorState, len(snapshot.Values))
	for _, id := range params.Sensors {
		newState[id] = &sensorState{}
	}
	for id, v := range snapshot.Values {
		newState[id].value = v
		newState[id].hasValue = true
	}
	*state = newState
	return nil
}

func restartStream(
	ctx context.Context,
	s *Service,
	params Params,
	from time.Time,
	streamCancel *context.CancelFunc,
	eventCh *<-chan storage.SensorEvent,
	streamErr *<-chan error,
	pending *[]storage.SensorEvent,
) error {
	if streamCancel != nil && *streamCancel != nil {
		(*streamCancel)()
	}
	streamCtx, cancel := context.WithCancel(ctx)
	*pending = (*pending)[:0]
	dataCh, errCh := s.Storage.Stream(streamCtx, storage.StreamRequest{
		Sensors: params.Sensors,
		From:    from,
		To:      params.To,
		Window:  params.Window,
	})
	*eventCh, *streamErr = fanInEvents(streamCtx, dataCh, errCh)
	*pending = make([]storage.SensorEvent, 0, 128)
	if streamCancel != nil {
		*streamCancel = cancel
	}
	return nil
}

func sendFullSnapshot(ctx context.Context, s *Service, params Params, state map[int64]*sensorState, stepID *int64, stepTs *time.Time) error {
	updates := make([]sharedmem.SensorUpdate, 0, len(state))
	for id, st := range state {
		if st.hasValue {
			updates = append(updates, sharedmem.SensorUpdate{ID: id, Value: st.value})
		}
	}
	if len(updates) == 0 {
		return nil
	}
	*stepID++
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
			StepID:     *stepID,
			StepTs:     stepTs.Format(time.RFC3339),
			BatchID:    i + 1,
			BatchTotal: total,
			Updates:    updates[start:end],
		}
		if err := s.Output.Send(ctx, payload); err != nil {
			return err
		}
	}
	return nil
}
