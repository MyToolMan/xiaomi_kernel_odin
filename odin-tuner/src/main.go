package main

import (
	"bytes"
	"context"
	"crypto/subtle"
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
	defaultCPUTemp   = 85.0
	defaultGPUTemp   = 85.0
	defaultHardTemp  = 95.0
	minimumWatchTemp = 60.0
	maximumHardTemp  = 95.0
	watchdogRearmGap = 5.0
	collectionPeriod = 2 * time.Second
	defaultProfile   = "performance"
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

type batteryReading struct {
	CurrentMA int64 `json:"current_ma"`
}

type thermalReading struct {
	Zone  string   `json:"zone"`
	Type  string   `json:"type"`
	TempC *float64 `json:"temp_c"`
	Mode  string   `json:"mode,omitempty"`
}

type thermalSummary struct {
	CPULittle *float64 `json:"cpu_little"`
	CPUMid    *float64 `json:"cpu_mid"`
	CPUPrime  *float64 `json:"cpu_prime"`
	GPU       *float64 `json:"gpu"`
	Battery   *float64 `json:"battery"`
	Board     *float64 `json:"board"`
	Memory    *float64 `json:"memory"`
}

type snapshot struct {
	GeneratedAt       string             `json:"generated_at"`
	CPU               []cpuReading       `json:"cpu"`
	GPU               *gpuReading        `json:"gpu"`
	GPUError          string             `json:"gpu_error,omitempty"`
	Battery           *batteryReading    `json:"battery,omitempty"`
	Thermal           []thermalReading   `json:"-"`
	ThermalSummary    *thermalSummary    `json:"thermal_summary"`
	MaxTempC          *float64           `json:"max_temp_c"`
	WatchdogTriggered bool               `json:"watchdog_triggered"`
	ConfigError       string             `json:"config_error,omitempty"`
	LastEvent         *watchdogEventInfo `json:"last_event,omitempty"`
	ActiveProfile     string             `json:"active_profile"`
	StartupProfile    string             `json:"startup_profile"`
	EnforceProfile    bool               `json:"enforce_profile"`
	Profiles          []string           `json:"profiles"`
	Controls          []parameterReading `json:"controls"`
}

type watchdogEventInfo struct {
	Time         string  `json:"time"`
	Type         string  `json:"type"`
	Sensor       string  `json:"sensor"`
	TemperatureC float64 `json:"temperature_c"`
	Param        string  `json:"param,omitempty"`
	Before       int64   `json:"before_freq,omitempty"`
	After        int64   `json:"after_freq,omitempty"`
	Detail       string  `json:"detail,omitempty"`
}

type watchdogConfig struct {
	CPUThrottleTemp float64 `json:"cpu_throttle_temp"`
	GPUThrottleTemp float64 `json:"gpu_throttle_temp"`
	HardLimitTemp   float64 `json:"hard_limit_temp"`
	AuthMode        string  `json:"auth_mode"`
	FixedToken      string  `json:"fixed_token"`
	WebAutostart    bool    `json:"web_autostart"`
	StartupProfile  string  `json:"startup_profile"`
	EnforceProfile  bool    `json:"enforce_profile"`
}

type paramSpec struct {
	Name      string
	Label     string
	Group     string
	Unit      string
	Path      string
	Min       int64
	Max       int64
	Step      int64
	SafeValue int64
	Danger    bool
	Available map[int64]struct{}
}

type parameterReading struct {
	Name      string  `json:"name"`
	Label     string  `json:"label"`
	Group     string  `json:"group"`
	Unit      string  `json:"unit,omitempty"`
	Path      string  `json:"path"`
	Value     int64   `json:"value"`
	Min       int64   `json:"min"`
	Max       int64   `json:"max"`
	Step      int64   `json:"step"`
	Danger    bool    `json:"danger,omitempty"`
	Available []int64 `json:"available,omitempty"`
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
	configPath   string

	writeMu       sync.Mutex
	configMu      sync.RWMutex
	config        watchdogConfig
	profileMu     sync.RWMutex
	activeProfile string
	subMu         sync.Mutex
	specs         map[string]paramSpec
	subscribers   map[chan snapshot]struct{}
	latest        atomic.Value
	cpuHot        bool
	gpuHot        bool
	hardHot       bool
}

type setRequest struct {
	Param string      `json:"param"`
	Value json.Number `json:"value"`
}

type profileRequest struct {
	Name string `json:"name"`
}

type configRequest struct {
	StartupProfile *string `json:"startup_profile,omitempty"`
	EnforceProfile *bool   `json:"enforce_profile,omitempty"`
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
	case "config-get":
		runConfigGet(os.Args[2:])
	case "version":
		fmt.Println("odin-tuner v0.3.1")
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: odin-tuner {daemon|web|restore-all|config-get|version} [options]")
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
	startupProfile := state.currentConfig().StartupProfile
	if startupProfile != "manual" {
		result, applyErr := state.applyProfile(startupProfile)
		if applyErr != nil {
			log.Printf("startup profile %s partially failed: %v", startupProfile, applyErr)
		} else {
			log.Printf("startup profile %s applied: changed=%d", startupProfile, len(result.Changed))
		}
	}
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
	authMode := flags.String("auth-mode", "off", "off, fixed, or random")
	tokenFile := flags.String("token-file", "", "file containing the web access token")
	root := flags.String("root", "", "webui directory")
	flags.Parse(args)

	if *socketPath == "" {
		*socketPath = filepath.Join(*dataDir, "odin-tuner.sock")
	}
	if *root == "" {
		*root = "webui"
	}
	if *tokenFile == "" {
		*tokenFile = filepath.Join(*dataDir, "web_token")
	}
	if *authMode != "off" && *authMode != "fixed" && *authMode != "random" {
		log.Fatalf("invalid auth mode %q", *authMode)
	}
	token := ""
	if *authMode != "off" {
		var err error
		token, err = readString(*tokenFile)
		if err != nil || token == "" {
			log.Fatalf("read web token %s: %v", *tokenFile, err)
		}
	}

	client := unixHTTPClient(*socketPath)
	target := "http://unix"
	if *apiURL != "" {
		client = &http.Client{Timeout: 5 * time.Second}
		target = strings.TrimRight(*apiURL, "/")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		response, err := client.Get(target + "/stream")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(response.StatusCode)
		flusher, _ := w.(http.Flusher)
		buffer := make([]byte, 32<<10)
		for {
			n, readErr := response.Body.Read(buffer)
			if n > 0 {
				if _, err := w.Write(buffer[:n]); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if readErr != nil {
				return
			}
		}
	})
	for _, endpoint := range []string{"/params", "/set", "/profile", "/config", "/restore"} {
		endpoint := endpoint
		mux.HandleFunc("/api"+endpoint, func(w http.ResponseWriter, r *http.Request) {
			proxyDaemonRequest(w, r, client, target, endpoint)
		})
	}
	mux.Handle("/", http.FileServer(http.Dir(*root)))

	server := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	server.Handler = webAccessHandler(*authMode, token, mux)
	if *authMode == "off" {
		log.Printf("WARNING: 鉴权已关闭，仅限可信网络")
	}
	log.Printf("web UI listening on http://%s", *listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func proxyDaemonRequest(w http.ResponseWriter, r *http.Request, client *http.Client, target, endpoint string) {
	requestTarget := target + endpoint
	if r.URL.RawQuery != "" {
		requestTarget += "?" + r.URL.RawQuery
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, requestTarget, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	request.Header = r.Header.Clone()
	response, err := client.Do(request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for _, header := range []string{"Content-Type", "Cache-Control"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
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

func runConfigGet(args []string) {
	flags := flag.NewFlagSet("config-get", flag.ExitOnError)
	configPath := flags.String("config", filepath.Join(defaultDataDir, "config.json"), "configuration path")
	field := flags.String("field", "auth_mode", "configuration field")
	flags.Parse(args)

	if err := ensureConfigFile(*configPath); err != nil {
		log.Fatal(err)
	}
	state := &daemonState{configPath: *configPath}
	config, err := state.loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	switch *field {
	case "auth_mode":
		fmt.Println(config.AuthMode)
	case "fixed_token":
		fmt.Println(config.FixedToken)
	case "web_autostart":
		fmt.Println(strconv.FormatBool(config.WebAutostart))
	case "startup_profile":
		fmt.Println(config.StartupProfile)
	case "enforce_profile":
		fmt.Println(strconv.FormatBool(config.EnforceProfile))
	case "cpu_throttle_temp":
		fmt.Println(strconv.FormatFloat(config.CPUThrottleTemp, 'f', -1, 64))
	case "gpu_throttle_temp":
		fmt.Println(strconv.FormatFloat(config.GPUThrottleTemp, 'f', -1, 64))
	case "hard_limit_temp":
		fmt.Println(strconv.FormatFloat(config.HardLimitTemp, 'f', -1, 64))
	default:
		fmt.Fprintf(os.Stderr, "unknown config field %q\n", *field)
		os.Exit(2)
	}
}

func newDaemonState(dataDir, moduleDir, socketPath string) (*daemonState, error) {
	state := &daemonState{
		dataDir:       dataDir,
		moduleDir:     moduleDir,
		socketPath:    socketPath,
		backupDir:     filepath.Join(dataDir, "backup"),
		eventsPath:    filepath.Join(dataDir, "events.jsonl"),
		snapshotPath:  filepath.Join(dataDir, "snapshot.json"),
		configPath:    filepath.Join(dataDir, "config.json"),
		config:        defaultWatchdogConfig(),
		specs:         make(map[string]paramSpec),
		subscribers:   make(map[chan snapshot]struct{}),
		activeProfile: "system",
	}
	for _, dir := range []string{state.dataDir, state.backupDir, filepath.Join(state.dataDir, "logs")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	_ = os.Remove(socketPath)
	if err := state.ensureConfig(); err != nil {
		return nil, err
	}
	state.discoverSpecs()
	return state, nil
}

func (s *daemonState) discoverSpecs() {
	s.discoverParameterSpecs()
	log.Printf("discovered %d writable parameter specs", len(s.specs))
}

func defaultWatchdogConfig() watchdogConfig {
	return watchdogConfig{
		CPUThrottleTemp: defaultCPUTemp,
		GPUThrottleTemp: defaultGPUTemp,
		HardLimitTemp:   defaultHardTemp,
		AuthMode:        "off",
		FixedToken:      "",
		WebAutostart:    true,
		StartupProfile:  defaultProfile,
		EnforceProfile:  true,
	}
}

func (s *daemonState) ensureConfig() error {
	return ensureConfigFile(s.configPath)
}

func ensureConfigFile(configPath string) error {
	if _, err := os.Stat(configPath); err == nil {
		state := &daemonState{configPath: configPath}
		config, loadErr := state.loadConfig()
		if loadErr != nil {
			return loadErr
		}
		return writeConfigAtomic(configPath, config)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return err
	}
	return writeConfigAtomic(configPath, defaultWatchdogConfig())
}

func writeConfigAtomic(configPath string, config watchdogConfig) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(configPath), "."+filepath.Base(configPath)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, configPath)
}

func (s *daemonState) loadConfig() (watchdogConfig, error) {
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		return defaultWatchdogConfig(), err
	}
	config := defaultWatchdogConfig()
	if err := json.Unmarshal(data, &config); err != nil {
		return defaultWatchdogConfig(), fmt.Errorf("parse config: %w", err)
	}
	return normalizeConfig(config)
}

func normalizeConfig(config watchdogConfig) (watchdogConfig, error) {
	defaults := defaultWatchdogConfig()
	if config.HardLimitTemp == 0 {
		config.HardLimitTemp = defaults.HardLimitTemp
	}
	config.HardLimitTemp = clampFloat(config.HardLimitTemp, minimumWatchTemp, maximumHardTemp)

	if config.CPUThrottleTemp == 0 {
		config.CPUThrottleTemp = defaults.CPUThrottleTemp
	}
	if config.GPUThrottleTemp == 0 {
		config.GPUThrottleTemp = defaults.GPUThrottleTemp
	}
	config.CPUThrottleTemp = clampFloat(config.CPUThrottleTemp, minimumWatchTemp, config.HardLimitTemp)
	config.GPUThrottleTemp = clampFloat(config.GPUThrottleTemp, minimumWatchTemp, config.HardLimitTemp)

	switch config.AuthMode {
	case "", "off":
		config.AuthMode = "off"
	case "fixed":
		if !isURLSafeToken(config.FixedToken, 16) {
			return config, errors.New("fixed_token must be at least 16 characters and URL-safe")
		}
	case "random":
	default:
		return config, fmt.Errorf("invalid auth_mode %q", config.AuthMode)
	}
	switch config.StartupProfile {
	case "":
		config.StartupProfile = defaultProfile
	case "system", "performance", "benchmark", "safe", "manual":
	default:
		return config, fmt.Errorf("invalid startup_profile %q", config.StartupProfile)
	}
	return config, nil
}

func isURLSafeToken(token string, minimumLength int) bool {
	if len(token) < minimumLength {
		return false
	}
	for _, character := range []byte(token) {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '.' || character == '_' || character == '~' {
			continue
		}
		return false
	}
	return true
}

func clampFloat(value, minimum, maximum float64) float64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func (s *daemonState) currentConfig() watchdogConfig {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config
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
	snap.Battery = readBattery()
	snap.ThermalSummary = summarizeThermal(snap.Thermal)
	snap.MaxTempC = maxTemperature(snap.Thermal)
	config, configErr := s.loadConfig()
	if configErr != nil {
		snap.ConfigError = configErr.Error()
	} else {
		s.configMu.Lock()
		s.config = config
		s.configMu.Unlock()
	}
	activeConfig := s.currentConfig()
	snap.WatchdogTriggered = s.cpuHot || s.gpuHot || s.hardHot
	snap.LastEvent = s.lastEvent()

	cpuTemp, cpuSensor := maxThermalValue(
		snap.ThermalSummary.CPULittle,
		snap.ThermalSummary.CPUMid,
		snap.ThermalSummary.CPUPrime,
	)
	gpuTemp := derefFloat(snap.ThermalSummary.GPU)
	anyTemp := summaryMaximum(snap.ThermalSummary)

	if anyTemp != nil && *anyTemp >= activeConfig.HardLimitTemp {
		if !s.hardHot {
			s.hardHot = true
			s.applySafeCaps("hard_limit", "thermal_summary_max", *anyTemp, []string{
				"cpu_little_max_freq", "cpu_big_max_freq", "cpu_prime_max_freq", "gpu_max_freq", "gpu_thermal_cooling",
			})
		}
	} else if anyTemp != nil && *anyTemp < activeConfig.HardLimitTemp-watchdogRearmGap {
		s.hardHot = false
	}

	if cpuTemp != nil && *cpuTemp >= activeConfig.CPUThrottleTemp {
		if !s.cpuHot {
			s.cpuHot = true
			s.applySafeCaps("cpu_throttle", cpuSensor, *cpuTemp, []string{
				"cpu_little_max_freq", "cpu_big_max_freq", "cpu_prime_max_freq",
			})
		}
	} else if cpuTemp != nil && *cpuTemp < activeConfig.CPUThrottleTemp-watchdogRearmGap {
		s.cpuHot = false
	}

	if gpuTemp != nil && *gpuTemp >= activeConfig.GPUThrottleTemp {
		if !s.gpuHot {
			s.gpuHot = true
			s.applySafeCaps("gpu_throttle", "gpu", *gpuTemp, []string{"gpu_max_freq", "gpu_thermal_cooling"})
		}
	} else if gpuTemp != nil && *gpuTemp < activeConfig.GPUThrottleTemp-watchdogRearmGap {
		s.gpuHot = false
	}

	if activeConfig.EnforceProfile && !s.thermalProtectionActive() {
		s.enforceActiveProfile()
	}

	snap.WatchdogTriggered = s.cpuHot || s.gpuHot || s.hardHot
	snap.LastEvent = s.lastEvent()
	snap.ActiveProfile = s.currentProfile()
	snap.StartupProfile = activeConfig.StartupProfile
	snap.EnforceProfile = activeConfig.EnforceProfile
	snap.Profiles = profileNames()
	snap.Controls = s.readParameters()

	data, err := json.Marshal(snap)
	if err == nil {
		tmp := s.snapshotPath + ".tmp"
		if os.WriteFile(tmp, append(data, '\n'), 0o600) == nil {
			_ = os.Rename(tmp, s.snapshotPath)
		}
	}
	s.latest.Store(snap)
	s.broadcast(snap)
}

func (s *daemonState) broadcast(snap snapshot) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for subscriber := range s.subscribers {
		select {
		case subscriber <- snap:
		default:
			// A slow browser must not block collection or other subscribers.
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

func readBattery() *batteryReading {
	microamps, err := readInt("/sys/class/power_supply/battery/current_now")
	if err != nil {
		return nil
	}
	return &batteryReading{CurrentMA: microamps / 1000}
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

func summarizeThermal(zones []thermalReading) *thermalSummary {
	summary := &thermalSummary{}
	for _, zone := range zones {
		if zone.TempC == nil || *zone.TempC <= -40 || zone.Mode == "disabled" {
			continue
		}
		value := *zone.TempC
		switch {
		case strings.HasPrefix(zone.Type, "cpu-0-") &&
			(strings.HasSuffix(zone.Type, "-step") || strings.HasSuffix(zone.Type, "-usr")):
			setMaximum(&summary.CPULittle, value)
		case strings.HasPrefix(zone.Type, "cpu-1-") &&
			(strings.HasSuffix(zone.Type, "-step") || strings.HasSuffix(zone.Type, "-usr")):
			index := cpuClusterSensorIndex(zone.Type)
			if index <= 3 {
				setMaximum(&summary.CPUMid, value)
			} else {
				setMaximum(&summary.CPUPrime, value)
			}
		case zone.Type == "gpuss-0-step" || zone.Type == "gpuss-1-step":
			setMaximum(&summary.GPU, value)
		case zone.Type == "battery":
			setMaximum(&summary.Battery, value)
		case zone.Type == "xo_therm" || zone.Type == "quiet_therm":
			setMaximum(&summary.Board, value)
		case zone.Type == "ddr-usr":
			setMaximum(&summary.Memory, value)
		}
	}
	return summary
}

func cpuClusterSensorIndex(zoneType string) int {
	parts := strings.Split(zoneType, "-")
	if len(parts) < 3 {
		return 99
	}
	index, err := strconv.Atoi(parts[2])
	if err != nil {
		return 99
	}
	return index
}

func setMaximum(target **float64, value float64) {
	if *target == nil || value > **target {
		copied := value
		*target = &copied
	}
}

func derefFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func maxThermalValue(values ...*float64) (*float64, string) {
	names := []string{"cpu_little", "cpu_mid", "cpu_prime"}
	var maximum *float64
	sensor := "cpu"
	for index, value := range values {
		if value != nil && (maximum == nil || *value > *maximum) {
			copied := *value
			maximum = &copied
			sensor = names[index]
		}
	}
	return maximum, sensor
}

func summaryMaximum(summary *thermalSummary) *float64 {
	if summary == nil {
		return nil
	}
	values := []*float64{
		summary.CPULittle, summary.CPUMid, summary.CPUPrime,
		summary.GPU, summary.Battery, summary.Board, summary.Memory,
	}
	var maximum *float64
	for _, value := range values {
		if value != nil && (maximum == nil || *value > *maximum) {
			copied := *value
			maximum = &copied
		}
	}
	return maximum
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

func (s *daemonState) applySafeCaps(eventType, sensor string, temperature float64, params []string) {
	s.writeMu.Lock()
	for _, name := range params {
		spec := s.specs[name]
		if spec.SafeValue == 0 {
			continue
		}
		before, err := readInt(spec.Path)
		if err != nil {
			s.recordEvent(eventType, sensor, temperature, name, 0, 0, "node unavailable")
			continue
		}
		after := before
		detail := "already at or below safety cap"
		needsCap := before > spec.SafeValue
		if name == "gpu_thermal_cooling" {
			needsCap = before < spec.SafeValue
			detail = "already at maximum cooling state"
		}
		if needsCap {
			if _, err := s.setParamLocked(spec, spec.SafeValue); err != nil {
				detail = "cap failed: " + err.Error()
			} else {
				after = spec.SafeValue
				if name == "gpu_thermal_cooling" {
					detail = "maximum cooling state restored"
				} else {
					detail = "frequency capped"
				}
			}
		}
		s.recordEvent(eventType, sensor, temperature, name, before, after, detail)
	}
	s.writeMu.Unlock()
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
	mux.HandleFunc("/profile", s.handleProfile)
	mux.HandleFunc("/config", s.handleConfig)
	mux.HandleFunc("/params", s.handleParams)
	mux.HandleFunc("/restore", s.handleRestore)
	mux.HandleFunc("/stream", s.handleStream)

	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

func (s *daemonState) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	subscriber := make(chan snapshot, 1)
	s.subMu.Lock()
	s.subscribers[subscriber] = struct{}{}
	s.subMu.Unlock()
	defer func() {
		s.subMu.Lock()
		delete(s.subscribers, subscriber)
		s.subMu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	_, _ = io.WriteString(w, "retry: 2000\n\n")
	if value := s.latest.Load(); value != nil {
		writeSSE(w, value.(snapshot))
		flusher.Flush()
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case snap := <-subscriber:
			writeSSE(w, snap)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func writeSSE(w io.Writer, snap snapshot) {
	data, err := json.Marshal(snap)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
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
	s.setActiveProfile("manual")
	writeJSON(w, http.StatusOK, result)
}

func (s *daemonState) handleParams(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"profile":    s.currentProfile(),
		"parameters": s.readParameters(),
	})
}

func (s *daemonState) handleProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
	var request profileRequest
	if err := decoder.Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.applyProfile(request.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"profile": request.Name,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *daemonState) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
	var request configRequest
	if err := decoder.Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	config, err := s.loadConfig()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if request.StartupProfile != nil {
		config.StartupProfile = *request.StartupProfile
	}
	if request.EnforceProfile != nil {
		config.EnforceProfile = *request.EnforceProfile
	}
	config, err = normalizeConfig(config)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := writeConfigAtomic(s.configPath, config); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.configMu.Lock()
	s.config = config
	s.configMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": config})
}

func (s *daemonState) setParam(spec paramSpec, value int64) (map[string]any, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.setParamLocked(spec, value)
}

func (s *daemonState) setParamLocked(spec paramSpec, value int64) (map[string]any, error) {
	if err := validateSiblingLimit(spec, value); err != nil {
		return nil, err
	}
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

func validateSiblingLimit(spec paramSpec, value int64) error {
	directory := filepath.Dir(spec.Path)
	minFile := "scaling_min_freq"
	maxFile := "scaling_max_freq"
	if strings.HasPrefix(spec.Name, "gpu_") {
		minFile = "min_freq"
		maxFile = "max_freq"
	}
	switch {
	case strings.HasSuffix(spec.Name, "_max_freq"):
		minimum, err := readInt(filepath.Join(directory, minFile))
		if err == nil && value < minimum {
			return fmt.Errorf("maximum %d is below current minimum %d", value, minimum)
		}
	case strings.HasSuffix(spec.Name, "_min_freq"):
		maximum, err := readInt(filepath.Join(directory, maxFile))
		if err == nil && value > maximum {
			return fmt.Errorf("minimum %d is above current maximum %d", value, maximum)
		}
	}
	return nil
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
	s.setActiveProfile("system")
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
			// Some Qualcomm policies omit cpuinfo_max_freq from
			// scaling_available_frequencies while still accepting it.
			if value != spec.Max {
				return fmt.Errorf("value %d is not an available frequency", value)
			}
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

func (s *daemonState) recordEvent(
	eventType string,
	sensor string,
	temperature float64,
	param string,
	before int64,
	after int64,
	detail string,
) {
	event := watchdogEventInfo{
		Time:         time.Now().Format(time.RFC3339),
		Type:         eventType,
		Sensor:       sensor,
		TemperatureC: temperature,
		Param:        param,
		Before:       before,
		After:        after,
		Detail:       detail,
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
	return &http.Client{Transport: transport}
}

func requireWebToken(expected string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := r.URL.Query().Get("token")
		if provided == "" {
			provided = r.Header.Get("X-ODIN-Token")
		}
		if provided == "" {
			authorization := r.Header.Get("Authorization")
			if strings.HasPrefix(authorization, "Bearer ") {
				provided = strings.TrimPrefix(authorization, "Bearer ")
			}
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			http.Error(w, "401 unauthorized: missing or invalid token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func webAccessHandler(authMode, token string, next http.Handler) http.Handler {
	if authMode == "off" {
		return next
	}
	return requireWebToken(token, next)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": message})
}
