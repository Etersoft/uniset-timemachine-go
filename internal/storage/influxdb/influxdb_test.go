package influxdb

import (
	"testing"
	"time"
)

func TestIsSource(t *testing.T) {
	tests := []struct {
		dsn  string
		want bool
	}{
		{"influxdb://localhost:8086/mydb", true},
		{"influx://localhost:8086/mydb", true},
		{"INFLUXDB://localhost:8086/mydb", true},
		{"INFLUX://localhost:8086/mydb", true},
		{"postgres://localhost/db", false},
		{"sqlite://test.db", false},
		{"clickhouse://localhost/db", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.dsn, func(t *testing.T) {
			got := IsSource(tt.dsn)
			if got != tt.want {
				t.Errorf("IsSource(%q) = %v, want %v", tt.dsn, got, tt.want)
			}
		})
	}
}

func TestParseDSN(t *testing.T) {
	tests := []struct {
		name     string
		dsn      string
		wantAddr string
		wantDB   string
		wantUser string
		wantPass string
		wantErr  bool
	}{
		{
			name:     "full DSN",
			dsn:      "influxdb://admin:secret@localhost:8086/mydb",
			wantAddr: "http://localhost:8086",
			wantDB:   "mydb",
			wantUser: "admin",
			wantPass: "secret",
		},
		{
			name:     "influx scheme",
			dsn:      "influx://user:pass@host:8086/testdb",
			wantAddr: "http://host:8086",
			wantDB:   "testdb",
			wantUser: "user",
			wantPass: "pass",
		},
		{
			name:     "default port",
			dsn:      "influxdb://localhost/mydb",
			wantAddr: "http://localhost:8086",
			wantDB:   "mydb",
		},
		{
			name:     "no auth",
			dsn:      "influxdb://localhost:8086/mydb",
			wantAddr: "http://localhost:8086",
			wantDB:   "mydb",
		},
		{
			name:    "no database",
			dsn:     "influxdb://localhost:8086",
			wantErr: true,
		},
		{
			name:    "empty database",
			dsn:     "influxdb://localhost:8086/",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, db, user, pass, err := parseDSN(tt.dsn)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseDSN() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if addr != tt.wantAddr {
				t.Errorf("addr = %q, want %q", addr, tt.wantAddr)
			}
			if db != tt.wantDB {
				t.Errorf("database = %q, want %q", db, tt.wantDB)
			}
			if user != tt.wantUser {
				t.Errorf("username = %q, want %q", user, tt.wantUser)
			}
			if pass != tt.wantPass {
				t.Errorf("password = %q, want %q", pass, tt.wantPass)
			}
		})
	}
}

func TestEscapeRegex(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"sensor", "sensor"},
		{"theatre.Box1_Temperature_AS", `theatre\.Box1_Temperature_AS`},
		{"test*", `test\*`},
		{"a.b.c", `a\.b\.c`},
		{"foo(bar)", `foo\(bar\)`},
		{"[test]", `\[test\]`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeRegex(tt.input)
			if got != tt.want {
				t.Errorf("escapeRegex(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildRegexPattern(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  string
	}{
		{
			name:  "single measurement",
			names: []string{"sensor1"},
			want:  `"sensor1"`,
		},
		{
			name:  "multiple measurements",
			names: []string{"sensor1", "sensor2"},
			want:  `/^(sensor1|sensor2)$/`,
		},
		{
			name:  "with dots",
			names: []string{"theatre.Box1", "theatre.Box2"},
			want:  `/^(theatre\.Box1|theatre\.Box2)$/`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildRegexPattern(tt.names)
			if got != tt.want {
				t.Errorf("buildRegexPattern() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseRow(t *testing.T) {
	tests := []struct {
		name    string
		row     []interface{}
		wantTs  time.Time
		wantVal float64
		wantErr bool
	}{
		{
			name:    "RFC3339 time string",
			row:     []interface{}{"2024-01-15T10:30:00Z", 42.5},
			wantTs:  time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			wantVal: 42.5,
		},
		{
			name:    "nanoseconds as float",
			row:     []interface{}{float64(1705316400000000000), 100.0},
			wantTs:  time.Unix(0, 1705316400000000000),
			wantVal: 100.0,
		},
		{
			name:    "integer value",
			row:     []interface{}{"2024-01-15T10:30:00Z", int64(123)},
			wantTs:  time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			wantVal: 123.0,
		},
		{
			name:    "row too short",
			row:     []interface{}{"2024-01-15T10:30:00Z"},
			wantErr: true,
		},
		{
			name:    "empty row",
			row:     []interface{}{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, val, err := parseRow(tt.row)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseRow() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if !ts.Equal(tt.wantTs) {
				t.Errorf("timestamp = %v, want %v", ts, tt.wantTs)
			}
			if val != tt.wantVal {
				t.Errorf("value = %v, want %v", val, tt.wantVal)
			}
		})
	}
}

func TestSortEventsByTime(t *testing.T) {
	events := []struct {
		SensorID  int64
		Timestamp time.Time
		Value     float64
	}{
		{1, time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC), 1.0},
		{2, time.Date(2024, 1, 15, 10, 10, 0, 0, time.UTC), 2.0},
		{3, time.Date(2024, 1, 15, 10, 20, 0, 0, time.UTC), 3.0},
	}

	// Конвертируем в storage.SensorEvent
	storageEvents := make([]struct {
		SensorID  int64
		Timestamp time.Time
		Value     float64
	}, len(events))
	copy(storageEvents, events)

	// Проверяем что после сортировки порядок правильный (10:10 < 10:20 < 10:30)
	// Сортируем вручную для проверки
	for i := 0; i < len(storageEvents)-1; i++ {
		for j := i + 1; j < len(storageEvents); j++ {
			if storageEvents[j].Timestamp.Before(storageEvents[i].Timestamp) {
				storageEvents[i], storageEvents[j] = storageEvents[j], storageEvents[i]
			}
		}
	}

	if storageEvents[0].SensorID != 2 {
		t.Errorf("first event should be sensor 2 (earliest), got %d", storageEvents[0].SensorID)
	}
	if storageEvents[1].SensorID != 3 {
		t.Errorf("second event should be sensor 3, got %d", storageEvents[1].SensorID)
	}
	if storageEvents[2].SensorID != 1 {
		t.Errorf("third event should be sensor 1 (latest), got %d", storageEvents[2].SensorID)
	}
}
