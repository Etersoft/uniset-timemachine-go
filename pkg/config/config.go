package config

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SensorMeta содержит дополнительную информацию о датчике.
type SensorMeta struct {
	ID       int64
	TextName string
	IOType   string
}

// Config описывает связь имён датчиков с их ID и наборы датчиков.
type Config struct {
	Sensors    map[string]int64    `json:"sensors"`
	Sets       map[string][]string `json:"sets"`
	SensorMeta map[string]SensorMeta
	idToName   map[int64]string
	Registry   *SensorRegistry // реестр датчиков с hash идентификаторами
}

// Load загружает конфигурацию датчиков из JSON или XML.
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, fmt.Errorf("config: path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	cfg := &Config{
		Sensors:    map[string]int64{},
		Sets:       map[string][]string{},
		SensorMeta: map[string]SensorMeta{},
		Registry:   NewSensorRegistry(),
	}

	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".json", "":
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("config: failed to decode JSON: %w", err)
		}
	case ".xml":
		if err := parseXMLSensors(cfg, data, filepath.Dir(path)); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("config: format %s is not supported yet", ext)
	}

	if len(cfg.Sensors) == 0 {
		return nil, errors.New("config: sensors list is empty")
	}
	cfg.buildReverse()
	return cfg, nil
}

// Resolve возвращает список hash датчиков согласно селектору.
// Селектор: "ALL", имя набора из Sets, имя отдельного датчика или список через запятую.
// Возвращаемые значения - cityhash64(name).
func (c *Config) Resolve(selector string) ([]int64, error) {
	if c == nil {
		return nil, errors.New("config: configuration is nil")
	}

	// Используем Registry если он заполнен
	if c.Registry != nil && c.Registry.Count() > 0 {
		return c.resolveWithRegistry(selector)
	}

	// Fallback на старую логику для JSON конфигов без Registry
	return c.resolveLegacy(selector)
}

// resolveWithRegistry использует Registry для резолвинга селекторов.
func (c *Config) resolveWithRegistry(selector string) ([]int64, error) {
	if selector == "" || strings.EqualFold(selector, "ALL") {
		return c.Registry.AllHashesSortedByName(), nil
	}

	// Набор из Sets
	if names, ok := c.Sets[selector]; ok {
		return c.hashesFromNames(names)
	}

	// Список через запятую
	if strings.Contains(selector, ",") {
		var hashes []int64
		for _, name := range strings.Split(selector, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			resolved, err := c.resolveSingleToHash(name)
			if err != nil {
				return nil, err
			}
			hashes = append(hashes, resolved...)
		}
		return hashes, nil
	}

	return c.resolveSingleToHash(selector)
}

// resolveSingleToHash резолвит одиночный селектор в hash.
func (c *Config) resolveSingleToHash(selector string) ([]int64, error) {
	// Точное совпадение по имени
	if key, ok := c.Registry.ByName(selector); ok {
		return []int64{key.Hash}, nil
	}

	// Glob паттерн
	if strings.ContainsAny(selector, "*?") {
		return c.hashesFromPattern(selector)
	}

	return nil, fmt.Errorf("config: failed to resolve selector %q", selector)
}

// hashesFromNames возвращает hashes для списка имён.
func (c *Config) hashesFromNames(names []string) ([]int64, error) {
	result := make([]int64, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		key, ok := c.Registry.ByName(name)
		if !ok {
			return nil, fmt.Errorf("config: sensor %q not found", name)
		}
		result = append(result, key.Hash)
	}
	if len(result) == 0 {
		return nil, errors.New("config: result is empty")
	}
	return result, nil
}

// hashesFromPattern возвращает hashes для glob-паттерна.
func (c *Config) hashesFromPattern(pattern string) ([]int64, error) {
	var hashes []int64
	for _, key := range c.Registry.All() {
		ok, err := filepath.Match(pattern, key.Name)
		if err != nil {
			return nil, fmt.Errorf("config: invalid pattern %q: %w", pattern, err)
		}
		if ok {
			hashes = append(hashes, key.Hash)
		}
	}
	if len(hashes) == 0 {
		return nil, fmt.Errorf("config: pattern %q matched nothing", pattern)
	}
	sort.Slice(hashes, func(i, j int) bool { return hashes[i] < hashes[j] })
	return hashes, nil
}

// resolveLegacy - старая логика для конфигов без Registry.
func (c *Config) resolveLegacy(selector string) ([]int64, error) {
	if selector == "" || strings.EqualFold(selector, "ALL") {
		return c.allSensorIDs(), nil
	}

	if ids, ok := c.Sets[selector]; ok {
		return c.idsFromNames(ids)
	}

	if strings.Contains(selector, ",") {
		names := strings.Split(selector, ",")
		var ids []int64
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			resolved, err := c.resolveSingle(name)
			if err != nil {
				return nil, err
			}
			ids = append(ids, resolved...)
		}
		return ids, nil
	}

	return c.resolveSingle(selector)
}

func (c *Config) resolveSingle(selector string) ([]int64, error) {
	if id, ok := c.Sensors[selector]; ok {
		return []int64{id}, nil
	}

	if strings.ContainsAny(selector, "*?") {
		return c.idsFromPattern(selector)
	}

	return nil, fmt.Errorf("config: failed to resolve selector %q", selector)
}

func (c *Config) allSensorIDs() []int64 {
	names := make([]string, 0, len(c.Sensors))
	for name := range c.Sensors {
		names = append(names, name)
	}
	sort.Strings(names)
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		ids = append(ids, c.Sensors[name])
	}
	return ids
}

