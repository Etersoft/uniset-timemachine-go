package sharedmem

import (
	"context"
	"fmt"
	"io"

	"github.com/pv/uniset-timemachine-go/pkg/config"
)

// SensorUpdate описывает новое значение датчика, подготовленное к публикации.
type SensorUpdate struct {
	Hash  int64   // cityhash64(name) - основной идентификатор
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
// Получает hash и registry для определения формата: ID из конфига или name.
type ParamFormatter func(hash int64, registry *config.SensorRegistry) string
