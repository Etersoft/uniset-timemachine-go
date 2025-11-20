package config

import (
	"os"
	"path/filepath"
	"reflect"
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
