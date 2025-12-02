package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/pv/uniset-timemachine-go/internal/sharedmem"
	"github.com/pv/uniset-timemachine-go/pkg/config"
)

type options struct {
	configYAML string
	smURL      string
	supplier   string
	sensorID   int64
	value      float64
	random     bool
	timeout    time.Duration
}

func main() {
	opts := parseFlags()
	if opts.random {
		rand.Seed(time.Now().UnixNano())
		opts.value = rand.Float64()*2000 - 1000
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	client := &sharedmem.HTTPClient{
		BaseURL:  opts.smURL,
		Supplier: opts.supplier,
		ParamFormatter: func(hash int64, _ *config.SensorRegistry) string {
			return strconv.FormatInt(hash, 10)
		},
	}
	payload := sharedmem.StepPayload{
		StepID: 1,
		Updates: []sharedmem.SensorUpdate{
			{Hash: opts.sensorID, Value: opts.value},
		},
	}
	if err := client.Send(ctx, payload); err != nil {
		log.Fatalf("SM set failed: %v", err)
	}

	body, err := smGet(ctx, opts.smURL, opts.sensorID)
	if err != nil {
		log.Fatalf("SM get failed: %v", err)
	}
	fmt.Printf("SM test OK. Sensor %d set to %.4f. GET response:\n%s\n", opts.sensorID, opts.value, body)
}

func parseFlags() options {
	var opt options
	flag.StringVar(&opt.configYAML, "config-yaml", "", "path to YAML file with default flag values")
	flag.StringVar(&opt.smURL, "sm-url", "http://localhost:9191/api/v01/SharedMemory", "SharedMemory endpoint base URL")
	flag.StringVar(&opt.supplier, "supplier", "TimeMachine", "supplier name (required if SM enforces ACL)")
	flag.Int64Var(&opt.sensorID, "sensor-id", 10001, "sensor ID to update")
	flag.Float64Var(&opt.value, "value", 0, "value to set (ignored if --random)")
	flag.BoolVar(&opt.random, "random", true, "generate random value instead of using --value")
	flag.DurationVar(&opt.timeout, "timeout", 5*time.Second, "request timeout")

	if cfgPath := findConfigYAML(os.Args[1:]); cfgPath != "" {
		if err := applyYAMLDefaults(cfgPath); err != nil {
			log.Fatalf("failed to apply --config-yaml: %v", err)
		}
		_ = flag.CommandLine.Set("config-yaml", cfgPath)
	}

	flag.Parse()
	return opt
}

func smGet(ctx context.Context, base string, sensorID int64) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = joinPath(u.Path, "/get")
	q := u.Query()
	q.Set(strconv.FormatInt(sensorID, 10), "")
	q.Set("shortInfo", "")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("SM get failed: %s %s", resp.Status, data)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func joinPath(base, suffix string) string {
	if base == "" || base == "/" {
		return suffix
	}
	if base[len(base)-1] == '/' {
		return base + suffix[1:]
	}
	return base + suffix
}

func findConfigYAML(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--config-yaml=") {
			return strings.TrimPrefix(arg, "--config-yaml=")
		}
		if arg == "--config-yaml" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func applyYAMLDefaults(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	flat := flattenYAML(raw)
	for key, value := range flat {
		flagName := mapYAMLKey(key)
		if flagName == "" {
			flagName = key
		}
		flagDef := flag.Lookup(flagName)
		if flagDef == nil {
			continue
		}
		valStr := formatFlagValue(value)
		if err := flag.CommandLine.Set(flagName, valStr); err != nil {
			return fmt.Errorf("set flag %s: %w", flagName, err)
		}
	}
	return nil
}

func flattenYAML(raw map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{})
	for key, value := range raw {
		flattenYAMLValue(key, value, out)
	}
	return out
}

func flattenYAMLValue(prefix string, value interface{}, out map[string]interface{}) {
	switch val := value.(type) {
	case map[string]interface{}:
		for k, v := range val {
			next := k
			if prefix != "" {
				next = prefix + "." + k
			}
			flattenYAMLValue(next, v, out)
		}
	case map[interface{}]interface{}:
		for k, v := range val {
			keyStr := fmt.Sprintf("%v", k)
			next := keyStr
			if prefix != "" {
				next = prefix + "." + keyStr
			}
			flattenYAMLValue(next, v, out)
		}
	default:
		if prefix != "" {
			out[prefix] = value
		}
	}
}

func mapYAMLKey(key string) string {
	key = strings.ToLower(key)
	key = strings.ReplaceAll(key, "_", "-")
	mappings := map[string]string{
		"output.sm-url":       "sm-url",
		"output.sm-supplier":  "supplier",
		"sm-test.sensor-id":   "sensor-id",
		"sm-test.value":       "value",
		"sm-test.random":      "random",
		"sm-test.timeout":     "timeout",
		"sm.sensor-id":        "sensor-id",
		"sm.value":            "value",
		"sm.supplier":         "supplier",
		"sm.url":              "sm-url",
		"sensors.sensor-id":   "sensor-id",
		"sensors.test-sensor": "sensor-id",
	}
	if flagName, ok := mappings[key]; ok {
		return flagName
	}
	return ""
}

func formatFlagValue(value interface{}) string {
	switch v := value.(type) {
	case time.Time:
		return v.Format(time.RFC3339)
	case *time.Time:
		if v == nil {
			return ""
		}
		return v.Format(time.RFC3339)
	case time.Duration:
		return v.String()
	default:
		return fmt.Sprintf("%v", value)
	}
}
