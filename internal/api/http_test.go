package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
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

func (s *apiTestStorage) Range(context.Context, []int64, time.Time, time.Time) (time.Time, time.Time, int64, error) {
	start := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	return start, start.Add(10 * time.Second), 2, nil
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
	mgr := NewManager(svc, []int64{1, 2}, nil, 1.0, time.Second, 16, nil)
	srv := NewServer(mgr, nil)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("skip: tcp listen not permitted: %v", err)
	}
	testSrv := httptest.NewUnstartedServer(srv.mux)
	testSrv.Listener = ln
	testSrv.Start()
	t.Cleanup(testSrv.Close)
	return testSrv, mgr
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

func TestUIIndexServed(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/ui/")
	if err != nil {
		t.Fatalf("get /ui/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status /ui/ = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /ui/ body: %v", err)
	}
	if !bytes.Contains(body, []byte("TimeMachine Player")) {
		t.Fatalf("ui index missing expected marker, got: %s", string(body))
	}
}

func TestRangeEndpoint(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	check := func(path string) {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("range %s status = %d, want 200", path, resp.StatusCode)
		}
		var body map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if body["from"] == "" || body["to"] == "" {
			t.Fatalf("%s response empty: %#v", path, body)
		}
	}

	check("/api/v1/range")
	check("/api/v2/job/range")
}

func TestJobV2PendingFlow(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	from := time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	to := from.Add(6 * time.Second)
	rangeBody := map[string]any{
		"from":  from.Format(time.RFC3339),
		"to":    to.Format(time.RFC3339),
		"step":  "1s",
		"speed": 1.0,
	}
	seekTs := from.Add(2 * time.Second).Format(time.RFC3339)

	if resp := postJSON(t, ts.URL+"/api/v2/job/range", rangeBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("v2 range status = %d, want 200", resp.StatusCode)
	}
	if resp := postJSON(t, ts.URL+"/api/v2/job/seek", map[string]any{"ts": seekTs, "apply": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("v2 seek status = %d, want 200", resp.StatusCode)
	}

	var st struct {
		Status  string         `json:"status"`
		Pending map[string]any `json:"pending"`
	}
	getJSON(t, ts.URL+"/api/v2/job", &st)
	if st.Status != "pending" {
		t.Fatalf("status after pending = %s, want pending", st.Status)
	}

	if resp := postJSON(t, ts.URL+"/api/v2/job/start", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("v2 start status = %d, want 200", resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond)
	getJSON(t, ts.URL+"/api/v2/job", &st)
	if st.Status != "running" && st.Status != "done" && st.Status != "paused" {
		t.Fatalf("status after start = %s, want running/paused/done", st.Status)
	}
}

func TestV2CommandsLifecycle(t *testing.T) {
	ts, mgr := newTestServer(t)
	defer ts.Close()

	from := time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	to := from.Add(10 * time.Second)
	rangeBody := map[string]any{
		"from":  from.Format(time.RFC3339),
		"to":    to.Format(time.RFC3339),
		"step":  "1s",
		"speed": 5.0,
	}
	seekTs := from.Add(2 * time.Second)

	if resp := postJSON(t, ts.URL+"/api/v2/job/range", rangeBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("v2 range status = %d, want 200", resp.StatusCode)
	}
	if resp := postJSON(t, ts.URL+"/api/v2/job/start", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("v2 start status = %d, want 200", resp.StatusCode)
	}
	waitStatus(t, mgr, []string{"running"}, 2*time.Second)

	if resp := postJSON(t, ts.URL+"/api/v2/job/pause", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("v2 pause status = %d, want 200", resp.StatusCode)
	}
	waitStatus(t, mgr, []string{"paused"}, 2*time.Second)

	if resp := postJSON(t, ts.URL+"/api/v2/job/step/backward", map[string]any{"apply": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("v2 step backward status = %d, want 200", resp.StatusCode)
	}
	waitStatus(t, mgr, []string{"paused"}, 2*time.Second)

	if resp := postJSON(t, ts.URL+"/api/v2/job/seek", map[string]any{"ts": seekTs.Format(time.RFC3339), "apply": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("v2 seek status = %d, want 200", resp.StatusCode)
	}
	waitStatus(t, mgr, []string{"paused"}, 2*time.Second)
	if got := mgr.Status().LastTS; got.IsZero() || got.Sub(seekTs) < -time.Second || got.Sub(seekTs) > time.Second {
		t.Fatalf("LastTS after seek = %s, want around %s", got, seekTs)
	}

	if resp := postJSON(t, ts.URL+"/api/v2/job/resume", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("v2 resume status = %d, want 200", resp.StatusCode)
	}
	waitStatus(t, mgr, []string{"running"}, 2*time.Second)

	if resp := postJSON(t, ts.URL+"/api/v2/job/stop", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("v2 stop status = %d, want 200", resp.StatusCode)
	}
	waitStatus(t, mgr, []string{"done", "failed"}, 3*time.Second)
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

func getJSON(t *testing.T, url string, out interface{}) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get %s status = %d, want 200", url, resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
}

func waitStatus(t *testing.T, mgr *Manager, want []string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	wantSet := make(map[string]struct{}, len(want))
	for _, s := range want {
		wantSet[s] = struct{}{}
	}
	for time.Now().Before(deadline) {
		st := mgr.Status().Status
		if _, ok := wantSet[st]; ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("status did not reach %v within %s, last=%s", want, timeout, mgr.Status().Status)
}
