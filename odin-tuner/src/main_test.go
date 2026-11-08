package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestValidateAllowsHardwareMaximumOmittedFromAvailableList(t *testing.T) {
	spec := paramSpec{
		Min: 844800, Max: 2995200,
		Available: map[int64]struct{}{1900800: {}, 2841600: {}},
	}
	if err := validateValue(spec, 2995200); err != nil {
		t.Fatalf("hardware maximum should be writable: %v", err)
	}
	if err := validateValue(spec, 2900000); err == nil {
		t.Fatal("non-OPP frequency should be rejected")
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
	config, err := normalizeConfig(watchdogConfig{
		CPUThrottleTemp: 120,
		GPUThrottleTemp: 90,
		HardLimitTemp:   120,
		WebAutostart:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.HardLimitTemp != 95 {
		t.Fatalf("hard limit = %.1f", config.HardLimitTemp)
	}
	if config.CPUThrottleTemp != 95 || config.GPUThrottleTemp != 90 {
		t.Fatalf("unexpected clamped config: %+v", config)
	}
	if config.AuthMode != "off" || !config.WebAutostart {
		t.Fatalf("unexpected debug defaults: %+v", config)
	}
}

func TestConfigDefaultsForExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"cpu_throttle_temp":83}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureConfigFile(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"auth_mode": "off"`) ||
		!strings.Contains(string(data), `"web_autostart": true`) ||
		!strings.Contains(string(data), `"startup_profile": "performance"`) ||
		!strings.Contains(string(data), `"enforce_profile": true`) {
		t.Fatalf("missing migrated fields:\n%s", data)
	}
	state := &daemonState{configPath: path}
	config, err := state.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.AuthMode != "off" {
		t.Fatalf("auth mode = %q, want off", config.AuthMode)
	}
	if !config.WebAutostart {
		t.Fatal("web_autostart should default to true")
	}
	if config.StartupProfile != "performance" || !config.EnforceProfile {
		t.Fatalf("unexpected tuning defaults: %+v", config)
	}
}

func TestConfigMigrationConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"cpu_throttle_temp":83}`), 0o600); err != nil {
		t.Fatal(err)
	}
	const workers = 16
	errorsFound := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsFound <- ensureConfigFile(path)
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	state := &daemonState{configPath: path}
	config, err := state.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.CPUThrottleTemp != 83 || config.AuthMode != "off" || !config.WebAutostart {
		t.Fatalf("unexpected migrated config: %+v", config)
	}
}

func TestConfigProfiles(t *testing.T) {
	for _, profile := range []string{"system", "performance", "benchmark", "safe", "manual"} {
		config, err := normalizeConfig(watchdogConfig{StartupProfile: profile, EnforceProfile: true})
		if err != nil {
			t.Fatalf("profile %s: %v", profile, err)
		}
		if config.StartupProfile != profile {
			t.Fatalf("profile = %q, want %q", config.StartupProfile, profile)
		}
	}
	if _, err := normalizeConfig(watchdogConfig{StartupProfile: "maximum-overdrive"}); err == nil {
		t.Fatal("expected invalid profile to be rejected")
	}
}

