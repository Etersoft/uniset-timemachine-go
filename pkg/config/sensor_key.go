package config

import (
	"github.com/aviddiviner/go-murmur"
	"github.com/go-faster/city"
)

// SensorKey представляет ключ датчика с уникальным hash идентификатором.
// Совместимость с UniSet:
//   - Hash (int64) использует CityHash64 - соответствует uniset::hash64()
//   - Hash32ForName (uint32) использует MurmurHash2 seed=0 - соответствует uniset::hash32()
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

// HashForName вычисляет CityHash64 для имени датчика.
// Совместимо с uniset::hash64().
func HashForName(name string) int64 {
	return int64(city.Hash64([]byte(name)))
}

// Hash32ForName вычисляет MurmurHash2 (32-bit, seed=0) для имени датчика.
// Совместимо с uniset::hash32().
// Используется для генерации config_id когда idfromfile="0".
func Hash32ForName(name string) uint32 {
	return murmur.MurmurHash2([]byte(name), 0)
}
