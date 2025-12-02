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

func (f *fakeStorage) Range(context.Context, []int64, time.Time, time.Time) (time.Time, time.Time, int64, error) {
	return time.Time{}, time.Time{}, 0, nil
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
		Sensors:    []int64{1, 2},
		From:       start,
		To:         start.Add(3 * time.Second),
		Step:       time.Second,
		Window:     time.Minute,
		Speed:      10.0,
		BatchSize:  1,
		SaveOutput: true,
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
				items[upd.Hash] = upd.Value
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

type controlStorage struct {
	warmup []storage.SensorEvent
	events []storage.SensorEvent
}

func (s *controlStorage) Warmup(context.Context, []int64, time.Time) ([]storage.SensorEvent, error) {
	return append([]storage.SensorEvent(nil), s.warmup...), nil
}

func (s *controlStorage) Stream(ctx context.Context, req storage.StreamRequest) (<-chan []storage.SensorEvent, <-chan error) {
	dataCh := make(chan []storage.SensorEvent, 1)
	errCh := make(chan error, 1)

	go func() {
		defer close(dataCh)
		defer close(errCh)

		var batch []storage.SensorEvent
		for _, ev := range s.events {
			if ev.Timestamp.Before(req.From) {
				continue
			}
			if !ev.Timestamp.Before(req.To) {
				continue
			}
			batch = append(batch, ev)
		}
		if len(batch) > 0 {
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			case dataCh <- batch:
			}
		}
	}()

	return dataCh, errCh
}

func (s *controlStorage) Range(context.Context, []int64, time.Time, time.Time) (time.Time, time.Time, int64, error) {
	return time.Time{}, time.Time{}, int64(len(s.events)), nil
}

