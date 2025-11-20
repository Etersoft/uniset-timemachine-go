package storage

import (
	"context"
	"time"
)

// SensorEvent описывает изменение значения датчика во времени.
type SensorEvent struct {
	SensorID  int64
	Timestamp time.Time
	Value     float64
}

// StreamRequest задаёт параметры подгрузки истории.
type StreamRequest struct {
	Sensors []int64
	From    time.Time
	To      time.Time
	Window  time.Duration
}

// Storage — интерфейс для чтения истории из конкретного хранилища (Postgres, SQLite...).
type Storage interface {
	// Warmup возвращает последнее известное значение каждого датчика перед стартом периода.
	Warmup(ctx context.Context, sensors []int64, from time.Time) ([]SensorEvent, error)
	// Stream запускает потоковую подгрузку изменений в пределах периода.
	Stream(ctx context.Context, req StreamRequest) (<-chan []SensorEvent, <-chan error)
	// Range возвращает минимальный и максимальный timestamp для выбранных датчиков.
	Range(ctx context.Context, sensors []int64) (time.Time, time.Time, error)
}
