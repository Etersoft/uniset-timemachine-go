package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/pv/uniset-timemachine-go/internal/api"
	"github.com/pv/uniset-timemachine-go/internal/replay"
	"github.com/pv/uniset-timemachine-go/internal/sharedmem"
	"github.com/pv/uniset-timemachine-go/internal/storage"
	"github.com/pv/uniset-timemachine-go/internal/storage/clickhouse"
	"github.com/pv/uniset-timemachine-go/internal/storage/memstore"
	"github.com/pv/uniset-timemachine-go/internal/storage/postgres"
	sqliteStore "github.com/pv/uniset-timemachine-go/internal/storage/sqlite"
	"github.com/pv/uniset-timemachine-go/pkg/config"
)

type options struct {
	configYAML    string
	dbURL         string
	config        string
	sensorSet     string
	from          string
	to            string
	step          time.Duration
	window        time.Duration
	speed         float64
	output        string
	smURL         string
	smSupplier    string
	smParamMode   string
	smParamPrefix string
	chTable       string
	batchSize     int
	httpAddr      string
	wsBatchTime   time.Duration
	sqliteCacheMB int
	sqliteWAL     bool
	sqliteSyncOff bool
	sqliteTempMem bool
	saveOutput    bool
	logFile       string
	verbose       bool
	logCache      bool
	debugLogs     bool
	version       bool
	showRange     bool
	generateCfg   string
}

const version = "2.0.1-dev"

func main() {
	opts := parseFlags()

	if opts.version {
		fmt.Println("timemachine", version)
		return
	}

	if err := configureLogging(opts.logFile); err != nil {
		log.Fatalf("log file: %v", err)
	}

	if opts.generateCfg != "" {
		if err := generateExampleConfig(opts.generateCfg); err != nil {
			log.Fatalf("write example config: %v", err)
		}
		return
	}

	cfg, err := config.Load(opts.config)
	if err != nil {
		log.Fatalf("failed to load config %s: %v", opts.config, err)
	}
	sensors, err := cfg.Resolve(opts.sensorSet)
	if err != nil {
		log.Fatalf("failed to resolve --slist: %v", err)
	}

	fromTs, toTs, err := func() (time.Time, time.Time, error) {
		if opts.httpAddr != "" {
			// В режиме serve диапазон задаётся через API, поэтому флаги from/to могут быть пустыми.
			return parsePeriodOptional(opts.from, opts.to)
		}
		if opts.showRange {
			return time.Time{}, time.Time{}, nil
		}
		return parsePeriodRequired(opts.from, opts.to)
	}()
	if err != nil {
		log.Fatalf("invalid period: %v", err)
	}

	ctx := context.Background()
	store, closer := initStorage(ctx, opts, cfg, sensors, fromTs, toTs)
	if closer != nil {
		defer closer()
	}

	if opts.httpAddr != "" {
		runHTTPServer(ctx, opts, cfg, sensors, store)
		return
	}

	if opts.showRange {
		printRange(ctx, store, sensors)
		return
	}

	fmt.Fprintf(os.Stdout, "timemachine %s — console player (work in progress)\n", version)
	fmt.Fprintf(os.Stdout, "  DB: %s\n  Config: %s\n  Sensors: %d (%s)\n  Period: %s → %s\n  Step: %s\n  Window: %s\n  Speed: %.2fx\n  Output: %s\n",
		opts.dbURL, opts.config, len(sensors), opts.sensorSet, fromTs.Format(time.RFC3339), toTs.Format(time.RFC3339), opts.step, opts.window, opts.speed, opts.output)

	client := initOutputClient(opts, cfg)
	saveAllowed := opts.output == "http" && opts.smURL != "" && opts.smSupplier != ""
	service := replay.Service{
		Storage:  store,
		Output:   client,
		LogCache: opts.logCache,
	}

	params := replay.Params{
		Sensors:    sensors,
		From:       fromTs,
		To:         toTs,
		Step:       opts.step,
		Window:     opts.window,
		Speed:      opts.speed,
		BatchSize:  opts.batchSize,
		SaveOutput: saveAllowed && opts.saveOutput,
	}
	if err := service.Run(ctx, params); err != nil {
		log.Fatalf("replay failed: %v", err)
	}
}

