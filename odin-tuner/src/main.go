package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultDataDir   = "/data/adb/odin-tuner"
	watchdogTempC    = 80.0
	watchdogRearmC   = 75.0
	collectionPeriod = 2 * time.Second
)

var (
	cpuDirectories = []string{
		"/sys/devices/system/cpu/cpu0/cpufreq",
		"/sys/devices/system/cpu/cpu4/cpufreq",
		"/sys/devices/system/cpu/cpu7/cpufreq",
	}
	gpuDevfreqCandidates = []string{
		"/sys/class/kgsl/kgsl-3d0/devfreq",
		"/sys/devices/platform/soc/3d00000.qcom,kgsl-3d0/devfreq/3d00000.qcom,kgsl-3d0",
		"/sys/devices/platform/soc/3d00000.qcom,kgsl-3d0/kgsl/kgsl-3d0",
	}
)

type cpuReading struct {
	CPU        string `json:"cpu"`
	CurrentKHz int64  `json:"current_khz"`
	CurrentMHz int64  `json:"current_mhz"`
	MaxKHz     int64  `json:"max_khz"`
	MaxMHz     int64  `json:"max_mhz"`
	Governor   string `json:"governor"`
}

type gpuReading struct {
	CurrentHz  int64  `json:"current_hz"`
	CurrentMHz int64  `json:"current_mhz"`
	MaxHz      int64  `json:"max_hz"`
	MaxMHz     int64  `json:"max_mhz"`
	Governor   string `json:"governor,omitempty"`
}

type thermalReading struct {
	Zone  string   `json:"zone"`
	Type  string   `json:"type"`
	TempC *float64 `json:"temp_c"`
	Mode  string   `json:"mode,omitempty"`
}

type snapshot struct {
	GeneratedAt       string             `json:"generated_at"`
	CPU               []cpuReading       `json:"cpu"`
	GPU               *gpuReading        `json:"gpu"`
	GPUError          string             `json:"gpu_error,omitempty"`
	Thermal           []thermalReading   `json:"thermal"`
	MaxTempC          *float64           `json:"max_temp_c"`
	WatchdogTriggered bool               `json:"watchdog_triggered"`
	LastEvent         *watchdogEventInfo `json:"last_event,omitempty"`
}

type watchdogEventInfo struct {
	Time   string `json:"time"`
	Type   string `json:"type"`
	Detail string `json:"detail"`
}

type paramSpec struct {
	Name      string
	Path      string
	Min       int64
	Max       int64
	SafeValue int64
	Available map[int64]struct{}
}

type backupEntry struct {
	Param     string `json:"param"`
	Path      string `json:"path"`
	Value     string `json:"value"`
	CreatedAt string `json:"created_at"`
}

type daemonState struct {
	dataDir      string
	moduleDir    string
	socketPath   string
	backupDir    string
	eventsPath   string
	snapshotPath string

	writeMu sync.Mutex
	specs   map[string]paramSpec
	latest  atomic.Value
	hot     bool
}

