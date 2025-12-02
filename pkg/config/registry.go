package config

import (
	"fmt"
	"sort"
)

// SensorRegistry хранит все датчики и обеспечивает быстрый поиск по hash, имени и configID.
type SensorRegistry struct {
	sensors map[int64]*SensorKey  // hash → sensor
	byName  map[string]int64      // name → hash
	byID    map[int64]int64       // configID → hash (только если есть ID)
	hasIDs  bool                  // true если все датчики имеют ID
}

// NewSensorRegistry создаёт пустой реестр датчиков.
func NewSensorRegistry() *SensorRegistry {
	return &SensorRegistry{
		sensors: make(map[int64]*SensorKey),
		byName:  make(map[string]int64),
		byID:    make(map[int64]int64),
		hasIDs:  true,
	}
}

// Add добавляет датчик в реестр. Возвращает ошибку при коллизии hash.
func (r *SensorRegistry) Add(key *SensorKey) error {
	if existing, exists := r.sensors[key.Hash]; exists {
		if existing.Name != key.Name {
			return fmt.Errorf("hash collision: %q and %q have same hash %d",
				existing.Name, key.Name, key.Hash)
		}
		// Дубликат того же датчика - игнорируем
		return nil
	}

	r.sensors[key.Hash] = key
	r.byName[key.Name] = key.Hash

	if key.ID != nil {
		r.byID[*key.ID] = key.Hash
	} else {
		r.hasIDs = false
	}

	return nil
}

// ByHash возвращает датчик по его hash.
func (r *SensorRegistry) ByHash(hash int64) (*SensorKey, bool) {
	if r == nil {
		return nil, false
	}
	key, ok := r.sensors[hash]
	return key, ok
}

// ByName возвращает датчик по имени.
func (r *SensorRegistry) ByName(name string) (*SensorKey, bool) {
	if r == nil {
		return nil, false
	}
	hash, ok := r.byName[name]
	if !ok {
		return nil, false
	}
	return r.sensors[hash], true
}

// ByConfigID возвращает датчик по ID из конфига.
func (r *SensorRegistry) ByConfigID(id int64) (*SensorKey, bool) {
	if r == nil {
		return nil, false
	}
	hash, ok := r.byID[id]
	if !ok {
		return nil, false
	}
	return r.sensors[hash], true
}

// HasIDs возвращает true, если все датчики в реестре имеют ID из конфига.
func (r *SensorRegistry) HasIDs() bool {
	if r == nil {
		return true
	}
	return r.hasIDs
}

// AllHashes возвращает отсортированный список всех hash датчиков.
func (r *SensorRegistry) AllHashes() []int64 {
	if r == nil {
		return nil
	}
	hashes := make([]int64, 0, len(r.sensors))
	for h := range r.sensors {
		hashes = append(hashes, h)
	}
	sort.Slice(hashes, func(i, j int) bool { return hashes[i] < hashes[j] })
	return hashes
}

// AllHashesSortedByName возвращает список hash датчиков, отсортированных по имени.
func (r *SensorRegistry) AllHashesSortedByName() []int64 {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.byName))
	for name := range r.byName {
		names = append(names, name)
	}
	sort.Strings(names)

	hashes := make([]int64, 0, len(names))
	for _, name := range names {
		hashes = append(hashes, r.byName[name])
	}
	return hashes
}

// Count возвращает количество датчиков в реестре.
func (r *SensorRegistry) Count() int {
	if r == nil {
		return 0
	}
	return len(r.sensors)
}

// All возвращает все датчики в реестре.
func (r *SensorRegistry) All() []*SensorKey {
	if r == nil {
		return nil
	}
	keys := make([]*SensorKey, 0, len(r.sensors))
	for _, key := range r.sensors {
		keys = append(keys, key)
	}
	return keys
}
