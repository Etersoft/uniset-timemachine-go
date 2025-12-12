package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pv/uniset-timemachine-go/internal/replay"
	"github.com/pv/uniset-timemachine-go/internal/sharedmem"
	"github.com/pv/uniset-timemachine-go/internal/storage"
)

const testSessionToken = "test-session"

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

// mockUnknownStore реализует Storage + UnknownAwareStorage с заданным unknownCount.
type mockUnknownStore struct {
	min, max   time.Time
	count      int64
	unknown    int64
	rangeError error
}

func (s *mockUnknownStore) Warmup(context.Context, []int64, time.Time) ([]storage.SensorEvent, error) {
	return nil, nil
}

func (s *mockUnknownStore) Stream(ctx context.Context, req storage.StreamRequest) (<-chan []storage.SensorEvent, <-chan error) {
	dataCh := make(chan []storage.SensorEvent)
	errCh := make(chan error, 1)
	go func() {
		defer close(dataCh)
		defer close(errCh)
		_ = req
	}()
	return dataCh, errCh
}

func (s *mockUnknownStore) Range(context.Context, []int64, time.Time, time.Time) (time.Time, time.Time, int64, error) {
	return s.min, s.max, s.count, s.rangeError
}

func (s *mockUnknownStore) RangeWithUnknown(context.Context, []int64, time.Time, time.Time) (time.Time, time.Time, int64, int64, error) {
	return s.min, s.max, s.count, s.unknown, s.rangeError
}

