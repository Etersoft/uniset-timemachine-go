package sharedmem

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HTTPClient отправляет изменения датчиков в SharedMemory HTTP API (/set).
type HTTPClient struct {
	BaseURL        string
	Supplier       string
	HTTP           *http.Client
	Logger         *log.Logger
	ParamFormatter ParamFormatter
	Timeout        time.Duration
	Retry          int
	WorkerCount    int
	QueueSize      int

	mu            sync.Mutex
	totalDuration time.Duration
	totalCalls    int64

	startWorkers sync.Once
	queue        chan workItem
}

type workItem struct {
	ctx     context.Context
	updates []SensorUpdate
	resp    chan error
}

// Send переводит StepPayload в запрос /set.
func (c *HTTPClient) Send(ctx context.Context, payload StepPayload) error {
	if len(payload.Updates) == 0 {
		return nil
	}
	if c.WorkerCount > 0 {
		return c.enqueue(ctx, payload.Updates)
	}
	return c.set(ctx, payload.Updates)
}

func (c *HTTPClient) enqueue(ctx context.Context, updates []SensorUpdate) error {
	c.startWorkers.Do(func() {
		size := c.QueueSize
		if size <= 0 {
			size = c.WorkerCount * 4
		}
		if size <= 0 {
			size = 8
		}
		c.queue = make(chan workItem, size)
		for i := 0; i < c.WorkerCount; i++ {
			go c.worker()
		}
	})
	item := workItem{
		ctx:     ctx,
		updates: updates,
		resp:    make(chan error, 1),
	}
	select {
	case c.queue <- item:
	case <-ctx.Done():
		return ctx.Err()
	default:
		return fmt.Errorf("http client: worker queue is full")
	}
	select {
	case err := <-item.resp:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *HTTPClient) worker() {
	for item := range c.queue {
		err := c.set(item.ctx, item.updates)
		select {
		case item.resp <- err:
		default:
		}
	}
}

func (c *HTTPClient) set(ctx context.Context, updates []SensorUpdate) error {
	if c == nil {
		return fmt.Errorf("http client: nil receiver")
	}
	if c.BaseURL == "" {
		return fmt.Errorf("http client: BaseURL is empty")
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	endpoint, err := joinURL(c.BaseURL, "/set")
	if err != nil {
		return err
	}
	rawQuery, err := buildSetQuery(c.Supplier, updates, c.ParamFormatter)
	if err != nil {
		return err
	}
	return c.sendWithRetry(ctx, httpClient, endpoint, rawQuery)
}

func buildSetQuery(supplier string, updates []SensorUpdate, formatter ParamFormatter) (string, error) {
	if len(updates) == 0 {
		return "", fmt.Errorf("http client: no updates to send")
	}
	if formatter == nil {
		formatter = func(update SensorUpdate) string {
			return fmt.Sprintf("id%d", update.ID)
		}
	}

	var b strings.Builder
	first := true
	writeParam := func(key, value string) {
		if first {
			b.WriteByte('?')
			first = false
		} else {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(key))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(value))
	}

	if supplier != "" {
		writeParam("supplier", supplier)
	}
	for _, upd := range updates {
		key := formatter(upd)
		if key == "" {
			return "", fmt.Errorf("http client: empty parameter name for sensor %d", upd.ID)
		}
		value := strconv.FormatFloat(upd.Value, 'f', -1, 64)
		writeParam(key, value)
	}
	return b.String(), nil
}

func joinURL(base, path string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("http client: parse base URL: %w", err)
	}
	joined, err := url.JoinPath(u.String(), path)
	if err != nil {
		return "", fmt.Errorf("http client: join path: %w", err)
	}
	return joined, nil
}

func (c *HTTPClient) sendWithRetry(ctx context.Context, httpClient *http.Client, endpoint, rawQuery string) error {
	attempts := c.Retry + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		reqCtx := ctx
		var cancel context.CancelFunc
		if c.Timeout > 0 {
			reqCtx, cancel = context.WithTimeout(ctx, c.Timeout)
		}
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint+rawQuery, nil)
		if err != nil {
			if cancel != nil {
				cancel()
			}
			return fmt.Errorf("http client: new request: %w", err)
		}
		start := time.Now()
		resp, err := httpClient.Do(req)
		if cancel != nil {
			defer cancel()
		}
		if err != nil {
			lastErr = fmt.Errorf("http client: do request: %w", err)
			if c.Logger != nil {
				c.Logger.Printf("SM error attempt=%d: %v (elapsed %s)", i+1, err, time.Since(start))
			}
			time.Sleep(backoffDelay(i))
			continue
		}
		defer resp.Body.Close()

		elapsed := time.Since(start)
		var avg time.Duration
		if c.Logger != nil {
			c.mu.Lock()
			c.totalDuration += elapsed
			c.totalCalls++
			if c.totalCalls > 0 {
				avg = time.Duration(int64(c.totalDuration) / c.totalCalls)
			}
			c.Logger.Printf("SM /set %s -> %s (%s, avg %s over %d calls)",
				req.URL.String(), resp.Status, elapsed, avg, c.totalCalls)
			c.mu.Unlock()
		}

		if resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			if c.Logger != nil {
				c.Logger.Printf("SM error body: %s", strings.TrimSpace(string(body)))
			}
			lastErr = fmt.Errorf("http client: /set failed: status=%s body=%s", resp.Status, strings.TrimSpace(string(body)))
			time.Sleep(backoffDelay(i))
			continue
		}
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return lastErr
}

func backoffDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	// простой линейный backoff до 200мс
	delay := time.Duration(attempt) * 50 * time.Millisecond
	if delay > 200*time.Millisecond {
		return 200 * time.Millisecond
	}
	return delay
}
