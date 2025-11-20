package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pv/uniset-timemachine-go/internal/replay"
	"github.com/pv/uniset-timemachine-go/internal/sharedmem"
	"github.com/pv/uniset-timemachine-go/internal/storage"
)

type apiTestStorage struct{}

func (s *apiTestStorage) Warmup(context.Context, []int64, time.Time) ([]storage.SensorEvent, error) {
	return nil, nil
}

func (s *apiTestStorage) Stream(ctx context.Context, req storage.StreamRequest) (<-chan []storage.SensorEvent, <-chan error) {
	dataCh := make(chan []storage.SensorEvent)
	errCh := make(chan error, 1)
	go func() {
		defer close(dataCh)
		defer close(errCh)
		_ = req
	}()
	return dataCh, errCh
}

func (s *apiTestStorage) Range(context.Context, []int64) (time.Time, time.Time, error) {
	return time.Time{}, time.Time{}, nil
}

type apiTestClient struct {
	payloads []sharedmem.StepPayload
}

func (c *apiTestClient) Send(_ context.Context, payload sharedmem.StepPayload) error {
	c.payloads = append(c.payloads, payload)
	return nil
}

func newTestServer(t *testing.T) (*httptest.Server, *Manager) {
	t.Helper()
	svc := replay.Service{
		Storage: &apiTestStorage{},
		Output:  &apiTestClient{},
	}
	mgr := NewManager(svc, []int64{1, 2}, 1.0, time.Second, 16)
	srv := NewServer(mgr)
	return httptest.NewServer(srv.mux), mgr
}

func TestJobStartConflict(t *testing.T) {
	ts, mgr := newTestServer(t)
	defer ts.Close()

	from := time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	to := from.Add(10 * time.Second)
	body := map[string]any{
		"from":   from.Format(time.RFC3339),
		"to":     to.Format(time.RFC3339),
		"step":   "1s",
		"speed":  1.0,
		"window": "1s",
	}
	if resp := postJSON(t, ts.URL+"/api/v1/job", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("start job status = %d, want 200", resp.StatusCode)
	}
	if resp := postJSON(t, ts.URL+"/api/v1/job", body); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second start status = %d, want 409", resp.StatusCode)
	}
	mgr.Stop()
}

func TestPauseResumeAndState(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	from := time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	to := from.Add(5 * time.Second)
	body := map[string]any{
		"from":  from.Format(time.RFC3339),
		"to":    to.Format(time.RFC3339),
		"step":  "1s",
		"speed": 1.0,
	}
	postJSON(t, ts.URL+"/api/v1/job", body)

	if resp := postJSON(t, ts.URL+"/api/v1/job/pause", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("pause status = %d, want 200", resp.StatusCode)
	}
	if resp := postJSON(t, ts.URL+"/api/v1/job/resume", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("resume status = %d, want 200", resp.StatusCode)
	}

	resp, err := http.Get(ts.URL + "/api/v1/job/state")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("state status = %d, want 200", resp.StatusCode)
	}

	postJSON(t, ts.URL+"/api/v1/job/stop", nil)
}

func TestStepBackwardSeekApplySnapshot(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	from := time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	to := from.Add(6 * time.Second)
	postJSON(t, ts.URL+"/api/v1/job", map[string]any{
		"from":  from.Format(time.RFC3339),
		"to":    to.Format(time.RFC3339),
		"step":  "1s",
		"speed": 1.0,
	})

	if resp := postJSON(t, ts.URL+"/api/v1/job/step/backward", map[string]any{"apply": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("step backward status = %d, want 200", resp.StatusCode)
	}

	seekTs := from.Add(2 * time.Second)
	if resp := postJSON(t, ts.URL+"/api/v1/job/seek", map[string]any{"ts": seekTs.Format(time.RFC3339), "apply": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("seek status = %d, want 200", resp.StatusCode)
	}

	if resp := postJSON(t, ts.URL+"/api/v1/job/apply", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d, want 200", resp.StatusCode)
	}

	if resp := postJSON(t, ts.URL+"/api/v1/snapshot", map[string]any{"ts": seekTs.Format(time.RFC3339)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status = %d, want 200", resp.StatusCode)
	}

	postJSON(t, ts.URL+"/api/v1/job/stop", nil)
}

func postJSON(t *testing.T, url string, body map[string]any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	resp, err := http.Post(url, "application/json", &buf)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}
