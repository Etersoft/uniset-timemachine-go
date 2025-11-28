package api

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pv/uniset-timemachine-go/internal/replay"
	"github.com/pv/uniset-timemachine-go/internal/sharedmem"
	"github.com/pv/uniset-timemachine-go/internal/storage/memstore"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(5 * time.Second)
	step := time.Second

	store := memstore.NewExampleStore([]int64{1, 2}, from, to, step)
	svc := replay.Service{
		Storage: store,
		Output:  &sharedmem.StdoutClient{Writer: io.Discard},
	}
	return NewManager(svc, []int64{1, 2}, nil, 1000, step, 8, nil, true, false)
}

func TestManagerStartConflictAndStop(t *testing.T) {
	mgr := newTestManager(t)
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(3 * time.Second)

	if err := mgr.Start(context.Background(), from, to, time.Second, 1, time.Second, true); err != nil {
		t.Fatalf("start returned error: %v", err)
	}
	if err := mgr.Start(context.Background(), from, to, time.Second, 1, time.Second, true); err == nil {
		t.Fatalf("expected conflict on second start")
	}
	if status := mgr.Status().Status; status != "running" && status != "paused" {
		t.Fatalf("unexpected status after start: %s", status)
	}
	if err := mgr.Stop(); err != nil && err.Error() != (replay.ErrStopped{}).Error() {
		t.Fatalf("stop returned error: %v", err)
	}
}

func TestManagerPauseResume(t *testing.T) {
	mgr := newTestManager(t)
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(3 * time.Second)

	if err := mgr.Start(context.Background(), from, to, time.Second, 1, time.Second, true); err != nil {
		t.Fatalf("start returned error: %v", err)
	}

	if err := mgr.Pause(); err != nil {
		t.Fatalf("pause returned error: %v", err)
	}
	if status := mgr.Status().Status; status != "paused" {
		t.Fatalf("status after pause = %s, want paused", status)
	}

	if err := mgr.Resume(); err != nil {
		t.Fatalf("resume returned error: %v", err)
	}
	if status := mgr.Status().Status; status != "running" {
		t.Fatalf("status after resume = %s, want running", status)
	}

	_ = mgr.Stop()
}

