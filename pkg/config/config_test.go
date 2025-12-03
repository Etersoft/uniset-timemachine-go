package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadJSONAndResolve(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sensors.json")
	content := `{
		"sensors": {
			"Input1_S": 1,
			"Input2_S": 2,
			"Output1_C": 10
		},
		"sets": {
			"inputs": ["Input1_S", "Input2_S"]
		}
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.Sensors) != 3 {
		t.Fatalf("expected 3 sensors, got %d", len(cfg.Sensors))
	}

	all, err := cfg.Resolve("ALL")
	if err != nil {
		t.Fatalf("Resolve ALL failed: %v", err)
	}
	if !reflect.DeepEqual(all, []int64{1, 2, 10}) {
		t.Fatalf("Resolve ALL returned %v", all)
	}

	inputs, err := cfg.Resolve("inputs")
	if err != nil {
		t.Fatalf("Resolve inputs failed: %v", err)
	}
	if !reflect.DeepEqual(inputs, []int64{1, 2}) {
		t.Fatalf("Resolve inputs returned %v", inputs)
	}

	single, err := cfg.Resolve("Output1_C")
	if err != nil {
		t.Fatalf("Resolve single sensor failed: %v", err)
	}
	if !reflect.DeepEqual(single, []int64{10}) {
		t.Fatalf("unexpected single resolve result: %v", single)
	}

	pattern, err := cfg.Resolve("Input*_S")
	if err != nil {
		t.Fatalf("Resolve pattern failed: %v", err)
	}
	if !reflect.DeepEqual(pattern, []int64{1, 2}) {
		t.Fatalf("Resolve pattern returned %v", pattern)
	}

	listPattern, err := cfg.Resolve("Input1_S,Output*_C")
	if err != nil {
		t.Fatalf("Resolve list pattern failed: %v", err)
	}
	if !reflect.DeepEqual(listPattern, []int64{1, 10}) {
		t.Fatalf("Resolve list pattern returned %v", listPattern)
	}

	if name, ok := cfg.NameByID(2); !ok || name != "Input2_S" {
		t.Fatalf("NameByID failed: %v %v", name, ok)
	}
	if id, ok := cfg.IDByName("Input1_S"); !ok || id != 1 {
		t.Fatalf("IDByName failed: %d %v", id, ok)
	}

	_, err = cfg.Resolve("missing")
	if err == nil {
		t.Fatal("expected error for missing selector")
	}

	if _, err := cfg.Resolve("NoMatch*"); err == nil {
		t.Fatal("expected error for unmatched pattern")
	}
}

func TestLoadXMLMissingID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sensors.xml")
	// idfromfile="1" но id не указан - должна быть ошибка
	content := `<?xml version="1.0" encoding="utf-8"?>
<uniset>
	<sensors>
		<item name="Sensor1" idfromfile="1" textname="Sensor without ID"/>
	</sensors>
</uniset>`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for sensor with idfromfile=1 but no id attribute")
	}
	if got := err.Error(); !strings.Contains(got, "no id attribute") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadXMLWithIDFromFile0(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sensors.xml")
	// idfromfile="0" - ID генерируется из hash32(name)
	content := `<?xml version="1.0" encoding="utf-8"?>
<uniset>
	<sensors>
		<item name="TestSensor" idfromfile="0" textname="Test sensor"/>
	</sensors>
</uniset>`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	// Проверяем, что ID сгенерирован из hash32(name)
	expectedID := int64(Hash32ForName("TestSensor"))
	if id, ok := cfg.Sensors["TestSensor"]; !ok || id != expectedID {
		t.Fatalf("expected generated ID %d, got %d", expectedID, id)
	}
}

func TestLoadXMLWithExplicitID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sensors.xml")
	// Обычный случай с явным ID
	content := `<?xml version="1.0" encoding="utf-8"?>
<uniset>
	<sensors>
		<item id="123" name="MySensor" textname="Sensor with explicit ID"/>
	</sensors>
</uniset>`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if id, ok := cfg.Sensors["MySensor"]; !ok || id != 123 {
		t.Fatalf("expected ID 123, got %d", id)
	}
}

func TestLoadXMLWithGlobalIDFromFile0(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sensors.xml")
	// idfromfile="0" на уровне ObjectsMap - все датчики без явного ID
	content := `<?xml version="1.0" encoding="utf-8"?>
<UNISETPLC>
  <ObjectsMap idfromfile="0">
    <sensors name="Sensors">
      <item name="Sensor1" textname="Датчик 1" iotype="DI"/>
      <item name="Sensor2" textname="Датчик 2" iotype="AI"/>
      <item name="Sensor3" textname="Датчик 3" iotype="AO"/>
    </sensors>
  </ObjectsMap>
</UNISETPLC>`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	// Проверяем, что все датчики загружены
	if len(cfg.Sensors) != 3 {
		t.Fatalf("expected 3 sensors, got %d", len(cfg.Sensors))
	}

	// Проверяем, что ID сгенерированы из hash32(name)
	for _, name := range []string{"Sensor1", "Sensor2", "Sensor3"} {
		expectedID := int64(Hash32ForName(name))
		if id, ok := cfg.Sensors[name]; !ok {
			t.Fatalf("sensor %q not found", name)
		} else if id != expectedID {
			t.Fatalf("sensor %q: expected generated ID %d, got %d", name, expectedID, id)
		}
	}

	// Проверяем метаданные
	if meta, ok := cfg.SensorMeta["Sensor1"]; !ok {
		t.Fatal("SensorMeta for Sensor1 not found")
	} else if meta.IOType != "DI" {
		t.Fatalf("expected iotype DI, got %s", meta.IOType)
	}
}