func parseFlags() options {
	var opt options

	flag.StringVar(&opt.configYAML, "config-yaml", "", "path to YAML file with default flag values")
	flag.StringVar(&opt.dbURL, "db", "", "database connection string (postgres://... or file:test.db)")
	flag.StringVar(&opt.config, "confile", "", "path to sensor configuration (XML/JSON)")
	flag.StringVar(&opt.sensorSet, "slist", "ALL", "sensor list or set name from config")
	flag.StringVar(&opt.from, "from", "", "start of playback period (RFC3339)")
	flag.StringVar(&opt.to, "to", "", "end of playback period (RFC3339)")

	flag.DurationVar(&opt.step, "step", time.Second, "playback step (e.g. 1s, 500ms)")
	flag.DurationVar(&opt.window, "window", 5*time.Minute, "preload window from DB")
	flag.Float64Var(&opt.speed, "speed", 1.0, "playback speed multiplier")
	flag.IntVar(&opt.batchSize, "batch-size", 500, "max sensor updates per payload batch")
	flag.StringVar(&opt.output, "output", "stdout", "output: stdout или http://localhost:9191/api/v01/SharedMemory (SharedMemory HTTP endpoint base URL)")
	flag.StringVar(&opt.smSupplier, "sm-supplier", "TimeMachine", "SharedMemory supplier name (only for http output)")
	flag.StringVar(&opt.smParamMode, "sm-param-mode", "id", "SharedMemory parameter mode (id or name)")
	flag.StringVar(&opt.smParamPrefix, "sm-param-prefix", "id", "Prefix for sensor parameters (use empty to send raw IDs)")
	flag.StringVar(&opt.chTable, "ch-table", "main_history", "ClickHouse table name (db.table or table)")
	flag.StringVar(&opt.httpAddr, "http-addr", "", "run HTTP control server on the given addr (e.g. :8080)")
	flag.DurationVar(&opt.wsBatchTime, "ws-batch-time", 100*time.Millisecond, "WebSocket updates batch interval (e.g. 100ms)")
	flag.IntVar(&opt.sqliteCacheMB, "sqlite-cache-mb", 100, "SQLite cache size (MB) for PRAGMA cache_size; 0 to skip")
	flag.BoolVar(&opt.sqliteWAL, "sqlite-wal", true, "Enable SQLite WAL mode (PRAGMA journal_mode=WAL)")
	flag.BoolVar(&opt.sqliteSyncOff, "sqlite-sync-off", true, "Set PRAGMA synchronous=OFF for SQLite")
	flag.BoolVar(&opt.sqliteTempMem, "sqlite-temp-memory", true, "Set PRAGMA temp_store=MEMORY for SQLite")
	flag.BoolVar(&opt.saveOutput, "save-output", false, "save updates to SharedMemory by default (only for --output=http with --sm-url)")
	flag.StringVar(&opt.logFile, "log-file", "", "write logs to file instead of stderr")
	flag.BoolVar(&opt.verbose, "v", false, "verbose logging (SM HTTP requests)")
	flag.BoolVar(&opt.logCache, "log-cache", false, "log replay cache hits/misses")
	flag.BoolVar(&opt.debugLogs, "debug", false, "enable verbose debug logs for HTTP/control")
	flag.BoolVar(&opt.version, "version", false, "print version and exit")
	flag.BoolVar(&opt.showRange, "show-range", false, "print available time range and exit")
	flag.StringVar(&opt.generateCfg, "generate-config", "", "write example YAML config to file (use '-' for stdout); default: config/config-example.yaml")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [options]\n\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Sensor history player prototype. Example:")
		fmt.Fprintf(flag.CommandLine.Output(), "  %s --db postgres://user:pass@host/db --confile sensors.yaml --from 2024-06-01T00:00:00Z --to 2024-06-01T01:00:00Z\n\n", os.Args[0])
		flag.PrintDefaults()
	}

	if cfgPath := findConfigYAML(os.Args[1:]); cfgPath != "" {
		if err := applyYAMLDefaults(cfgPath); err != nil {
			log.Fatalf("failed to apply --config-yaml: %v", err)
		}
		_ = flag.CommandLine.Set("config-yaml", cfgPath)
	}

	flag.Parse()
	return opt
}

func parsePeriodRequired(from, to string) (time.Time, time.Time, error) {
	if from == "" || to == "" {
		return time.Time{}, time.Time{}, fmt.Errorf("--from and --to are required")
	}
	return parsePeriodOptional(from, to)
}

func parsePeriodOptional(from, to string) (time.Time, time.Time, error) {
	if from == "" && to == "" {
		return time.Time{}, time.Time{}, nil
	}
	start, err := time.Parse(time.RFC3339, from)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid --from: %w", err)
	}
	finish, err := time.Parse(time.RFC3339, to)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid --to: %w", err)
	}
	if !finish.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("--to (%s) must be greater than --from (%s)", finish, start)
	}
	return start, finish, nil
}