func TestConfigAuthModes(t *testing.T) {
	tests := []struct {
		name    string
		config  watchdogConfig
		wantErr bool
	}{
		{name: "empty defaults to off", config: watchdogConfig{}},
		{name: "off", config: watchdogConfig{AuthMode: "off"}},
		{name: "fixed", config: watchdogConfig{AuthMode: "fixed", FixedToken: "0123456789abcdef"}},
		{name: "fixed too short", config: watchdogConfig{AuthMode: "fixed", FixedToken: "short"}, wantErr: true},
		{name: "fixed unsafe", config: watchdogConfig{AuthMode: "fixed", FixedToken: "0123456789abcde/"}, wantErr: true},
		{name: "random", config: watchdogConfig{AuthMode: "random"}},
		{name: "invalid", config: watchdogConfig{AuthMode: "sometimes"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeConfig(test.config)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
		})
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

func TestWebAccessModes(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	tests := []struct {
		name       string
		mode       string
		token      string
		requestURL string
		want       int
	}{
		{name: "off no token", mode: "off", requestURL: "/", want: http.StatusNoContent},
		{name: "fixed no token", mode: "fixed", token: "0123456789abcdef", requestURL: "/", want: http.StatusUnauthorized},
		{name: "fixed query token", mode: "fixed", token: "0123456789abcdef", requestURL: "/?token=0123456789abcdef", want: http.StatusNoContent},
		{name: "random header token", mode: "random", token: "random-token-value", requestURL: "/", want: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.requestURL, nil)
			if test.name == "random header token" {
				request.Header.Set("X-ODIN-Token", test.token)
			}
			recorder := httptest.NewRecorder()
			webAccessHandler(test.mode, test.token, next).ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
		})
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

func TestApplyProfileEnforceAndRestore(t *testing.T) {
	root := t.TempDir()
	backupDir := filepath.Join(root, "backup")
	if err := os.Mkdir(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"cpu_little_max_freq": "1209600\n",
		"cpu_big_max_freq":    "1555200\n",
		"cpu_prime_max_freq":  "1900800\n",
		"gpu_max_freq":        "540000000\n",
	}
	specs := map[string]paramSpec{}
	for name, value := range files {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	specs["cpu_little_max_freq"] = paramSpec{
		Name: "cpu_little_max_freq", Path: filepath.Join(root, "cpu_little_max_freq"),
		Min: 300000, Max: 1804800, SafeValue: 1209600,
		Available: map[int64]struct{}{1209600: {}, 1804800: {}},
	}
	specs["cpu_big_max_freq"] = paramSpec{
		Name: "cpu_big_max_freq", Path: filepath.Join(root, "cpu_big_max_freq"),
		Min: 710400, Max: 2419200, SafeValue: 1555200,
		Available: map[int64]struct{}{1555200: {}, 2419200: {}},
	}
	specs["cpu_prime_max_freq"] = paramSpec{
		Name: "cpu_prime_max_freq", Path: filepath.Join(root, "cpu_prime_max_freq"),
		Min: 844800, Max: 2995200, SafeValue: 1900800,
		Available: map[int64]struct{}{1900800: {}, 2995200: {}},
	}
	specs["gpu_max_freq"] = paramSpec{
		Name: "gpu_max_freq", Path: filepath.Join(root, "gpu_max_freq"),
		Min: 315000000, Max: 840000000, SafeValue: 540000000,
		Available: map[int64]struct{}{315000000: {}, 540000000: {}, 840000000: {}},
	}
	state := &daemonState{
		backupDir:  backupDir,
		eventsPath: filepath.Join(root, "events.jsonl"),
		specs:      specs,
	}

	result, err := state.applyProfile("performance")
	if err != nil || !result.OK || len(result.Changed) != 4 {
		t.Fatalf("apply performance: result=%+v err=%v", result, err)
	}
	for name, want := range map[string]string{
		"cpu_little_max_freq": "1804800",
		"cpu_big_max_freq":    "2419200",
		"cpu_prime_max_freq":  "2995200",
		"gpu_max_freq":        "840000000",
	} {
		if got, _ := readString(filepath.Join(root, name)); got != want {
			t.Fatalf("%s = %s, want %s", name, got, want)
		}
	}

	if err := os.WriteFile(specs["cpu_prime_max_freq"].Path, []byte("1900800\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state.enforceActiveProfile()
	if got, _ := readString(specs["cpu_prime_max_freq"].Path); got != "2995200" {
		t.Fatalf("enforcement value = %s", got)
	}

	restored, err := state.applyProfile("system")
	if err != nil || !restored.OK {
		t.Fatalf("restore system: %+v %v", restored, err)
	}
	if got, _ := readString(specs["cpu_little_max_freq"].Path); got != "1209600" {
		t.Fatalf("restored little = %s", got)
	}
	if state.currentProfile() != "system" {
		t.Fatalf("active profile = %s", state.currentProfile())
	}
}

func TestProfileReleasesAndRestoresGPUCoolingState(t *testing.T) {
	root := t.TempDir()
	backupDir := filepath.Join(root, "backup")
	if err := os.Mkdir(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	coolingPath := filepath.Join(root, "gpu_cooling_state")
	if err := os.WriteFile(coolingPath, []byte("4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := paramSpec{
		Name: "gpu_thermal_cooling", Path: coolingPath,
		Min: 0, Max: 4, Step: 1, SafeValue: 4,
	}
	state := &daemonState{
		backupDir:  backupDir,
		eventsPath: filepath.Join(root, "events.jsonl"),
		specs:      map[string]paramSpec{spec.Name: spec},
	}
	result, err := state.applyProfile("performance")
	if err != nil || len(result.Changed) != 1 {
		t.Fatalf("release cooling: result=%+v err=%v", result, err)
	}
	if got, _ := readString(coolingPath); got != "0" {
		t.Fatalf("cooling state = %s, want 0", got)
	}
	state.applySafeCaps("gpu_throttle", "gpu", 86, []string{"gpu_thermal_cooling"})
	if got, _ := readString(coolingPath); got != "4" {
		t.Fatalf("safe cooling state = %s, want 4", got)
	}
}
