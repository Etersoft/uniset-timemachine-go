package config

import "github.com/go-faster/city"

// SensorKey представляет ключ датчика с уникальным hash идентификатором.
type SensorKey struct {
	Name string // имя датчика (всегда есть)
	ID   *int64 // ID из конфига (nil если idfromfile="0")
	Hash int64  // cityhash64(name) - основной внутренний идентификатор
}

// NewSensorKey создаёт новый SensorKey с вычисленным hash.
func NewSensorKey(name string, id *int64) *SensorKey {
	return &SensorKey{
		Name: name,
		ID:   id,
		Hash: int64(city.Hash64([]byte(name))),
	}
}

// HasID возвращает true, если у датчика есть ID из конфига.
func (k *SensorKey) HasID() bool {
	return k.ID != nil
}

// ConfigID возвращает ID из конфига или 0, если ID не задан.
func (k *SensorKey) ConfigID() int64 {
	if k.ID != nil {
		return *k.ID
	}
	return 0
}

// HashForName вычисляет cityhash64 для имени датчика.
func HashForName(name string) int64 {
	return int64(city.Hash64([]byte(name)))
}
