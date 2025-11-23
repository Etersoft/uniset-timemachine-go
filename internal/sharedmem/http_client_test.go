package sharedmem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHTTPClientSendBuildsSetRequest(t *testing.T) {
	var capturedPath string
	var capturedQuery string

	client := &HTTPClient{
		BaseURL:  "http://example.com",
		Supplier: "ProcA",
		HTTP: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				capturedPath = req.URL.Path
				capturedQuery = req.URL.RawQuery
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     http.StatusText(http.StatusOK),
					Body:       io.NopCloser(strings.NewReader("ok")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	payload := StepPayload{
		StepID: 1,
		Updates: []SensorUpdate{
			{ID: 42, Value: 10.5},
			{ID: 77, Value: 0},
		},
	}

	if err := client.Send(context.Background(), payload); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if capturedPath != "/set" {
		t.Fatalf("expected /set path, got %s", capturedPath)
	}
	expectedOrder := []string{"supplier=ProcA", "id42=10.5", "id77=0"}
	for i, token := range expectedOrder {
		if !strings.Contains(capturedQuery, token) {
			t.Fatalf("query %q does not contain %q", capturedQuery, token)
		}
		switch i {
		case 0:
			if !strings.HasPrefix(capturedQuery, token) {
				t.Fatalf("supplier should go first: %s", capturedQuery)
			}
		default:
			prev := expectedOrder[i-1]
			if strings.Index(capturedQuery, token) < strings.Index(capturedQuery, prev) {
				t.Fatalf("order mismatch: %s", capturedQuery)
			}
		}
	}
}

func TestBuildSetQueryFormatter(t *testing.T) {
	custom := func(update SensorUpdate) string {
		return "name_" + strconv.FormatInt(update.ID, 10)
	}
	query, err := buildSetQuery("", []SensorUpdate{{ID: 1, Value: 2.5}}, custom)
	if err != nil {
		t.Fatalf("buildSetQuery returned error: %v", err)
	}
	if query != "?name_1=2.5" {
		t.Fatalf("unexpected query: %s", query)
	}
}

func TestHTTPClientSetAndGetFlow(t *testing.T) {
	state := map[string]float64{}

	client := &HTTPClient{
		BaseURL:  "http://example.com",
		Supplier: "Tester",
		HTTP: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				for key, values := range req.URL.Query() {
					if key == "supplier" {
						continue
					}
					val, err := strconv.ParseFloat(values[0], 64)
					if err != nil {
						return &http.Response{
							StatusCode: http.StatusBadRequest,
							Status:     http.StatusText(http.StatusBadRequest),
							Body:       io.NopCloser(strings.NewReader("bad value")),
							Header:     make(http.Header),
							Request:    req,
						}, nil
					}
					state[key] = val
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     http.StatusText(http.StatusOK),
					Body:       io.NopCloser(strings.NewReader("ok")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}
	payload := StepPayload{
		Updates: []SensorUpdate{
			{ID: 42, Value: 12.5},
		},
	}
	if err := client.Send(context.Background(), payload); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	if got := state["id42"]; got != 12.5 {
		t.Fatalf("expected id42=12.5, got %v", got)
	}
}

func TestHTTPClientSendHandlesHTTPError(t *testing.T) {
	client := &HTTPClient{
		BaseURL:  "http://example.com",
		Supplier: "BadSM",
		HTTP: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Status:     "500 internal error",
					Body:       io.NopCloser(strings.NewReader("failed to set")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}
	err := client.Send(context.Background(), StepPayload{
		Updates: []SensorUpdate{{ID: 1, Value: 1}},
	})
	if err == nil || !strings.Contains(err.Error(), "status=500") || !strings.Contains(err.Error(), "failed to set") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPClientSendContextTimeout(t *testing.T) {
	client := &HTTPClient{
		BaseURL:  "http://example.com",
		Supplier: "Timeout",
		Timeout:  time.Millisecond * 2,
		HTTP: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				<-req.Context().Done()
				return nil, req.Context().Err()
			}),
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*5)
	defer cancel()

	err := client.Send(ctx, StepPayload{
		Updates: []SensorUpdate{{ID: 10, Value: 2}},
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded error, got %v", err)
	}
}

func TestHTTPClientRetry(t *testing.T) {
	var calls int
	client := &HTTPClient{
		BaseURL: "http://example.com",
		Retry:   1,
		HTTP: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return nil, fmt.Errorf("temporary")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     http.StatusText(http.StatusOK),
					Body:       io.NopCloser(strings.NewReader("ok")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}
	err := client.Send(context.Background(), StepPayload{
		Updates: []SensorUpdate{{ID: 1, Value: 1}},
	})
	if err != nil {
		t.Fatalf("expected retry success, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 attempts, got %d", calls)
	}
}

func TestHTTPClientWorkerQueue(t *testing.T) {
	reqCh := make(chan *http.Request, 2)
	releaseCh := make(chan struct{})
	client := &HTTPClient{
		BaseURL:     "http://example.com",
		WorkerCount: 1,
		QueueSize:   2,
		HTTP: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				reqCh <- req
				<-releaseCh
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     http.StatusText(http.StatusOK),
					Body:       io.NopCloser(strings.NewReader("ok")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 2)
	go func() {
		errCh <- client.Send(ctx, StepPayload{Updates: []SensorUpdate{{ID: 1, Value: 1}}})
	}()
	go func() {
		errCh <- client.Send(ctx, StepPayload{Updates: []SensorUpdate{{ID: 2, Value: 2}}})
	}()

	// ждём первый запрос и отпускаем транспорт, чтобы воркер взял второй
	select {
	case <-reqCh:
	case <-time.After(time.Second):
		t.Fatalf("request 1 not received")
	}
	releaseCh <- struct{}{}
	select {
	case <-reqCh:
	case <-time.After(time.Second):
		t.Fatalf("request 2 not received")
	}
	releaseCh <- struct{}{}

	for i := 0; i < 2; i++ {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("Send %d error: %v", i+1, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("Send %d timeout", i+1)
		}
	}
}
