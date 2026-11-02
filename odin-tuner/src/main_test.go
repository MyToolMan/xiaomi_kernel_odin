package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSetAndRestore(t *testing.T) {
	root := t.TempDir()
	node := filepath.Join(root, "scaling_max_freq")
	if err := os.WriteFile(node, []byte("1804800\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := &daemonState{
		backupDir: filepath.Join(root, "backup"),
		specs:     make(map[string]paramSpec),
	}
	if err := os.Mkdir(state.backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := paramSpec{
		Name:      "cpu_test_max_freq",
		Path:      node,
		Min:       300000,
		Max:       1804800,
		SafeValue: 1209600,
		Available: map[int64]struct{}{1209600: {}, 1804800: {}},
	}
	if err := validateValue(spec, 2000000); err == nil {
		t.Fatal("expected out-of-range value to be rejected")
	}
	if _, err := state.setParam(spec, 1209600); err != nil {
		t.Fatal(err)
	}
	if got, _ := readString(node); got != "1209600" {
		t.Fatalf("node value = %q", got)
	}
	if _, err := os.Stat(filepath.Join(state.backupDir, spec.Name+".json")); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	result, err := state.restoreAll()
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Restored) != 1 {
		t.Fatalf("unexpected restore result: %+v", result)
	}
	if got, _ := readString(node); got != "1804800" {
		t.Fatalf("restored node value = %q", got)
	}
	if entries, _ := os.ReadDir(state.backupDir); len(entries) != 0 {
		t.Fatal("backup was not removed after verified restore")
	}
}

func TestFailedWriteLeavesOriginalAndNoBackup(t *testing.T) {
	root := t.TempDir()
	node := filepath.Join(root, "scaling_max_freq")
	if err := os.WriteFile(node, []byte("1804800\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	state := &daemonState{
		backupDir: filepath.Join(root, "backup"),
		specs:     make(map[string]paramSpec),
	}
	if err := os.Mkdir(state.backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := paramSpec{
		Name:      "cpu_test_max_freq",
		Path:      node,
		Min:       300000,
		Max:       1804800,
		SafeValue: 1209600,
		Available: map[int64]struct{}{1209600: {}, 1804800: {}},
	}
	if _, err := state.setParam(spec, 1209600); err == nil {
		t.Fatal("expected write failure")
	}
	if got, _ := readString(node); got != "1804800" {
		t.Fatalf("original value changed: %q", got)
	}
	if entries, _ := os.ReadDir(state.backupDir); len(entries) != 0 {
		t.Fatal("backup should be removed when the original value is confirmed")
	}
}

func TestTemperatureSummaryWhitelist(t *testing.T) {
	value := func(v float64) *float64 { return &v }
	zones := []thermalReading{
		{Type: "cpu-0-0-step", TempC: value(40), Mode: "enabled"},
		{Type: "cpu-0-3-usr", TempC: value(44), Mode: "enabled"},
		{Type: "cpu-1-0-step", TempC: value(50), Mode: "enabled"},
		{Type: "cpu-1-4-usr", TempC: value(55), Mode: "enabled"},
		{Type: "gpuss-0-step", TempC: value(60), Mode: "enabled"},
		{Type: "gpuss-1-step", TempC: value(62), Mode: "enabled"},
		{Type: "battery", TempC: value(35)},
		{Type: "xo_therm", TempC: value(38)},
		{Type: "quiet_therm", TempC: value(39)},
		{Type: "ddr-usr", TempC: value(42)},
		{Type: "unrelated", TempC: value(99), Mode: "enabled"},
		{Type: "gpuss-0-step", TempC: value(-40), Mode: "enabled"},
		{Type: "battery", TempC: value(55), Mode: "disabled"},
	}
	summary := summarizeThermal(zones)
	assertFloat := func(name string, got *float64, want float64) {
		t.Helper()
		if got == nil || *got != want {
			t.Fatalf("%s = %v, want %.1f", name, got, want)
		}
	}
	assertFloat("little", summary.CPULittle, 44)
	assertFloat("mid", summary.CPUMid, 50)
	assertFloat("prime", summary.CPUPrime, 55)
	assertFloat("gpu", summary.GPU, 62)
	assertFloat("battery", summary.Battery, 35)
	assertFloat("board", summary.Board, 39)
	assertFloat("memory", summary.Memory, 42)
}

func TestConfigClamp(t *testing.T) {
	config := normalizeConfig(watchdogConfig{
		CPUThrottleTemp: 120,
		GPUThrottleTemp: 90,
		HardLimitTemp:   120,
	})
	if config.HardLimitTemp != 95 {
		t.Fatalf("hard limit = %.1f", config.HardLimitTemp)
	}
	if config.CPUThrottleTemp != 95 || config.GPUThrottleTemp != 90 {
		t.Fatalf("unexpected clamped config: %+v", config)
	}
}

func TestWebTokenRequired(t *testing.T) {
	handler := requireWebToken("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	withoutToken := httptest.NewRecorder()
	handler.ServeHTTP(withoutToken, httptest.NewRequest(http.MethodGet, "/", nil))
	if withoutToken.Code != http.StatusUnauthorized {
		t.Fatalf("without token status = %d", withoutToken.Code)
	}

	withToken := httptest.NewRecorder()
	handler.ServeHTTP(withToken, httptest.NewRequest(http.MethodGet, "/?token=secret", nil))
	if withToken.Code != http.StatusNoContent {
		t.Fatalf("with token status = %d", withToken.Code)
	}
}

func TestWatchdogCapsAndRecordsFrequency(t *testing.T) {
	root := t.TempDir()
	node := filepath.Join(root, "scaling_max_freq")
	backupDir := filepath.Join(root, "backup")
	if err := os.Mkdir(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(node, []byte("2995200\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := paramSpec{
		Name:      "cpu_prime_max_freq",
		Path:      node,
		Min:       844800,
		Max:       2995200,
		SafeValue: 1900800,
		Available: map[int64]struct{}{1900800: {}, 2995200: {}},
	}
	state := &daemonState{
		backupDir:  backupDir,
		eventsPath: filepath.Join(root, "events.jsonl"),
		specs:      map[string]paramSpec{spec.Name: spec},
	}
	state.applySafeCaps("cpu_throttle", "cpu_prime", 86.5, []string{spec.Name})
	if got, _ := readString(node); got != "1900800" {
		t.Fatalf("capped value = %q", got)
	}
	data, err := os.ReadFile(state.eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	var event watchdogEventInfo
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if event.Sensor != "cpu_prime" || event.TemperatureC != 86.5 ||
		event.Before != 2995200 || event.After != 1900800 {
		t.Fatalf("unexpected event: %+v", event)
	}
}