func TestRunWithControlStepBackwardApply(t *testing.T) {
	from := time.Date(2025, 11, 21, 0, 0, 0, 0, time.UTC)
	st := &controlStorage{
		warmup: []storage.SensorEvent{
			{SensorID: 1, Timestamp: from.Add(-time.Second), Value: 5},
		},
		events: []storage.SensorEvent{
			{SensorID: 1, Timestamp: from, Value: 10},
			{SensorID: 1, Timestamp: from.Add(time.Second), Value: 20},
		},
	}
	client := &fakeClient{}
	cmdCh := make(chan Command, 4)
	stepCh := make(chan StepInfo, 4)

	svc := Service{Storage: st, Output: client}
	params := Params{
		Sensors:    []int64{1},
		From:       from,
		To:         from.Add(2 * time.Second),
		Step:       time.Second,
		Window:     time.Second,
		Speed:      1000,
		BatchSize:  10,
		SaveOutput: true,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- svc.RunWithControl(ctx, params, Control{
			Commands: cmdCh,
			OnStep: func(info StepInfo) {
				stepCh <- info
			},
		})
	}()

	waitStep := func(expected int64) {
		t.Helper()
		select {
		case info := <-stepCh:
			if info.StepID != expected {
				t.Fatalf("unexpected step id: %d, want %d", info.StepID, expected)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for step %d", expected)
		}
	}

	waitStep(1)

	sendCmd := func(cmd Command) {
		resp := make(chan error, 1)
		cmd.Resp = resp
		cmdCh <- cmd
		select {
		case err := <-resp:
			if err != nil {
				t.Fatalf("command %v returned error: %v", cmd.Type, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("command %v timeout", cmd.Type)
		}
	}

	sendCmd(Command{Type: CommandPause})
	sendCmd(Command{Type: CommandStepBackward, Apply: true})
	// Stop may return ErrStopped; treat it as success.
	resp := make(chan error, 1)
	cmdCh <- Command{Type: CommandStop, Resp: resp}
	select {
	case err := <-resp:
		if err != nil && err.Error() != (ErrStopped{}).Error() {
			t.Fatalf("stop returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("stop command timeout")
	}

	if len(client.payloads) == 0 {
		t.Fatalf("expected snapshot payload after step backward")
	}
	last := client.payloads[len(client.payloads)-1]
	if last.StepTs != from.Format(time.RFC3339) {
		t.Fatalf("unexpected snapshot ts: %s", last.StepTs)
	}
	if len(last.Updates) != 1 || last.Updates[0].Value != 5 {
		t.Fatalf("unexpected snapshot updates: %+v", last.Updates)
	}
}

func TestRunWithControlSeekApply(t *testing.T) {
	from := time.Date(2025, 11, 21, 0, 0, 0, 0, time.UTC)
	target := from.Add(2 * time.Second)
	st := &controlStorage{
		warmup: []storage.SensorEvent{
			{SensorID: 1, Timestamp: from.Add(-time.Second), Value: 1},
		},
		events: []storage.SensorEvent{
			{SensorID: 1, Timestamp: target, Value: 2},
		},
	}

	client := &fakeClient{}
	cmdCh := make(chan Command, 2)

	svc := Service{Storage: st, Output: client}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	params := Params{
		Sensors:    []int64{1},
		From:       from,
		To:         from.Add(3 * time.Second),
		Step:       time.Second,
		Window:     time.Second,
		Speed:      1000,
		BatchSize:  10,
		SaveOutput: true,
	}

	go func() {
		done <- svc.RunWithControl(ctx, params, Control{
			Commands: cmdCh,
		})
	}()

	respCh := make(chan error, 1)
	cmdCh <- Command{Type: CommandSeek, TS: target, Apply: true, Resp: respCh}
	select {
	case err := <-respCh:
		if err != nil {
			t.Fatalf("seek returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("seek command timeout")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("run did not finish after cancel")
	}

	if len(client.payloads) == 0 {
		t.Fatalf("expected snapshot after seek apply")
	}
	snap := client.payloads[len(client.payloads)-1]
	if snap.StepTs != target.Format(time.RFC3339) {
		t.Fatalf("snapshot ts mismatch: %s", snap.StepTs)
	}
	if len(snap.Updates) != 1 || snap.Updates[0].Value != 1 {
		t.Fatalf("snapshot updates mismatch: %+v", snap.Updates)
	}
}

func TestRunWithControlSaveOutputToggle(t *testing.T) {
	start := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	st := &controlStorage{
		warmup: []storage.SensorEvent{
			{SensorID: 1, Timestamp: start.Add(-time.Second), Value: 9},
		},
		events: []storage.SensorEvent{
			{SensorID: 1, Timestamp: start, Value: 10},
			{SensorID: 1, Timestamp: start.Add(time.Second), Value: 20},
		},
	}
	client := &fakeClient{}
	cmdCh := make(chan Command, 4)
	stepCh := make(chan StepInfo, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)

	svc := Service{Storage: st, Output: client}
	params := Params{
		Sensors:    []int64{1},
		From:       start,
		To:         start.Add(2 * time.Second),
		Step:       time.Second,
		Window:     time.Second,
		Speed:      1000,
		BatchSize:  10,
		SaveOutput: true,
	}

	go func() {
		done <- svc.RunWithControl(ctx, params, Control{
			Commands: cmdCh,
			OnStep: func(info StepInfo) {
				stepCh <- info
			},
		})
	}()

	waitStep := func(expected int64) {
		t.Helper()
		select {
		case info := <-stepCh:
			if info.StepID != expected {
				t.Fatalf("unexpected step id: %d, want %d", info.StepID, expected)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for step %d", expected)
		}
	}

	waitStep(1)
	// После первого шага делаем паузу и выключаем сохранение.
	cmdCh <- Command{Type: CommandPause}
	cmdCh <- Command{Type: CommandSaveOutput, SaveOutput: false}
	cmdCh <- Command{Type: CommandResume}

	waitStep(2)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("run did not finish after cancel")
	}

	// Сохранение было только до переключения.
	if len(client.payloads) != 1 {
		t.Fatalf("expected only first step to be saved, got %d payloads", len(client.payloads))
	}
	if len(client.payloads[0].Updates) == 0 {
		t.Fatalf("saved payload has no updates")
	}
}

func TestRunWithControlApplyRespectsSaveOutput(t *testing.T) {
	start := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	st := &controlStorage{
		warmup: []storage.SensorEvent{
			{SensorID: 1, Timestamp: start.Add(-time.Second), Value: 5},
		},
		events: []storage.SensorEvent{
			{SensorID: 1, Timestamp: start, Value: 10},
		},
	}
	client := &fakeClient{}
	cmdCh := make(chan Command, 2)

	svc := Service{Storage: st, Output: client}
	params := Params{
		Sensors:    []int64{1},
		From:       start,
		To:         start.Add(time.Second),
		Step:       time.Second,
		Window:     time.Second,
		Speed:      1000,
		BatchSize:  10,
		SaveOutput: false,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- svc.RunWithControl(ctx, params, Control{
			Commands: cmdCh,
		})
	}()

	resp := make(chan error, 1)
	cmdCh <- Command{Type: CommandApply, Resp: resp}
	select {
	case err := <-resp:
		if err != nil {
			t.Fatalf("apply returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("apply command timeout")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("run did not finish after cancel")
	}

	if len(client.payloads) != 0 {
		t.Fatalf("apply should not send payload when save_output=false, got %d", len(client.payloads))
	}
}

func TestStateCacheFastForward(t *testing.T) {
	start := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	sensors := []int64{1}

	store := &controlStorage{
		warmup: []storage.SensorEvent{
			{SensorID: 1, Timestamp: start.Add(-time.Second), Value: 10},
		},
		events: []storage.SensorEvent{
			{SensorID: 1, Timestamp: start, Value: 11},
			{SensorID: 1, Timestamp: start.Add(time.Second), Value: 12},
			{SensorID: 1, Timestamp: start.Add(2 * time.Second), Value: 13},
			{SensorID: 1, Timestamp: start.Add(2500 * time.Millisecond), Value: 14},
		},
	}
	client := &fakeClient{}
	svc := Service{Storage: store, Output: client}

	params := Params{
		Sensors:   sensors,
		From:      start,
		To:        start.Add(5 * time.Second),
		Step:      time.Second,
		Window:    time.Second,
		BatchSize: 10,
	}

	// Прогоняем один раз, чтобы убедиться в корректности warmup/stream.
	if err := svc.Run(context.Background(), params); err != nil {
		t.Fatalf("initial run failed: %v", err)
	}

	// Готовим кеш со снапшотом на 2s (stepID=3).
	state := map[int64]*sensorState{
		1: {value: 13, hasValue: true},
	}
	stepTs := start.Add(2 * time.Second)
	stepID := int64(3)
	cache := newStateCache(4)
	cache.add(stepTs, stepID, state)

	// Цель 3s должна взять кеш 2s и догнать до 3s, применив событие на 2.5s.
	target := start.Add(3 * time.Second)
	stateCopy := cloneState(state)
	var streamCancel context.CancelFunc
	eventCh := make(<-chan storage.SensorEvent)
	streamErr := make(<-chan error)
	pending := make([]storage.SensorEvent, 0, 16)

	if err := restoreState(context.Background(), &svc, params, target, &stateCopy, &stepTs, &stepID, &streamCancel, &eventCh, &streamErr, &pending, cache); err != nil {
		t.Fatalf("restoreState failed: %v", err)
	}
	if stepTs != target {
		t.Fatalf("stepTs after restore = %s, want %s", stepTs, target)
	}
	if stepID != 4 {
		t.Fatalf("stepID after restore = %d, want 4", stepID)
	}
	if val := stateCopy[1].value; val != 14 {
		t.Fatalf("state value after restore = %v, want 14", val)
	}
}