func TestManagerPendingRangeAndSeek(t *testing.T) {
	mgr := newTestManager(t)
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(5 * time.Second)
	seekTs := from.Add(2 * time.Second)

	mgr.SetRange(from, to, time.Second, 1, time.Second, true)
	mgr.SetPendingSeek(seekTs)
	st := mgr.Status()
	if st.Status != "pending" {
		t.Fatalf("status after pending range = %s, want pending", st.Status)
	}
	if !st.Pending.RangeSet {
		t.Fatalf("pending range not set")
	}
	if !st.Pending.SeekSet || !st.Pending.SeekTS.Equal(seekTs) {
		t.Fatalf("pending seek mismatch: %+v", st.Pending)
	}

	if err := mgr.StartPending(context.Background()); err != nil {
		t.Fatalf("StartPending error: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	st2 := mgr.Status()
	if st2.Pending.RangeSet {
		t.Fatalf("pending range should be cleared after start")
	}
	if st2.LastTS.IsZero() {
		t.Fatalf("LastTS not updated after start")
	}
	// Допускаем погрешность шага.
	if st2.LastTS.Sub(seekTs) < -time.Second || st2.LastTS.Sub(seekTs) > time.Second {
		t.Fatalf("LastTS = %s, want around %s", st2.LastTS, seekTs)
	}
	_ = mgr.Stop()
}

func TestManagerPlaybackFlowScenario(t *testing.T) {
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	step := 200 * time.Millisecond
	to := from.Add(3 * time.Second)

	store := memstore.NewExampleStore([]int64{1}, from, to, step)
	svc := replay.Service{
		Storage: store,
		Output:  &sharedmem.StdoutClient{Writer: io.Discard},
	}
	mgr := NewManager(svc, []int64{1}, nil, 2.0, step, 8, nil, true, false)

	mgr.SetRange(from, to, step, 2.0, time.Second, true)
	seekStart := from.Add(2 * step)
	mgr.SetPendingSeek(seekStart)
	if err := mgr.StartPending(context.Background()); err != nil {
		t.Fatalf("StartPending error: %v", err)
	}

	waitManagerStatus(t, mgr, []string{"running"}, 2*time.Second)
	waitForCond(t, time.Second, func() bool {
		cur := mgr.Status().LastTS
		return !cur.IsZero() && (cur.Equal(seekStart) || cur.After(seekStart))
	})

	if err := mgr.Pause(); err != nil {
		t.Fatalf("Pause error: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"paused"}, 2*time.Second)
	lastAfterStart := mgr.Status().LastTS
	if lastAfterStart.Before(seekStart) {
		t.Fatalf("expected last_ts >= seekStart, got %s", lastAfterStart)
	}

	seekForward := seekStart.Add(3 * step)
	if err := mgr.Seek(seekForward, false); err != nil {
		t.Fatalf("Seek forward error: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"paused"}, 2*time.Second)
	waitForCond(t, time.Second, func() bool {
		cur := mgr.Status().LastTS
		return approxTime(cur, seekForward, step)
	})
	lastAfterSeek := mgr.Status().LastTS

	if err := mgr.Resume(); err != nil {
		t.Fatalf("Resume error: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"running"}, 2*time.Second)
	waitForCond(t, 2*time.Second, func() bool {
		return mgr.Status().LastTS.After(lastAfterSeek)
	})

	if err := mgr.Pause(); err != nil {
		t.Fatalf("Pause #2 error: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"paused"}, 2*time.Second)

	if err := mgr.StepBackward(false); err != nil {
		t.Fatalf("StepBackward error: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"paused"}, 2*time.Second)
	backTs := mgr.Status().LastTS

	if err := mgr.Resume(); err != nil {
		t.Fatalf("Resume after back error: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"running"}, 2*time.Second)
	waitForCond(t, 2*time.Second, func() bool {
		return mgr.Status().LastTS.After(backTs)
	})

	if err := mgr.Stop(); err != nil && err.Error() != (replay.ErrStopped{}).Error() {
		t.Fatalf("Stop error: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"done", "failed"}, 3*time.Second)
}

func TestManagerPauseResumeAfterSteps(t *testing.T) {
	mgr := newTestManager(t)
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(4 * time.Second)

	if err := mgr.Start(context.Background(), from, to, time.Second, 1, time.Second, true); err != nil {
		t.Fatalf("start returned error: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"running"}, 2*time.Second)
	waitForCond(t, 2*time.Second, func() bool { return mgr.Status().LastTS.After(from) })

	if err := mgr.Pause(); err != nil {
		t.Fatalf("pause returned error: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"paused"}, 2*time.Second)
	last := mgr.Status().LastTS
	if err := mgr.Resume(); err != nil {
		t.Fatalf("resume returned error: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"running"}, 2*time.Second)
	waitForCond(t, 2*time.Second, func() bool { return mgr.Status().LastTS.After(last) })
	_ = mgr.Stop()
}

func TestManagerSeekApplyPaused(t *testing.T) {
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	step := time.Second
	to := from.Add(5 * time.Second)

	store := memstore.NewExampleStore([]int64{1}, from, to, step)
	var capClient captureClient
	client := &capClient
	svc := replay.Service{Storage: store, Output: client}
	mgr := NewManager(svc, []int64{1}, nil, 1, step, 8, nil, true, false)

	if err := mgr.Start(context.Background(), from, to, step, 1, step, true); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"running"}, 2*time.Second)
	if err := mgr.Pause(); err != nil {
		t.Fatalf("pause: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"paused"}, 2*time.Second)
	target := from.Add(2 * step)
	if err := mgr.Seek(target, true); err != nil {
		t.Fatalf("seek apply: %v", err)
	}
	waitForCond(t, time.Second, func() bool { return approxTime(mgr.Status().LastTS, target, step) })
	waitForCond(t, time.Second, func() bool {
		return len(client.Payloads()) > 0
	})
	_ = mgr.Stop()
}

func TestManagerStepBackwardApplyPaused(t *testing.T) {
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	step := time.Second
	to := from.Add(4 * time.Second)

	store := memstore.NewExampleStore([]int64{1}, from, to, step)
	var capClient captureClient
	client := &capClient
	svc := replay.Service{Storage: store, Output: client}
	mgr := NewManager(svc, []int64{1}, nil, 1, step, 8, nil, true, false)

	if err := mgr.Start(context.Background(), from, to, step, 1, step, true); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"running"}, 2*time.Second)
	waitForCond(t, 2*time.Second, func() bool { return mgr.Status().LastTS.After(from) })
	if err := mgr.Pause(); err != nil {
		t.Fatalf("pause: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"paused"}, 2*time.Second)
	before := mgr.Status().LastTS
	if err := mgr.StepBackward(true); err != nil {
		t.Fatalf("step back apply: %v", err)
	}
	waitForCond(t, 2*time.Second, func() bool {
		cur := mgr.Status().LastTS
		return !cur.IsZero() && (cur.Before(before) || approxTime(cur, before, step))
	})
	waitForCond(t, 2*time.Second, func() bool {
		return len(client.Payloads()) > 0
	})
	_ = mgr.Stop()
}

func TestManagerStopFromPausedAndRunning(t *testing.T) {
	mgr := newTestManager(t)
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(3 * time.Second)

	if err := mgr.Start(context.Background(), from, to, time.Second, 1, time.Second, true); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"running"}, 2*time.Second)
	if err := mgr.Pause(); err != nil {
		t.Fatalf("pause: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"paused"}, 2*time.Second)
	if err := mgr.Stop(); err != nil && err.Error() != (replay.ErrStopped{}).Error() {
		t.Fatalf("stop from paused: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"done", "failed"}, 2*time.Second)

	if err := mgr.Start(context.Background(), from, to, time.Second, 1, time.Second, true); err != nil {
		t.Fatalf("restart: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"running"}, 2*time.Second)
	if err := mgr.Stop(); err != nil && err.Error() != (replay.ErrStopped{}).Error() {
		t.Fatalf("stop from running: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"done", "failed"}, 2*time.Second)
}

func TestManagerErrorBranches(t *testing.T) {
	mgr := newTestManager(t)
	if err := mgr.StartPending(context.Background()); err == nil {
		t.Fatalf("expected error when pending range is not set")
	}
	if err := mgr.SetSaveOutput(true); err == nil {
		t.Fatalf("expected error on set save output without job")
	}

	// Запускаем задачу, а затем пытаемся стартовать pending поверх неё.
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(2 * time.Second)
	if err := mgr.Start(context.Background(), from, to, time.Second, 1, time.Second, true); err != nil {
		t.Fatalf("start returned error: %v", err)
	}
	mgr.SetRange(from, to, time.Second, 1, time.Second, true)
	if err := mgr.StartPending(context.Background()); err == nil {
		t.Fatalf("expected error when job already active and pending start called")
	}
	_ = mgr.Stop()
}

func TestManagerDefaultsApplied(t *testing.T) {
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	step := 200 * time.Millisecond
	to := from.Add(2 * time.Second)
	store := memstore.NewExampleStore([]int64{1}, from, to, step)
	svc := replay.Service{
		Storage: store,
		Output:  &sharedmem.StdoutClient{Writer: io.Discard},
	}
	mgr := NewManager(svc, []int64{1}, nil, 0, 0, 4, nil, false, false)
	if err := mgr.Start(context.Background(), from, to, step, 0, 0, true); err != nil {
		t.Fatalf("start with defaults: %v", err)
	}
	st := mgr.Status()
	if st.Params.SaveOutput {
		t.Fatalf("save_output should be false when saveAllowed is false")
	}
	if st.Params.Step != step || st.Params.Window <= 0 || st.Params.Speed <= 0 {
		t.Fatalf("defaults not applied: %#v", st.Params)
	}
	_ = mgr.Stop()
}

func TestManagerStartConflictsByStatus(t *testing.T) {
	mgr := newTestManager(t)
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(2 * time.Second)

	if err := mgr.Start(context.Background(), from, to, time.Second, 1, time.Second, true); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := mgr.Start(context.Background(), from, to, time.Second, 1, time.Second, true); err == nil {
		t.Fatalf("expected conflict when job running")
	}
	if err := mgr.Pause(); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := mgr.Start(context.Background(), from, to, time.Second, 1, time.Second, true); err == nil {
		t.Fatalf("expected conflict when job paused")
	}
	_ = mgr.Stop()
	waitManagerStatus(t, mgr, []string{"done", "failed"}, 2*time.Second)
	if err := mgr.Start(context.Background(), from, to, time.Second, 1, time.Second, true); err != nil {
		t.Fatalf("start after stop should succeed: %v", err)
	}
	_ = mgr.Stop()
}

func TestManagerSetSaveOutputActiveJob(t *testing.T) {
	mgr := newTestManager(t)
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(2 * time.Second)
	if err := mgr.Start(context.Background(), from, to, time.Second, 1, time.Second, true); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := mgr.SetSaveOutput(false); err != nil {
		t.Fatalf("set save output on job: %v", err)
	}
	st := mgr.Status()
	if st.Params.SaveOutput {
		t.Fatalf("save_output should be false after SetSaveOutput(false)")
	}
	_ = mgr.Stop()
}

func TestManagerStartPendingWithSeekResume(t *testing.T) {
	mgr := newTestManager(t)
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(4 * time.Second)
	seekTs := from.Add(2 * time.Second)

	mgr.SetRange(from, to, time.Second, 1, time.Second, true)
	mgr.SetPendingSeek(seekTs)
	if err := mgr.StartPending(context.Background()); err != nil {
		t.Fatalf("StartPending: %v", err)
	}
	waitManagerStatus(t, mgr, []string{"paused", "running"}, 2*time.Second)
	waitForCond(t, 2*time.Second, func() bool { return approxTime(mgr.Status().LastTS, seekTs, time.Second) })
	_ = mgr.Stop()
}

// captureClientForManagerTest is a local copy to avoid import cycle with http_sqlite_test.
type captureClientForManagerTest struct {
	mu       sync.Mutex
	payloads []sharedmem.StepPayload
}

func (c *captureClientForManagerTest) Send(_ context.Context, payload sharedmem.StepPayload) error {
	c.mu.Lock()
	c.payloads = append(c.payloads, payload)
	c.mu.Unlock()
	return nil
}

func (c *captureClientForManagerTest) Payloads() []sharedmem.StepPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]sharedmem.StepPayload, len(c.payloads))
	copy(cp, c.payloads)
	return cp
}

func waitManagerStatus(t *testing.T, mgr *Manager, want []string, timeout time.Duration) string {
	t.Helper()
	wantSet := make(map[string]struct{}, len(want))
	for _, s := range want {
		wantSet[s] = struct{}{}
	}
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		last = normalizeStatus(mgr.Status().Status)
		if _, ok := wantSet[last]; ok {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("status did not reach %v within %s, last=%s", want, timeout, last)
	return ""
}

func waitForCond(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func approxTime(a, b time.Time, tol time.Duration) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	delta := a.Sub(b)
	if delta < 0 {
		delta = -delta
	}
	return delta <= tol
}

func normalizeStatus(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
