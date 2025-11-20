package sharedmem

import (
	"context"
	"fmt"
	"io"
)

// SensorUpdate описывает новое значение датчика, подготовленное к публикации.
type SensorUpdate struct {
	ID    int64
	Value float64
}

// StepPayload — одна пачка изменений для конкретного шага.
type StepPayload struct {
	StepID     int64
	StepTs     string
	BatchID    int
	BatchTotal int
	Updates    []SensorUpdate
}

// Client отправляет данные в процесс SharedMemory.
type Client interface {
	Send(ctx context.Context, payload StepPayload) error
}

// StdoutClient — временная заглушка, печатающая payload в writer.
type StdoutClient struct {
	Writer io.Writer
}

func (c *StdoutClient) Send(_ context.Context, payload StepPayload) error {
	if c.Writer == nil {
		return fmt.Errorf("stdout client: writer is not set")
	}
	_, err := fmt.Fprintf(c.Writer, "STEP %d (%s) batch %d/%d: %+v\n", payload.StepID, payload.StepTs, payload.BatchID, payload.BatchTotal, payload.Updates)
	return err
}

// ParamFormatter позволяет переопределить имя параметра для датчика.
type ParamFormatter func(update SensorUpdate) string