func initStorage(ctx context.Context, opts options, cfg *config.Config, sensors []int64, from, to time.Time) (storage.Storage, func()) {
	if opts.dbURL == "" {
		return memstore.NewExampleStore(sensors, from, to, opts.step), nil
	}

	if postgres.IsPostgresURL(opts.dbURL) {
		pgStore, err := postgres.New(ctx, postgres.Config{ConnString: opts.dbURL})
		if err != nil {
			log.Fatalf("postgres storage error: %v", err)
		}
		return pgStore, pgStore.Close
	}

	if sqliteStore.IsSource(opts.dbURL) {
		src := sqliteStore.NormalizeSource(opts.dbURL)
		sqlite, err := sqliteStore.New(ctx, sqliteStore.Config{
			Source: src,
			Pragmas: sqliteStore.Pragmas{
				CacheMB:    opts.sqliteCacheMB,
				WAL:        opts.sqliteWAL,
				SyncOff:    opts.sqliteSyncOff,
				TempMemory: opts.sqliteTempMem,
			},
		})
		if err != nil {
			log.Fatalf("sqlite storage error: %v", err)
		}
		return sqlite, sqlite.Close
	}

	if clickhouse.IsSource(opts.dbURL) {
		chStore, err := clickhouse.New(ctx, clickhouse.Config{
			DSN:      opts.dbURL,
			Table:    opts.chTable,
			Resolver: configResolver{cfg: cfg},
		})
		if err != nil {
			log.Fatalf("clickhouse storage error: %v", err)
		}
		return chStore, chStore.Close
	}

	log.Fatalf("unsupported --db value: %s", opts.dbURL)
	return nil, nil
}

func printRange(ctx context.Context, store storage.Storage, sensors []int64) {
	min, max, count, err := store.Range(ctx, sensors, time.Time{}, time.Time{})
	if err != nil {
		log.Fatalf("failed to fetch range: %v", err)
	}
	if min.IsZero() || max.IsZero() {
		fmt.Println("No data range found (possibly no records)")
		return
	}
	fmt.Printf("Available range: %s → %s (sensors: %d)\n", min.Format(time.RFC3339), max.Format(time.RFC3339), count)
}

type configResolver struct {
	cfg *config.Config
}

func (r configResolver) NameByID(id int64) (string, bool) {
	if r.cfg == nil {
		return "", false
	}
	return r.cfg.NameByID(id)
}