func newTestServer(t *testing.T) (*httptest.Server, *Manager) {
	t.Helper()
	svc := replay.Service{
		Storage: &apiTestStorage{},
		Output:  &apiTestClient{},
	}
	mgr := NewManager(svc, []int64{1, 2}, nil, 1.0, time.Second, 16, nil, true, false, 0)
	srv := NewServer(mgr, nil, "")

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

func newTestServerWithTimeout(t *testing.T, timeout time.Duration) (*httptest.Server, *Manager) {
	t.Helper()
	svc := replay.Service{
		Storage: &apiTestStorage{},
		Output:  &apiTestClient{},
	}
	mgr := NewManager(svc, []int64{1, 2}, nil, 1.0, time.Second, 16, nil, true, false, timeout)
	srv := NewServer(mgr, nil, "")

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

func newServerWithMode(t *testing.T, mode string, store storage.Storage) (*httptest.Server, *Manager) {
	t.Helper()
	if store == nil {
		store = &apiTestStorage{}
	}
	svc := replay.Service{
		Storage: store,
		Output:  &apiTestClient{},
	}
	mgr := NewManager(svc, []int64{1, 2}, nil, 1.0, time.Second, 16, nil, true, false, 0)
	srv := NewServer(mgr, nil, mode)

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
	if resp := postJSON(t, ts.URL+"/api/v2/job/range", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("start job status = %d, want 200", resp.StatusCode)
	}
	if resp := postJSON(t, ts.URL+"/api/v2/job/start", map[string]any{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("start job status = %d, want 200", resp.StatusCode)
	}
	if resp := postJSON(t, ts.URL+"/api/v2/job/start", map[string]any{}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("second start status = %d, want 400", resp.StatusCode)
	}
	mgr.Stop()
}

func TestJobStopStatus(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	from := time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	to := from.Add(2 * time.Second)
	body := map[string]any{
		"from":  from.Format(time.RFC3339),
		"to":    to.Format(time.RFC3339),
		"step":  "1s",
		"speed": 1.0,
	}
	postJSON(t, ts.URL+"/api/v2/job/range", body)
	postJSON(t, ts.URL+"/api/v2/job/start", map[string]any{})

	if resp := postJSON(t, ts.URL+"/api/v2/job/stop", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop status = %d, want 200", resp.StatusCode)
	}
}

func TestHandlersInvalidJSON(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	cases := []string{
		"/api/v2/job/range",
		"/api/v2/job/seek",
		"/api/v2/job/step/forward",
		"/api/v2/job/step/backward",
		"/api/v2/job/apply",
		"/api/v2/job/resume",
	}
	for _, path := range cases {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewBufferString(`{"bad":`))
		req.Header.Set("X-TM-Session", testSessionToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s expected 400 on bad json, got %d", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// Unknown sensors handling modes (warn/strict/off) for range endpoints.
func TestRangeUnknownModesGET(t *testing.T) {
	now := time.Now().UTC()
	store := &mockUnknownStore{
		min:     now.Add(-time.Hour),
		max:     now,
		count:   5,
		unknown: 3,
	}

	cases := []struct {
		mode       string
		wantStatus int
	}{
		{"warn", http.StatusOK},
		{"off", http.StatusOK},
		{"strict", http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		testSrv, _ := newServerWithMode(t, tc.mode, store)

		resp, err := http.Get(testSrv.URL + "/api/v2/job/range")
		if err != nil {
			t.Fatalf("GET range mode=%s: %v", tc.mode, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != tc.wantStatus {
			t.Fatalf("mode=%s status=%d want=%d", tc.mode, resp.StatusCode, tc.wantStatus)
		}
		if tc.wantStatus == http.StatusOK {
			var data map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
				t.Fatalf("decode mode=%s: %v", tc.mode, err)
			}
			if tc.mode == "warn" {
				if unk, ok := data["unknown_count"].(float64); !ok || int64(unk) != store.unknown {
					t.Fatalf("mode=%s unknown_count=%v want=%d", tc.mode, data["unknown_count"], store.unknown)
				}
			}
		}
	}
}

func TestRangeUnknownModesPOST(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store := &mockUnknownStore{
		min:     now.Add(-time.Hour),
		max:     now,
		count:   5,
		unknown: 2,
	}
	body := map[string]any{
		"from":   now.Add(-time.Minute).Format(time.RFC3339),
		"to":     now.Format(time.RFC3339),
		"step":   "1s",
		"speed":  1.0,
		"window": "1s",
	}

	cases := []struct {
		mode       string
		wantStatus int
	}{
		{"warn", http.StatusOK},
		{"off", http.StatusOK},
		{"strict", http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		testSrv, _ := newServerWithMode(t, tc.mode, store)

		buf, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, testSrv.URL+"/api/v2/job/range", bytes.NewReader(buf))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-TM-Session", testSessionToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST range mode=%s: %v", tc.mode, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != tc.wantStatus {
			t.Fatalf("mode=%s status=%d want=%d", tc.mode, resp.StatusCode, tc.wantStatus)
		}
		if tc.wantStatus == http.StatusOK && tc.mode == "warn" {
			var data map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
				t.Fatalf("decode mode=%s: %v", tc.mode, err)
			}
			if unk, ok := data["unknown_count"].(float64); !ok || int64(unk) != store.unknown {
				t.Fatalf("mode=%s unknown_count=%v want=%d", tc.mode, data["unknown_count"], store.unknown)
			}
		}
	}
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
	postJSON(t, ts.URL+"/api/v2/job/range", body)
	postJSON(t, ts.URL+"/api/v2/job/start", map[string]any{})

	if resp := postJSON(t, ts.URL+"/api/v2/job/pause", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("pause status = %d, want 200", resp.StatusCode)
	}
	if resp := postJSON(t, ts.URL+"/api/v2/job/resume", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("resume status = %d, want 200", resp.StatusCode)
	}

	resp, err := http.Get(ts.URL + "/api/v2/job")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("state status = %d, want 200", resp.StatusCode)
	}

	postJSON(t, ts.URL+"/api/v2/job/stop", nil)
}

func TestStartPendingWithoutRange(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()
	resp := postJSON(t, ts.URL+"/api/v2/job/start", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("start without pending range = %d, want 400", resp.StatusCode)
	}
}

func TestSensorsEndpoint(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/v2/sensors")
	if err != nil {
		t.Fatalf("get sensors: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sensors status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode sensors: %v", err)
	}
	if _, ok := body["count"]; !ok {
		t.Fatalf("sensors response missing count: %#v", body)
	}
}

func TestJobSensorsEndpoints(t *testing.T) {
	ts, mgr := newTestServer(t)
	defer ts.Close()

	var getBody map[string]any
	getJSON(t, ts.URL+"/api/v2/job/sensors", &getBody)
	if count, ok := getBody["count"].(float64); !ok || int(count) != 2 {
		t.Fatalf("get job sensors count=%v, want 2", getBody["count"])
	}
	if def, ok := getBody["default"].(bool); !ok || !def {
		t.Fatalf("default flag unexpected: %v", getBody["default"])
	}

	// Set subset with one valid, one invalid (using sensor names).
	// Without config, sensors with hashes 1 and 2 have names "hash1" and "hash2".
	resp := postJSON(t, ts.URL+"/api/v2/job/sensors", map[string]any{"sensors": []string{"hash1", "invalid_name"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("set job sensors status=%d body=%s", resp.StatusCode, string(b))
	}
	working := mgr.WorkingSensors()
	if len(working) != 1 || working[0] != 1 {
		t.Fatalf("working sensors after set=%v, want [1]", working)
	}

	getBody = map[string]any{}
	getJSON(t, ts.URL+"/api/v2/job/sensors", &getBody)
	if count, ok := getBody["count"].(float64); !ok || int(count) != 1 {
		t.Fatalf("after set count=%v, want 1", getBody["count"])
	}
	if def, ok := getBody["default"].(bool); ok && def {
		t.Fatalf("default flag should be false after custom set")
	}

	// Invalid only -> expect 400
	resp = postJSON(t, ts.URL+"/api/v2/job/sensors", map[string]any{"sensors": []string{"invalid_sensor"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("set invalid sensors status=%d, want 400", resp.StatusCode)
	}
}

func TestJobGetState(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/v2/job")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("job status = %d, want 200", resp.StatusCode)
	}
}

func TestCORSOptions(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/api/v2/job/range", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("options request: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("options status = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestStepBackwardSeekApplySnapshot(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	from := time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	to := from.Add(6 * time.Second)
	postJSON(t, ts.URL+"/api/v2/job/range", map[string]any{
		"from":  from.Format(time.RFC3339),
		"to":    to.Format(time.RFC3339),
		"step":  "1s",
		"speed": 1.0,
	})
	postJSON(t, ts.URL+"/api/v2/job/start", map[string]any{})

	if resp := postJSON(t, ts.URL+"/api/v2/job/step/backward", map[string]any{"apply": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("step backward status = %d, want 200", resp.StatusCode)
	}

	seekTs := from.Add(2 * time.Second)
	if resp := postJSON(t, ts.URL+"/api/v2/job/seek", map[string]any{"ts": seekTs.Format(time.RFC3339), "apply": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("seek status = %d, want 200", resp.StatusCode)
	}

	if resp := postJSON(t, ts.URL+"/api/v2/job/apply", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d, want 200", resp.StatusCode)
	}

	if resp := postJSON(t, ts.URL+"/api/v2/snapshot", map[string]any{"ts": seekTs.Format(time.RFC3339)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status = %d, want 200", resp.StatusCode)
	}

	postJSON(t, ts.URL+"/api/v2/job/stop", nil)
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
	if !bytes.Contains(body, []byte("TimeMachine Replayer")) {
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
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if body["from"] == nil || body["to"] == nil {
			t.Fatalf("%s response empty: %#v", path, body)
		}
	}

	check("/api/v2/job/range")
}

func TestSensorsCountAndSnapshotErrors(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	// bad from
	resp, err := http.Get(ts.URL + "/api/v2/job/sensors/count?from=bad")
	if err != nil {
		t.Fatalf("get sensors count: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("sensors count bad from = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// ok
	resp, err = http.Get(ts.URL + "/api/v2/job/sensors/count?from=2024-06-01T00:00:00Z&to=2024-06-01T00:00:05Z")
	if err != nil {
		t.Fatalf("get sensors count ok: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sensors count status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if body["sensor_count"] == nil {
		t.Fatalf("sensor_count missing in response")
	}

	// snapshot invalid ts
	resp = postJSON(t, ts.URL+"/api/v2/snapshot", map[string]any{"ts": "bad"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("snapshot bad ts status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// snapshot ok
	resp = postJSON(t, ts.URL+"/api/v2/snapshot", map[string]any{"ts": "2024-06-01T00:00:00Z"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("snapshot ok status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestWSStateEndpoint(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()
	// HTTP GET should fail (expect upgrade required / bad request / service unavailable if streamer is absent).
	resp, err := http.Get(ts.URL + "/api/v2/ws/state")
	if err != nil {
		t.Fatalf("ws state GET: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUpgradeRequired && resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ws state GET status = %d, want 400/426/503", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestControlLockAndClaim(t *testing.T) {
	timeout := 300 * time.Millisecond
	ts, _ := newTestServerWithTimeout(t, timeout)
	defer ts.Close()

	tokenA := "tok-A"
	tokenB := "tok-B"

	// A устанавливает диапазон -> становится контроллером.
	if resp := postJSONWithToken(t, ts.URL+"/api/v2/job/range", map[string]any{
		"from":   time.Now().Add(-time.Second).UTC().Format(time.RFC3339),
		"to":     time.Now().UTC().Format(time.RFC3339),
		"step":   "1s",
		"speed":  1,
		"window": "1s",
	}, tokenA); resp.StatusCode != http.StatusOK {
		t.Fatalf("range set by A status = %d", resp.StatusCode)
	}

	// B пытается остановить — должен получить 403.
	if resp := postJSONWithToken(t, ts.URL+"/api/v2/job/stop", nil, tokenB); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stop by B status = %d, want 403", resp.StatusCode)
	}

	// Ждём, пока истечёт таймаут, и Claim от B должен сработать.
	time.Sleep(timeout + 100*time.Millisecond)
	if resp := postJSONWithToken(t, ts.URL+"/api/v2/session/claim", nil, tokenB); resp.StatusCode != http.StatusOK {
		t.Fatalf("claim by B status = %d, want 200", resp.StatusCode)
	}

	// Теперь stop от B проходит, а от A — 403.
	if resp := postJSONWithToken(t, ts.URL+"/api/v2/job/stop", nil, tokenB); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop by B status = %d, want 200", resp.StatusCode)
	}
	if resp := postJSONWithToken(t, ts.URL+"/api/v2/job/stop", nil, tokenA); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stop by A after claim status = %d, want 403", resp.StatusCode)
	}
}

// Проверяем, что /api/v2/session корректно отражает наличие контроллера,
// и второй Claim получает 409, пока активный контроллер не истёк.
func TestSessionStatusAndClaimExclusiveHTTP(t *testing.T) {
	timeout := 500 * time.Millisecond
	ts, _ := newTestServerWithTimeout(t, timeout)
	defer ts.Close()

	tokenA := "tok-A"
	tokenB := "tok-B"

	// Claim без токена должен отдавать 400.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v2/session/claim", bytes.NewReader([]byte(`{}`)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("claim without token request err: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("claim without token status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Первый захватывает управление.
	if resp := postJSONWithToken(t, ts.URL+"/api/v2/session/claim", nil, tokenA); resp.StatusCode != http.StatusOK {
		t.Fatalf("claim A status = %d, want 200", resp.StatusCode)
	}

	// Вторая сессия видит, что контроллер присутствует.
	var status map[string]any
	getJSONWithToken(t, ts.URL+"/api/v2/session", &status, tokenB)
	if present := status["controller_present"]; present != true {
		t.Fatalf("controller_present for B = %v, want true", present)
	}
	if isCtrl := status["is_controller"]; isCtrl == true {
		t.Fatalf("is_controller for B = %v, want false", isCtrl)
	}
	if canClaim := status["can_claim"]; canClaim == true {
		t.Fatalf("can_claim for B = %v, want false while controller active", canClaim)
	}

	// Попытка Claim от B до таймаута должна вернуть 409.
	if resp := postJSONWithToken(t, ts.URL+"/api/v2/session/claim", nil, tokenB); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second claim status = %d, want 409", resp.StatusCode)
	}

	// После истечения таймаута Claim должен пройти.
	time.Sleep(timeout + 100*time.Millisecond)
	if resp := postJSONWithToken(t, ts.URL+"/api/v2/session/claim", nil, tokenB); resp.StatusCode != http.StatusOK {
		t.Fatalf("claim B after timeout status = %d, want 200", resp.StatusCode)
	}
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
	// Pending seek when no job -> pending status OK.
	if resp := postJSON(t, ts.URL+"/api/v2/job/seek", map[string]any{"ts": seekTs.Format(time.RFC3339), "apply": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("v2 seek pending status = %d, want 200", resp.StatusCode)
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
	return postJSONWithToken(t, url, body, testSessionToken)
}

func postJSONWithToken(t *testing.T, url string, body map[string]any, token string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-TM-Session", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}

func getJSON(t *testing.T, url string, out interface{}) {
	getJSONWithToken(t, url, out, testSessionToken)
}

func getJSONWithToken(t *testing.T, url string, out interface{}, token string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("X-TM-Session", token)
	}
	resp, err := http.DefaultClient.Do(req)
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

// TestTimezoneHandling verifies that RFC3339 timestamps are correctly parsed and formatted in UTC.
func TestTimezoneHandling(t *testing.T) {
	t.Run("RFC3339 with offset parses to UTC", func(t *testing.T) {
		// Input with explicit timezone offset (+03:00)
		input := "2024-06-01T12:00:00+03:00"
		parsed, err := time.Parse(time.RFC3339, input)
		if err != nil {
			t.Fatalf("failed to parse RFC3339: %v", err)
		}

		// Expected UTC time (12:00 +03:00 = 09:00 UTC)
		expected := time.Date(2024, 6, 1, 9, 0, 0, 0, time.UTC)
		if !parsed.UTC().Equal(expected) {
			t.Errorf("expected %v, got %v", expected, parsed.UTC())
		}
	})

	t.Run("RFC3339 with Z parses to UTC", func(t *testing.T) {
		input := "2024-06-01T12:00:00Z"
		parsed, err := time.Parse(time.RFC3339, input)
		if err != nil {
			t.Fatalf("failed to parse RFC3339: %v", err)
		}

		expected := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
		if !parsed.UTC().Equal(expected) {
			t.Errorf("expected %v, got %v", expected, parsed.UTC())
		}
	})

	t.Run("UTC formatting always produces Z suffix", func(t *testing.T) {
		// Create time with explicit non-UTC location
		loc := time.FixedZone("TEST", 3*60*60) // +03:00
		localTime := time.Date(2024, 6, 1, 15, 0, 0, 0, loc)

		// Format as UTC should produce Z suffix
		formatted := localTime.UTC().Format(time.RFC3339)
		if !strings.HasSuffix(formatted, "Z") {
			t.Errorf("expected Z suffix, got %s", formatted)
		}

		// Should be 12:00 UTC (15:00 - 3 hours)
		if formatted != "2024-06-01T12:00:00Z" {
			t.Errorf("expected 2024-06-01T12:00:00Z, got %s", formatted)
		}
	})

	t.Run("time.Time JSON marshaling uses RFC3339", func(t *testing.T) {
		ts := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
		data, err := json.Marshal(ts)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		// JSON marshaling should produce RFC3339 format with Z
		expected := `"2024-06-01T12:00:00Z"`
		if string(data) != expected {
			t.Errorf("expected %s, got %s", expected, string(data))
		}
	})
}
