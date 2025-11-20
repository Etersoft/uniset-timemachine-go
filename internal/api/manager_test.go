package api

import (
	"context"
	"io"
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
	return NewManager(svc, []int64{1, 2}, 1000, step, 8)
}

func TestManagerStartConflictAndStop(t *testing.T) {
	mgr := newTestManager(t)
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(3 * time.Second)

	if err := mgr.Start(context.Background(), from, to, time.Second, 1, time.Second); err != nil {
		t.Fatalf("start returned error: %v", err)
	}
	if err := mgr.Start(context.Background(), from, to, time.Second, 1, time.Second); err == nil {
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

	if err := mgr.Start(context.Background(), from, to, time.Second, 1, time.Second); err != nil {
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