func (r configResolver) IDByName(name string) (int64, bool) {
	if r.cfg == nil {
		return 0, false
	}
	return r.cfg.IDByName(name)
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
		flagName := yamlKeyToFlag(key)
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

func initOutputClient(opt options, cfg *config.Config) sharedmem.Client {
	rawOut := opt.output
	lowerOut := strings.ToLower(opt.output)
	if lowerOut == "stdout" || rawOut == "" {
		return &sharedmem.StdoutClient{Writer: os.Stdout}
	}
	if strings.HasPrefix(lowerOut, "http://") || strings.HasPrefix(lowerOut, "https://") {
		if opt.smSupplier == "" {
			log.Fatalf("--sm-supplier is required when --output is http URL")
		}
		var logger *log.Logger
		if opt.verbose {
			logger = log.New(log.Writer(), "[sm] ", log.Flags())
		}
		paramFormatter := makeParamFormatter(opt, cfg)
		return &sharedmem.HTTPClient{
			BaseURL:        rawOut,
			Supplier:       opt.smSupplier,
			ParamFormatter: paramFormatter,
			Logger:         logger,
			BatchSize:      opt.batchSize,
		}
	}
	log.Fatalf("unsupported --output value: %s", opt.output)
	return nil
}

func makeParamFormatter(opt options, cfg *config.Config) sharedmem.ParamFormatter {
	mode := strings.ToLower(opt.smParamMode)
	prefix := opt.smParamPrefix
	switch mode {
	case "id", "":
		return func(update sharedmem.SensorUpdate) string {
			return fmt.Sprintf("%s%d", prefix, update.ID)
		}
	case "name":
		idToName := make(map[int64]string, len(cfg.Sensors))
		for name, id := range cfg.Sensors {
			idToName[id] = name
		}
		return func(update sharedmem.SensorUpdate) string {
			if name, ok := idToName[update.ID]; ok && name != "" {
				return name
			}
			return fmt.Sprintf("%s%d", prefix, update.ID)
		}
	default:
		log.Fatalf("unsupported --sm-param-mode value: %s", opt.smParamMode)
		return nil
	}
}

func runHTTPServer(ctx context.Context, opt options, cfg *config.Config, sensors []int64, store storage.Storage) {
	saveAllowed := (strings.HasPrefix(strings.ToLower(opt.output), "http://") || strings.HasPrefix(strings.ToLower(opt.output), "https://") || opt.output == "") && opt.smSupplier != ""
	service := replay.Service{
		Storage:  store,
		Output:   initOutputClient(opt, cfg),
		LogCache: opt.logCache,
	}
	streamer := api.NewStateStreamer(opt.wsBatchTime)
	manager := api.NewManager(service, sensors, cfg, opt.speed, opt.window, opt.batchSize, streamer, saveAllowed, opt.saveOutput)
	api.SetDebugLogging(opt.debugLogs)
	server := api.NewServer(manager, streamer)
	addr := opt.httpAddr
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("starting HTTP control server on %s", addr)
	if err := server.Listen(ctx, addr); err != nil && err != context.Canceled {
		log.Fatalf("http server error: %v", err)
	}
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

func configureLogging(path string) error {
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	log.SetOutput(f)
	return nil
}

func yamlKeyToFlag(key string) string {
	key = strings.ToLower(key)
	key = strings.ReplaceAll(key, "_", "-")
	mapped := map[string]string{
		"database.dsn":                "db",
		"database.url":                "db",
		"database.table":              "ch-table",
		"database.step":               "step",
		"database.window":             "window",
		"database.speed":              "speed",
		"database.batch-size":         "batch-size",
		"sensors.selector":            "slist",
		"sensors.slist":               "slist",
		"sensors.list":                "slist",
		"sensors.set":                 "slist",
		"sensors.config":              "confile",
		"sensors.file":                "confile",
		"sensors.confile":             "confile",
		"sensors.from":                "from",
		"sensors.to":                  "to",
		"output.mode":                 "output",
		"output.sm-url":               "sm-url",
		"output.sm-supplier":          "sm-supplier",
		"output.sm-param-mode":        "sm-param-mode",
		"output.sm-param-prefix":      "sm-param-prefix",
		"output.batch-size":           "batch-size",
		"output.save":                 "save-output",
		"output.verbose":              "v",
		"database.sqlite.cache-mb":    "sqlite-cache-mb",
		"database.sqlite.wal":         "sqlite-wal",
		"database.sqlite.sync-off":    "sqlite-sync-off",
		"database.sqlite.temp-memory": "sqlite-temp-memory",
		"http-addr":                   "http-addr",
		"http.addr":                   "http-addr",
		"http.address":                "http-addr",
		"server.http-addr":            "http-addr",
		"server.addr":                 "http-addr",
		"logging.cache":               "log-cache",
	}
	if flagName, ok := mapped[key]; ok {
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

func generateExampleConfig(path string) error {
	if path == "" {
		path = "config/config-example.yaml"
	}
	if path == "-" {
		_, err := os.Stdout.WriteString(exampleConfigYAML)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(exampleConfigYAML), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Printf("Example config written to %s\n", path)
	return nil
}

const exampleConfigYAML = `# Пример конфигурации timemachine (все основные поля).

http:
  addr: :9090  # HTTP UI/API. Пусто, если не нужен server-режим.

database:
  # Тип хранилища: clickhouse | postgres | sqlite
  type: clickhouse
  # ClickHouse
  dsn: clickhouse://default:@localhost:9000/uniset
  table: uniset.main_history
  # PostgreSQL (пример)
  # type: postgres
  # dsn: postgres://admin:123@localhost:5432/uniset?sslmode=disable
  # SQLite (пример)
  # type: sqlite
  # dsn: sqlite://sqlite-demo.db
  # Доп. параметры чтения
  window: 15s          # длительность окна подкачки
  step: 1s             # шаг интерполяции (для memstore/sqlite, если не задан через CLI)
  speed: 1             # множитель скорости проигрывания (1 — realtime)
  batch_size: 1024     # макс. обновлений в одном батче отправки
  ws_batch_time: 100ms # слайс времени для батчирования WS
  sqlite_cache_mb: 1000
  sqlite_wal: true
  sqlite_sync_off: true
  sqlite_temp_mem: true

sensors:
  config: config/test.xml
  selector: ALL        # имя набора/маска/список имён/ALL
  from: 2024-06-01T00:00:00Z
  to: 2024-06-01T00:09:55Z
  step: 1s             # шаг интерполяции
  window: 15s          # окно подкачки истории
  speed: 1

output:
  mode: stdout         # stdout | http (SharedMemory)
  sm_url: http://localhost:9191/api/v01/SharedMemory
  sm_supplier: TestProc
  sm_param_mode: id    # id | name
  sm_param_prefix: id
  batch_size: 1024
  verbose: false

logging:
  cache: false
`