func (c *Config) idsFromNames(names []string) ([]int64, error) {
	result := make([]int64, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		id, ok := c.Sensors[name]
		if !ok {
			return nil, fmt.Errorf("config: sensor %q not found", name)
		}
		result = append(result, id)
	}
	if len(result) == 0 {
		return nil, errors.New("config: result is empty")
	}
	return result, nil
}

func (c *Config) idsFromPattern(pattern string) ([]int64, error) {
	names := make([]string, 0, len(c.Sensors))
	for name := range c.Sensors {
		names = append(names, name)
	}
	sort.Strings(names)
	var ids []int64
	for _, name := range names {
		ok, err := filepath.Match(pattern, name)
		if err != nil {
			return nil, fmt.Errorf("config: invalid pattern %q: %w", pattern, err)
		}
		if !ok {
			continue
		}
		ids = append(ids, c.Sensors[name])
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("config: pattern %q matched nothing", pattern)
	}
	return ids, nil
}

func (c *Config) buildReverse() {
	c.idToName = make(map[int64]string, len(c.Sensors))
	for name, id := range c.Sensors {
		if _, exists := c.idToName[id]; !exists {
			c.idToName[id] = name
		}
	}
}

// NameByID возвращает имя датчика по ID.
func (c *Config) NameByID(id int64) (string, bool) {
	if c == nil {
		return "", false
	}
	if c.idToName == nil {
		c.buildReverse()
	}
	name, ok := c.idToName[id]
	return name, ok
}

// IDByName возвращает ID датчика по имени.
func (c *Config) IDByName(name string) (int64, bool) {
	if c == nil {
		return 0, false
	}
	id, ok := c.Sensors[name]
	return id, ok
}

// NameByHash возвращает имя датчика по его hash.
func (c *Config) NameByHash(hash int64) (string, bool) {
	if c == nil {
		return "", false
	}
	if c.Registry != nil {
		if key, ok := c.Registry.ByHash(hash); ok {
			return key.Name, true
		}
	}
	// Fallback на старый метод
	return c.NameByID(hash)
}

// KeyByHash возвращает полный SensorKey по hash.
func (c *Config) KeyByHash(hash int64) (*SensorKey, bool) {
	if c == nil || c.Registry == nil {
		return nil, false
	}
	return c.Registry.ByHash(hash)
}

// HasIDs возвращает true, если все датчики в конфиге имеют ID.
func (c *Config) HasIDs() bool {
	if c == nil || c.Registry == nil {
		return true // legacy mode
	}
	return c.Registry.HasIDs()
}

type xmlSensors struct {
	Items    []xmlSensor  `xml:"item"`
	Includes []xmlInclude `xml:"http://www.w3.org/2001/XInclude include"`
}

type xmlInclude struct {
	Href     string `xml:"href,attr"`
	XPointer string `xml:"xpointer,attr"`
}

type xmlSensor struct {
	ID         int64  `xml:"id,attr"`
	IDFromFile string `xml:"idfromfile,attr"` // "0" означает, что ID не задан
	Name       string `xml:"name,attr"`
	TextName   string `xml:"textname,attr"`
	IOType     string `xml:"iotype,attr"`
}

func parseXMLSensors(cfg *Config, data []byte, baseDir string) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("config: XML read error: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if strings.EqualFold(start.Name.Local, "sensors") {
			var block xmlSensors
			if err := decoder.DecodeElement(&block, &start); err != nil {
				return fmt.Errorf("config: failed to parse <sensors>: %w", err)
			}
			addXMLSensors(cfg, block.Items)
			for _, incl := range block.Includes {
				if incl.Href == "" {
					continue
				}
				includePath := incl.Href
				if !filepath.IsAbs(includePath) {
					includePath = filepath.Join(baseDir, includePath)
				}
				if err := loadIncludedSensors(cfg, includePath); err != nil {
					return err
				}
			}
			cfg.buildReverse()
			break
		}
	}
	if len(cfg.Sensors) == 0 {
		return errors.New("config: <sensors> block not found in XML")
	}
	return nil
}
func addXMLSensors(cfg *Config, items []xmlSensor) {
	for _, item := range items {
		if item.Name == "" {
			continue
		}

		// idfromfile="0" означает, что ID не задан в конфиге
		var idPtr *int64
		if item.IDFromFile != "0" && item.ID != 0 {
			id := item.ID
			idPtr = &id
		}

		// Создаём SensorKey и добавляем в Registry
		key := NewSensorKey(item.Name, idPtr)
		if err := cfg.Registry.Add(key); err != nil {
			// Логируем предупреждение о коллизии, пропускаем датчик
			continue
		}

		// Сохраняем в Sensors для совместимости
		if idPtr != nil {
			cfg.Sensors[item.Name] = *idPtr
		} else {
			// Если ID нет, используем hash как ID для совместимости
			cfg.Sensors[item.Name] = key.Hash
		}

		cfg.SensorMeta[item.Name] = SensorMeta{
			ID:       key.Hash, // Используем hash как основной ID
			TextName: item.TextName,
			IOType:   item.IOType,
		}
	}
}

func loadIncludedSensors(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: read include %s: %w", path, err)
	}
	var block struct {
		Items []xmlSensor `xml:"item"`
	}
	if err := xml.Unmarshal(data, &block); err != nil {
		return fmt.Errorf("config: parse include %s: %w", path, err)
	}
	addXMLSensors(cfg, block.Items)
	return nil
}
