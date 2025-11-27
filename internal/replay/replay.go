package replay

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/pv/uniset-timemachine-go/internal/sharedmem"
	"github.com/pv/uniset-timemachine-go/internal/storage"
)

// Params описывает настройки воспроизведения.
type Params struct {
	Sensors    []int64
	From       time.Time
	To         time.Time
	Step       time.Duration
	Window     time.Duration
	Speed      float64
	BatchSize  int
	SaveOutput bool `json:"save_output,omitempty"`
}

// Service связывает storage и sharedmem.
type Service struct {
	Storage  storage.Storage
	Output   sharedmem.Client
	LogCache bool
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

	saveOutput := params.SaveOutput
	state := make(map[int64]*sensorState, len(params.Sensors))
	for _, id := range params.Sensors {
		state[id] = &sensorState{}
	}

	warmupEvents, err := s.Storage.Warmup(ctx, params.Sensors, params.From)
	if err != nil {
		return fmt.Errorf("replay: warmup: %w", err)
	}
	applyEvents(state, warmupEvents, true)
	cache := newStateCache(16)
	cache.add(params.From, 0, state)

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
			if err := handleCommands(ctx, s, params, ctrl, &saveOutput, &state, &stepTs, &stepID, &streamCancel, &eventCh, &streamErr, &pending, &paused, &stepOnce, cache); err != nil {
				return err
			}
		}

		if paused {
			if ctrl != nil {
				if err := waitWhilePaused(ctx, s, params, ctrl, &saveOutput, &state, &stepTs, &stepID, &streamCancel, &eventCh, &streamErr, &pending, &paused, &stepOnce, cache); err != nil {
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
			if saveOutput {
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
		}

		if ctrl != nil && ctrl.OnUpdates != nil {
			ctrl.OnUpdates(StepInfo{
				StepID:       stepID,
				StepTs:       stepTs,
				UpdatesCount: len(updates),
			}, updates)
		}

		if ctrl != nil && ctrl.OnStep != nil {
			ctrl.OnStep(StepInfo{
				StepID:       stepID,
				StepTs:       stepTs,
				UpdatesCount: len(updates),
			})
		}
		cache.add(stepTs, stepID, state)

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

type cacheEntry struct {
	ts     time.Time
	stepID int64
	state  map[int64]*sensorState
}

type stateCache struct {
	entries []cacheEntry
	limit   int
}

func newStateCache(limit int) *stateCache {
	if limit <= 0 {
		limit = 8
	}
	return &stateCache{limit: limit}
}

func (c *stateCache) add(ts time.Time, stepID int64, src map[int64]*sensorState) {
	if c == nil {
		return
	}
	cloned := cloneState(src)
	c.entries = append(c.entries, cacheEntry{ts: ts, stepID: stepID, state: cloned})
	if len(c.entries) > c.limit {
		c.entries = c.entries[len(c.entries)-c.limit:]
	}
}

func (c *stateCache) get(ts time.Time) (cacheEntry, bool) {
	if c == nil {
		return cacheEntry{}, false
	}
	for i := len(c.entries) - 1; i >= 0; i-- {
		if c.entries[i].ts.Equal(ts) {
			return c.entries[i], true
		}
	}
	return cacheEntry{}, false
}

func (c *stateCache) getLE(ts time.Time) (cacheEntry, bool) {
	if c == nil {
		return cacheEntry{}, false
	}
	for i := len(c.entries) - 1; i >= 0; i-- {
		if !c.entries[i].ts.After(ts) {
			return c.entries[i], true
		}
	}
	return cacheEntry{}, false
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
	saveOutput *bool,
	state *map[int64]*sensorState,
	stepTs *time.Time,
	stepID *int64,
	streamCancel *context.CancelFunc,
	eventCh *<-chan storage.SensorEvent,
	streamErr *<-chan error,
	pending *[]storage.SensorEvent,
	paused *bool,
	stepOnce *bool,
	cache *stateCache,
) error {
	for {
		select {
		case cmd := <-ctrl.Commands:
			cmdTS := ""
			if !cmd.TS.IsZero() {
				cmdTS = cmd.TS.Format(time.RFC3339)
			}
			log.Printf("[replay] handling %v apply=%t ts=%s paused=%v", cmd.Type, cmd.Apply, cmdTS, *paused)
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
				if err := restoreState(ctx, s, params, target, state, stepTs, stepID, streamCancel, eventCh, streamErr, pending, cache); err != nil {
					respErr = err
					break
				}
				notifyOnStep(ctrl, *stepID, *stepTs, 0)
				*paused = true
				if cmd.Apply {
					if err := sendFullSnapshot(ctx, s, params, *state, stepID, stepTs, *saveOutput); err != nil {
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
				if err := restoreState(ctx, s, params, cmd.TS, state, stepTs, stepID, streamCancel, eventCh, streamErr, pending, cache); err != nil {
					respErr = err
					break
				}
				notifyOnStep(ctrl, *stepID, *stepTs, 0)
				*paused = true
				if cmd.Apply {
					if err := sendFullSnapshot(ctx, s, params, *state, stepID, stepTs, *saveOutput); err != nil {
						respErr = err
					}
				}
			case CommandSaveOutput:
				*saveOutput = cmd.SaveOutput
			case CommandApply:
				respErr = sendFullSnapshot(ctx, s, params, *state, stepID, stepTs, *saveOutput)
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
	saveOutput *bool,
	state *map[int64]*sensorState,
	stepTs *time.Time,
	stepID *int64,
	streamCancel *context.CancelFunc,
	eventCh *<-chan storage.SensorEvent,
	streamErr *<-chan error,
	pending *[]storage.SensorEvent,
	paused *bool,
	stepOnce *bool,
	cache *stateCache,
) error {
	evCh := *eventCh
	errCh := *streamErr

	handleCommand := func(cmd Command) error {
		var respErr error
		switch cmd.Type {
		case CommandResume:
			*paused = false
		case CommandPause:
			// already paused
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
			if err := restoreState(ctx, s, params, target, state, stepTs, stepID, streamCancel, eventCh, streamErr, pending, cache); err != nil {
				respErr = err
				break
			}
			evCh = *eventCh
			errCh = *streamErr
			notifyOnStep(ctrl, *stepID, *stepTs, 0)
			*paused = true
			if cmd.Apply {
				if err := sendFullSnapshot(ctx, s, params, *state, stepID, stepTs, *saveOutput); err != nil {
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
			if err := restoreState(ctx, s, params, cmd.TS, state, stepTs, stepID, streamCancel, eventCh, streamErr, pending, cache); err != nil {
				respErr = err
				break
			}
			evCh = *eventCh
			errCh = *streamErr
			notifyOnStep(ctrl, *stepID, *stepTs, 0)
			*paused = true
			if cmd.Apply {
				if err := sendFullSnapshot(ctx, s, params, *state, stepID, stepTs, *saveOutput); err != nil {
					respErr = err
				}
			}
		case CommandSaveOutput:
			*saveOutput = cmd.SaveOutput
		case CommandApply:
			respErr = sendFullSnapshot(ctx, s, params, *state, stepID, stepTs, *saveOutput)
		}
		if cmd.Resp != nil {
			select {
			case cmd.Resp <- respErr:
			default:
			}
		}
		return respErr
	}

	for *paused {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case cmd := <-ctrl.Commands:
			if err := handleCommand(cmd); err != nil {
				return err
			}
			evCh = *eventCh
			errCh = *streamErr
		case ev, ok := <-evCh:
			if !ok {
				evCh = nil
				continue
			}
			*pending = append(*pending, ev)
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err != nil {
				return err
			}
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
	*state = snapshotToState(params.Sensors, snapshot.Values)
	return nil
}

func fastForwardFromCache(
	ctx context.Context,
	s *Service,
	params Params,
	target time.Time,
	state *map[int64]*sensorState,
	stepTs *time.Time,
	stepID *int64,
) error {
	if target.Before(*stepTs) {
		return fmt.Errorf("target %s is before cached state %s", target, *stepTs)
	}
	if target.Equal(*stepTs) {
		return nil
	}
	if !target.After(params.From) {
		return nil
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	fromTs := stepTs.Add(time.Nanosecond)
	dataCh, errCh := s.Storage.Stream(streamCtx, storage.StreamRequest{
		Sensors: params.Sensors,
		From:    fromTs,
		To:      target,
		Window:  params.Window,
	})
	eventCh, streamErr := fanInEvents(streamCtx, dataCh, errCh)

	pending := make([]storage.SensorEvent, 0, 128)
	// Собираем события до закрытия/до первой порции, без busy-loop.
	closed := false
	for !closed && len(pending) == 0 {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				closed = true
				eventCh = nil
				continue
			}
			pending = append(pending, ev)
		case err := <-streamErr:
			if err != nil {
				return err
			}
			streamErr = nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Дальше добираем всё, что успело прилететь.
	for {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				closed = true
				eventCh = nil
				continue
			}
			pending = append(pending, ev)
		case err := <-streamErr:
			if err != nil {
				return err
			}
			streamErr = nil
		default:
			goto collected
		}
	}

collected:

	curTs := *stepTs

	for curTs.Before(target) {
		curTs = curTs.Add(params.Step)
		pending = applyPending(*state, pending, curTs)
	}

	select {
	case err := <-streamErr:
		if err != nil {
			return err
		}
	default:
	}

	*stepTs = curTs
	*stepID = int64(curTs.Sub(params.From)/params.Step) + 1
	return nil
}

func snapshotToState(ids []int64, values map[int64]float64) map[int64]*sensorState {
	newState := make(map[int64]*sensorState, len(ids))
	for _, id := range ids {
		newState[id] = &sensorState{}
	}
	for id, v := range values {
		st := newState[id]
		if st == nil {
			st = &sensorState{}
			newState[id] = st
		}
		st.value = v
		st.hasValue = true
	}
	return newState
}

func cloneState(src map[int64]*sensorState) map[int64]*sensorState {
	dst := make(map[int64]*sensorState, len(src))
	for id, st := range src {
		if st == nil {
			continue
		}
		dst[id] = &sensorState{value: st.value, hasValue: st.hasValue}
	}
	return dst
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

func restoreState(
	ctx context.Context,
	s *Service,
	params Params,
	target time.Time,
	state *map[int64]*sensorState,
	stepTs *time.Time,
	stepID *int64,
	streamCancel *context.CancelFunc,
	eventCh *<-chan storage.SensorEvent,
	streamErr *<-chan error,
	pending *[]storage.SensorEvent,
	cache *stateCache,
) error {
	if entry, ok := cache.get(target); ok {
		if s != nil && s.LogCache {
			log.Printf("[replay] cache hit exact ts=%s step=%d", entry.ts.Format(time.RFC3339), entry.stepID)
		}
		*state = cloneState(entry.state)
		*stepTs = entry.ts
		*stepID = entry.stepID
	} else if entry, ok := cache.getLE(target); ok {
		if s != nil && s.LogCache {
			log.Printf("[replay] cache hit le ts=%s step=%d target=%s", entry.ts.Format(time.RFC3339), entry.stepID, target.Format(time.RFC3339))
		}
		*state = cloneState(entry.state)
		*stepTs = entry.ts
		*stepID = entry.stepID
		if err := fastForwardFromCache(ctx, s, params, target, state, stepTs, stepID); err != nil {
			return err
		}
		cache.add(*stepTs, *stepID, *state)
	} else {
		if s != nil && s.LogCache {
			log.Printf("[replay] cache miss, rebuild target=%s", target.Format(time.RFC3339))
		}
		if err := rebuildState(ctx, s, params, target, state); err != nil {
			return err
		}
		*stepTs = target
		if target.Equal(params.From) {
			*stepID = 1
		} else {
			*stepID = int64(target.Sub(params.From)/params.Step) + 1
		}
		cache.add(*stepTs, *stepID, *state)
	}
	if err := restartStream(ctx, s, params, *stepTs, streamCancel, eventCh, streamErr, pending); err != nil {
		return err
	}
	return nil
}
func sendFullSnapshot(ctx context.Context, s *Service, params Params, state map[int64]*sensorState, stepID *int64, stepTs *time.Time, saveOutput bool) error {
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
	if saveOutput {
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
	}
	return nil
}

func notifyOnStep(ctrl *Control, stepID int64, stepTs time.Time, updates int) {
	if ctrl == nil || ctrl.OnStep == nil {
		return
	}
	ctrl.OnStep(StepInfo{
		StepID:       stepID,
		StepTs:       stepTs,
		UpdatesCount: updates,
	})
}