type setRequest struct {
	Param string      `json:"param"`
	Value json.Number `json:"value"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "daemon":
		runDaemon(os.Args[2:])
	case "web":
		runWeb(os.Args[2:])
	case "restore-all":
		runRestoreAll(os.Args[2:])
	case "version":
		fmt.Println("odin-tuner v0.1.0")
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: odin-tuner {daemon|web|restore-all|version} [options]")
	os.Exit(2)
}

func runDaemon(args []string) {
	flags := flag.NewFlagSet("daemon", flag.ExitOnError)
	dataDir := flags.String("data-dir", defaultDataDir, "persistent daemon data directory")
	moduleDir := flags.String("module-dir", "", "Magisk module directory")
	socketPath := flags.String("socket", "", "Unix socket path")
	listenAddr := flags.String("listen", "", "optional loopback TCP API address for verification")
	flags.Parse(args)

	if *socketPath == "" {
		*socketPath = filepath.Join(*dataDir, "odin-tuner.sock")
	}
	state, err := newDaemonState(*dataDir, *moduleDir, *socketPath)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	state.collectOnce()
	go state.collectLoop(ctx)

	server := state.newUnixServer()
	var listener net.Listener
	if *listenAddr != "" {
		listener, err = net.Listen("tcp", *listenAddr)
		log.Printf("verification API listening on tcp %s", *listenAddr)
	} else {
		listener, err = state.startUnixListener()
		log.Printf("daemon listening on %s", *socketPath)
	}
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("daemon HTTP server stopped: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Printf("shutdown requested; restoring managed nodes")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	result, err := state.restoreAll()
	if err != nil || len(result.Failed) > 0 {
		log.Printf("restore during shutdown failed: %v", err)
	}
	_ = os.Remove(*socketPath)
}

func runWeb(args []string) {
	flags := flag.NewFlagSet("web", flag.ExitOnError)
	dataDir := flags.String("data-dir", defaultDataDir, "daemon data directory")
	socketPath := flags.String("socket", "", "daemon Unix socket")
	apiURL := flags.String("api-url", "", "optional daemon HTTP URL for verification")
	listen := flags.String("listen", "127.0.0.1:8787", "HTTP listen address")
	root := flags.String("root", "", "webui directory")
	flags.Parse(args)

	if *socketPath == "" {
		*socketPath = filepath.Join(*dataDir, "odin-tuner.sock")
	}
	if *root == "" {
		*root = "webui"
	}

	client := unixHTTPClient(*socketPath)
	target := "http://unix"
	if *apiURL != "" {
		client = &http.Client{Timeout: 5 * time.Second}
		target = strings.TrimRight(*apiURL, "/")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		response, err := client.Get(target + "/snapshot")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	})
	mux.Handle("/", http.FileServer(http.Dir(*root)))

	server := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("web UI listening on http://%s", *listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func runRestoreAll(args []string) {
	flags := flag.NewFlagSet("restore-all", flag.ExitOnError)
	socketPath := flags.String("socket", filepath.Join(defaultDataDir, "odin-tuner.sock"), "daemon Unix socket")
	flags.Parse(args)

	client := unixHTTPClient(*socketPath)
	response, err := client.Post("http://unix/restore", "application/json", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "restore request failed:", err)
		os.Exit(1)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	fmt.Println(string(body))
	if response.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}

func newDaemonState(dataDir, moduleDir, socketPath string) (*daemonState, error) {
	state := &daemonState{
		dataDir:      dataDir,
		moduleDir:    moduleDir,
		socketPath:   socketPath,
		backupDir:    filepath.Join(dataDir, "backup"),
		eventsPath:   filepath.Join(dataDir, "events.jsonl"),
		snapshotPath: filepath.Join(dataDir, "snapshot.json"),
		specs:        make(map[string]paramSpec),
	}
	for _, dir := range []string{state.dataDir, state.backupDir, filepath.Join(state.dataDir, "logs")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	_ = os.Remove(socketPath)
	state.discoverSpecs()
	return state, nil
}

func (s *daemonState) discoverSpecs() {
	clusterSpecs := []struct {
		name      string
		directory string
		safe      int64
	}{
		{"cpu_little_max_freq", cpuDirectories[0], 1209600},
		{"cpu_big_max_freq", cpuDirectories[1], 1555200},
		{"cpu_prime_max_freq", cpuDirectories[2], 1900800},
	}

	for _, candidate := range clusterSpecs {
		path := filepath.Join(candidate.directory, "scaling_max_freq")
		minValue, err := readInt(filepath.Join(candidate.directory, "scaling_min_freq"))
		if err != nil {
			continue
		}
		maxValue, err := readInt(filepath.Join(candidate.directory, "cpuinfo_max_freq"))
		if err != nil {
			continue
		}
		available := parseIntSet(filepath.Join(candidate.directory, "scaling_available_frequencies"))
		s.specs[candidate.name] = paramSpec{
			Name:      candidate.name,
			Path:      path,
			Min:       minValue,
			Max:       maxValue,
			SafeValue: candidate.safe,
			Available: available,
		}
	}

	gpuDir := findGPUDevfreqDir()
	if gpuDir != "" {
		maxPath := filepath.Join(gpuDir, "max_freq")
		available := parseIntSet(filepath.Join(gpuDir, "available_frequencies"))
		minValue, maxValue := minMax(available)
		if minValue == 0 || maxValue == 0 {
			minValue, _ = readInt(filepath.Join(gpuDir, "min_freq"))
			maxValue, _ = readInt(maxPath)
		}
		if minValue > 0 && maxValue >= minValue {
			s.specs["gpu_max_freq"] = paramSpec{
				Name:      "gpu_max_freq",
				Path:      maxPath,
				Min:       minValue,
				Max:       maxValue,
				SafeValue: safeGPUFrequency(available),
				Available: available,
			}
		}
	}
	log.Printf("discovered %d writable parameter specs", len(s.specs))
}

func (s *daemonState) collectLoop(ctx context.Context) {
	ticker := time.NewTicker(collectionPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.collectOnce()
		}
	}
}

func (s *daemonState) collectOnce() {
	snap := snapshot{
		GeneratedAt: time.Now().Format(time.RFC3339),
		CPU:         readCPU(),
		Thermal:     readThermal(),
	}
	snap.GPU, snap.GPUError = readGPU()
	snap.MaxTempC = maxTemperature(snap.Thermal)
	snap.WatchdogTriggered = s.hot
	snap.LastEvent = s.lastEvent()

	if snap.MaxTempC != nil && *snap.MaxTempC >= watchdogTempC && !s.hot {
		s.hot = true
		snap.WatchdogTriggered = true
		s.enforceSafeCaps(*snap.MaxTempC)
		snap.LastEvent = s.lastEvent()
	} else if snap.MaxTempC != nil && *snap.MaxTempC < watchdogRearmC && s.hot {
		s.hot = false
		snap.WatchdogTriggered = false
		s.recordEvent("watchdog_rearm", fmt.Sprintf("temperature %.1f C below rearm threshold", *snap.MaxTempC))
	}
	s.latest.Store(snap)
	if data, err := json.Marshal(snap); err == nil {
		tmp := s.snapshotPath + ".tmp"
		if os.WriteFile(tmp, append(data, '\n'), 0o600) == nil {
			_ = os.Rename(tmp, s.snapshotPath)
		}
	}
}

func readCPU() []cpuReading {
	directories, _ := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/cpufreq")
	sort.Slice(directories, func(i, j int) bool {
		return cpuNumber(directories[i]) < cpuNumber(directories[j])
	})
	result := make([]cpuReading, 0, len(directories))
	for _, directory := range directories {
		current, err := readInt(filepath.Join(directory, "scaling_cur_freq"))
		if err != nil {
			continue
		}
		maximum, _ := readInt(filepath.Join(directory, "scaling_max_freq"))
		governor, _ := readString(filepath.Join(directory, "scaling_governor"))
		result = append(result, cpuReading{
			CPU:        "cpu" + strconv.Itoa(cpuNumber(directory)),
			CurrentKHz: current,
			CurrentMHz: frequencyMHz(current),
			MaxKHz:     maximum,
			MaxMHz:     frequencyMHz(maximum),
			Governor:   governor,
		})
	}
	return result
}

func readGPU() (*gpuReading, string) {
	gpuDir := findGPUDevfreqDir()
	if gpuDir == "" {
		return nil, "GPU devfreq node not found or inaccessible"
	}
	current, err := readInt(filepath.Join(gpuDir, "cur_freq"))
	if err != nil {
		return nil, "GPU current frequency: " + err.Error()
	}
	maximum, _ := readInt(filepath.Join(gpuDir, "max_freq"))
	governor, _ := readString(filepath.Join(gpuDir, "governor"))
	return &gpuReading{
		CurrentHz:  current,
		CurrentMHz: frequencyMHz(current),
		MaxHz:      maximum,
		MaxMHz:     frequencyMHz(maximum),
		Governor:   governor,
	}, ""
}

func readThermal() []thermalReading {
	paths, _ := filepath.Glob("/sys/class/thermal/thermal_zone*")
	sort.Slice(paths, func(i, j int) bool {
		return zoneNumber(paths[i]) < zoneNumber(paths[j])
	})
	result := make([]thermalReading, 0, len(paths))
	for _, directory := range paths {
		raw, err := readString(filepath.Join(directory, "temp"))
		if err != nil || raw == "" {
			continue
		}
		millidegrees, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			continue
		}
		value := float64(millidegrees) / 1000
		zoneType, _ := readString(filepath.Join(directory, "type"))
		mode, _ := readString(filepath.Join(directory, "mode"))
		result = append(result, thermalReading{
			Zone:  filepath.Base(directory),
			Type:  zoneType,
			TempC: &value,
			Mode:  mode,
		})
	}
	return result
}

func maxTemperature(zones []thermalReading) *float64 {
	var maximum *float64
	for _, zone := range zones {
		if zone.TempC == nil || *zone.TempC < -40 || *zone.TempC > 200 {
			continue
		}
		if maximum == nil || *zone.TempC > *maximum {
			value := *zone.TempC
			maximum = &value
		}
	}
	return maximum
}

func (s *daemonState) enforceSafeCaps(temperature float64) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	names := make([]string, 0, len(s.specs))
	for name := range s.specs {
		names = append(names, name)
	}
	sort.Strings(names)
	details := make([]string, 0, len(names))
	for _, name := range names {
		spec := s.specs[name]
		if spec.SafeValue == 0 {
			continue
		}
		current, err := readInt(spec.Path)
		if err != nil {
			details = append(details, fmt.Sprintf("%s unavailable", name))
			continue
		}
		if current <= spec.SafeValue {
			continue
		}
		if _, err := s.setParamLocked(spec, spec.SafeValue); err != nil {
			details = append(details, fmt.Sprintf("%s failed: %v", name, err))
		} else {
			details = append(details, fmt.Sprintf("%s capped to %d", name, spec.SafeValue))
		}
	}
	s.recordEvent("thermal_cap", fmt.Sprintf("temperature %.1f C; %s", temperature, strings.Join(details, "; ")))
}

func (s *daemonState) newUnixServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		value := s.latest.Load()
		if value == nil {
			http.Error(w, "snapshot not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(value)
	})
	mux.HandleFunc("/set", s.handleSet)
	mux.HandleFunc("/restore", s.handleRestore)

	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

func (s *daemonState) startUnixListener() (net.Listener, error) {
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(s.socketPath, 0o660); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

func (s *daemonState) handleSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
	decoder.UseNumber()
	var request setRequest
	if err := decoder.Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	value, err := request.Value.Int64()
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "value must be an integer")
		return
	}
	spec, ok := s.specs[request.Param]
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "unsupported or inaccessible parameter")
		return
	}
	if err := validateValue(spec, value); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := s.setParam(spec, value)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *daemonState) setParam(spec paramSpec, value int64) (map[string]any, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.setParamLocked(spec, value)
}

func (s *daemonState) setParamLocked(spec paramSpec, value int64) (map[string]any, error) {
	original, err := readString(spec.Path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", spec.Path, err)
	}
	target := strconv.FormatInt(value, 10)
	if original == target {
		return map[string]any{
			"ok":       true,
			"param":    spec.Name,
			"path":     spec.Path,
			"previous": original,
			"value":    value,
			"verified": true,
			"changed":  false,
		}, nil
	}
	backupPath := filepath.Join(s.backupDir, spec.Name+".json")
	created, err := s.ensureBackup(backupPath, spec, original)
	if err != nil {
		return nil, err
	}

	if err := writeVerified(spec.Path, target, target); err != nil {
		rollbackErr := writeVerified(spec.Path, original, original)
		if rollbackErr == nil {
			if created {
				_ = os.Remove(backupPath)
			}
			return nil, fmt.Errorf("write failed; original value confirmed: %w", err)
		}
		actual, readErr := readString(spec.Path)
		if readErr == nil && actual == original {
			if created {
				_ = os.Remove(backupPath)
			}
			return nil, fmt.Errorf("write failed; original value remained unchanged: %w", err)
		}
		if readErr != nil {
			return nil, fmt.Errorf("write failed: %v; rollback read failed: %v", err, readErr)
		}
		if rollbackErr != nil {
			return nil, fmt.Errorf("write failed: %v; rollback failed: %v", err, rollbackErr)
		}
		if created {
			_ = os.Remove(backupPath)
		}
		return nil, fmt.Errorf("write failed and rollback could not be confirmed: %w", err)
	}
	return map[string]any{
		"ok":       true,
		"param":    spec.Name,
		"path":     spec.Path,
		"previous": original,
		"value":    value,
		"verified": true,
		"changed":  true,
	}, nil
}

func (s *daemonState) ensureBackup(path string, spec paramSpec, original string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	entry := backupEntry{
		Param:     spec.Name,
		Path:      spec.Path,
		Value:     original,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	data, _ := json.MarshalIndent(entry, "", "  ")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		_ = os.Remove(path)
		return false, err
	}
	return true, file.Close()
}

func (s *daemonState) handleRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, err := s.restoreAll()
	if err != nil || len(result.Failed) > 0 {
		status := http.StatusInternalServerError
		writeJSON(w, status, result)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type restoreResult struct {
	OK       bool     `json:"ok"`
	Restored []string `json:"restored"`
	Failed   []string `json:"failed"`
	Error    string   `json:"error,omitempty"`
}

func (s *daemonState) restoreAll() (restoreResult, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	result := restoreResult{OK: true}
	entries, err := os.ReadDir(s.backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.backupDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			result.Failed = append(result.Failed, entry.Name()+": read backup")
			continue
		}
		var backup backupEntry
		if err := json.Unmarshal(data, &backup); err != nil {
			result.Failed = append(result.Failed, entry.Name()+": invalid backup")
			continue
		}
		if err := writeVerified(backup.Path, backup.Value, backup.Value); err != nil {
			result.Failed = append(result.Failed, backup.Param+": "+err.Error())
			continue
		}
		if err := os.Remove(path); err != nil {
			result.Failed = append(result.Failed, entry.Name()+": backup remove failed")
			continue
		}
		result.Restored = append(result.Restored, backup.Param)
	}
	sort.Strings(result.Restored)
	sort.Strings(result.Failed)
	result.OK = len(result.Failed) == 0
	if !result.OK {
		return result, fmt.Errorf("failed to restore %d parameter(s)", len(result.Failed))
	}
	return result, nil
}

func validateValue(spec paramSpec, value int64) error {
	if value < spec.Min || value > spec.Max {
		return fmt.Errorf("value %d outside [%d, %d]", value, spec.Min, spec.Max)
	}
	if len(spec.Available) > 0 {
		if _, ok := spec.Available[value]; !ok {
			return fmt.Errorf("value %d is not an available frequency", value)
		}
	}
	return nil
}

func writeVerified(path, value, expected string) error {
	current, err := readString(path)
	if err == nil && current == expected {
		return nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(file, value); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	actual, err := readString(path)
	if err != nil {
		return fmt.Errorf("readback: %w", err)
	}
	if actual != expected {
		return fmt.Errorf("readback mismatch: expected %q, got %q", expected, actual)
	}
	return nil
}

func (s *daemonState) recordEvent(eventType, detail string) {
	event := watchdogEventInfo{
		Time:   time.Now().Format(time.RFC3339),
		Type:   eventType,
		Detail: detail,
	}
	data, _ := json.Marshal(event)
	file, err := os.OpenFile(s.eventsPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err == nil {
		_, _ = file.Write(append(data, '\n'))
		_ = file.Close()
	}
	log.Printf("event=%s detail=%s", eventType, detail)
}

func (s *daemonState) lastEvent() *watchdogEventInfo {
	data, err := os.ReadFile(s.eventsPath)
	if err != nil || len(data) == 0 {
		return nil
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	var event watchdogEventInfo
	if json.Unmarshal(lines[len(lines)-1], &event) != nil {
		return nil
	}
	return &event
}

func findGPUDevfreqDir() string {
	for _, candidate := range gpuDevfreqCandidates {
		if _, err := os.Stat(filepath.Join(candidate, "max_freq")); err == nil {
			return candidate
		}
	}
	return ""
}

func readString(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func readInt(path string) (int64, error) {
	value, err := readString(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(value, 10, 64)
}

func parseIntSet(path string) map[int64]struct{} {
	value, err := readString(path)
	if err != nil {
		return nil
	}
	result := make(map[int64]struct{})
	for _, field := range strings.Fields(value) {
		number, err := strconv.ParseInt(field, 10, 64)
		if err == nil && number > 0 {
			result[number] = struct{}{}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func minMax(values map[int64]struct{}) (int64, int64) {
	if len(values) == 0 {
		return 0, 0
	}
	minimum := int64(math.MaxInt64)
	var maximum int64
	for value := range values {
		if value < minimum {
			minimum = value
		}
		if value > maximum {
			maximum = value
		}
	}
	return minimum, maximum
}

func safeGPUFrequency(values map[int64]struct{}) int64 {
	if len(values) == 0 {
		return 0
	}
	_, maximum := minMax(values)
	target := int64(550_000_000)
	if maximum < 10_000_000 {
		target = 550_000
	} else if maximum < 1_000_000_000 {
		target = 550_000_000
	}
	var best int64
	for value := range values {
		if value <= target && value > best {
			best = value
		}
	}
	if best == 0 {
		best, _ = minMax(values)
	}
	return best
}

func frequencyMHz(value int64) int64 {
	if value >= 10_000_000 {
		return value / 1_000_000
	}
	return value / 1_000
}

func cpuNumber(path string) int {
	base := filepath.Base(filepath.Dir(path))
	number, _ := strconv.Atoi(strings.TrimPrefix(base, "cpu"))
	return number
}

func zoneNumber(path string) int {
	number, _ := strconv.Atoi(strings.TrimPrefix(filepath.Base(path), "thermal_zone"))
	return number
}

func unixHTTPClient(socketPath string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": message})
}
