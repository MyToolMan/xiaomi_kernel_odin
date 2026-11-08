package main

import (
	"os"
	"path/filepath"
	"sort"
)

func (s *daemonState) discoverParameterSpecs() {
	clusters := []struct {
		prefix    string
		label     string
		directory string
		safe      int64
	}{
		{prefix: "cpu_little", label: "小核", directory: cpuDirectories[0], safe: 1209600},
		{prefix: "cpu_big", label: "中核", directory: cpuDirectories[1], safe: 1555200},
		{prefix: "cpu_prime", label: "超大核", directory: cpuDirectories[2], safe: 1900800},
	}

	for _, cluster := range clusters {
		cpuinfoMin, minErr := readInt(filepath.Join(cluster.directory, "cpuinfo_min_freq"))
		cpuinfoMax, maxErr := readInt(filepath.Join(cluster.directory, "cpuinfo_max_freq"))
		scalingMin, scalingMinErr := readInt(filepath.Join(cluster.directory, "scaling_min_freq"))
		if minErr != nil || maxErr != nil || scalingMinErr != nil {
			continue
		}
		available := parseIntSet(filepath.Join(cluster.directory, "scaling_available_frequencies"))
		group := "CPU · " + cluster.label
		s.addSpec(paramSpec{
			Name: cluster.prefix + "_min_freq", Label: "最低频率", Group: group, Unit: "kHz",
			Path: filepath.Join(cluster.directory, "scaling_min_freq"),
			Min:  cpuinfoMin, Max: cpuinfoMax, Step: 1, Available: available,
		})
		s.addSpec(paramSpec{
			Name: cluster.prefix + "_max_freq", Label: "最高频率", Group: group, Unit: "kHz",
			Path: filepath.Join(cluster.directory, "scaling_max_freq"),
			Min:  scalingMin, Max: cpuinfoMax, Step: 1, SafeValue: cluster.safe, Available: available,
		})

		schedutilDir := filepath.Join(cluster.directory, "schedutil")
		addSpecificFrequencySpec(s, schedutilDir, cluster.prefix+"_hispeed_freq", "触发升频频率", group, cpuinfoMin, cpuinfoMax)
		addSpecificFrequencySpec(s, schedutilDir, cluster.prefix+"_efficient_freq", "节能参考频率", group, cpuinfoMin, cpuinfoMax)
		addSpecificFrequencySpec(s, schedutilDir, cluster.prefix+"_rtg_boost_freq", "实时任务 boost", group, 0, cpuinfoMax)
		addIntegerSpec(s, filepath.Join(schedutilDir, "hispeed_load"), cluster.prefix+"_hispeed_load", "升频负载阈值", group, "%", 0, 100, 1, false)
		addIntegerSpec(s, filepath.Join(schedutilDir, "up_rate_limit_us"), cluster.prefix+"_up_rate_limit_us", "升频速率间隔", group, "us", 0, 100000, 100, false)
		addIntegerSpec(s, filepath.Join(schedutilDir, "down_rate_limit_us"), cluster.prefix+"_down_rate_limit_us", "降频速率间隔", group, "us", 0, 100000, 100, false)
		addBinarySpec(s, filepath.Join(schedutilDir, "frame_aware"), cluster.prefix+"_frame_aware", "帧感知调度", group)
	}

	gpuDir := findGPUDevfreqDir()
	if gpuDir != "" {
		available := parseIntSet(filepath.Join(gpuDir, "available_frequencies"))
		minValue, maxValue := minMax(available)
		if minValue > 0 && maxValue >= minValue {
			s.addSpec(paramSpec{
				Name: "gpu_min_freq", Label: "最低频率", Group: "GPU", Unit: "Hz",
				Path: filepath.Join(gpuDir, "min_freq"), Min: minValue, Max: maxValue,
				Step: 1, Available: available,
			})
			s.addSpec(paramSpec{
				Name: "gpu_max_freq", Label: "最高频率", Group: "GPU", Unit: "Hz",
				Path: filepath.Join(gpuDir, "max_freq"), Min: minValue, Max: maxValue,
				Step: 1, SafeValue: safeGPUFrequency(available), Available: available,
			})
		}
		kgslDir := filepath.Dir(gpuDir)
		for _, binary := range []struct {
			file  string
			name  string
			label string
		}{
			{"force_clk_on", "gpu_force_clk_on", "强制 GPU 时钟"},
			{"force_bus_on", "gpu_force_bus_on", "强制 GPU 总线"},
			{"force_rail_on", "gpu_force_rail_on", "强制 GPU 供电"},
			{"force_no_nap", "gpu_force_no_nap", "禁止 GPU 休眠"},
		} {
			addBinarySpecDanger(s, filepath.Join(kgslDir, binary.file), binary.name, binary.label, "GPU · 高级", true)
		}
		addIntegerSpec(s, filepath.Join(gpuDir, "mod_percent"), "gpu_mod_percent", "活跃度阈值", "GPU · 高级", "%", 0, 100, 1, false)
		addIntegerSpec(s, filepath.Join(gpuDir, "polling_interval"), "gpu_polling_interval", "轮询间隔", "GPU · 高级", "ms", 0, 1000, 1, false)
	}
	if coolingDir := findCoolingDeviceByType("thermal-devfreq-0"); coolingDir != "" {
		maximum, err := readInt(filepath.Join(coolingDir, "max_state"))
		if err == nil && maximum >= 0 {
			s.addSpec(paramSpec{
				Name: "gpu_thermal_cooling", Label: "GPU 热控上限", Group: "GPU · 高级", Unit: "state",
				Path: filepath.Join(coolingDir, "cur_state"), Min: 0, Max: maximum, Step: 1,
				SafeValue: maximum, Danger: true,
			})
		}
	}

	s.addSpec(paramSpec{
		Name: "sched_util_clamp_min", Label: "调度最低利用率", Group: "内核调度", Unit: "util",
		Path: "/proc/sys/kernel/sched_util_clamp_min", Min: 0, Max: 1024, Step: 1,
	})
	s.addSpec(paramSpec{
		Name: "sched_util_clamp_max", Label: "调度最高利用率", Group: "内核调度", Unit: "util",
		Path: "/proc/sys/kernel/sched_util_clamp_max", Min: 0, Max: 1024, Step: 1,
	})
	addBinarySpec(s, "/proc/sys/kernel/sched_energy_aware", "sched_energy_aware", "EAS 能效调度", "内核调度")
	addIntegerSpec(s, "/proc/sys/kernel/perf_cpu_time_max_percent", "perf_cpu_time_max_percent", "Perf 采样上限", "内核调度", "%", 0, 100, 1, false)
}

func findCoolingDeviceByType(target string) string {
	directories, _ := filepath.Glob("/sys/class/thermal/cooling_device*")
	for _, directory := range directories {
		deviceType, err := readString(filepath.Join(directory, "type"))
		if err == nil && deviceType == target {
			return directory
		}
	}
	return ""
}

func (s *daemonState) addSpec(spec paramSpec) {
	if spec.Step <= 0 {
		spec.Step = 1
	}
	if _, err := os.Stat(spec.Path); err != nil {
		return
	}
	s.specs[spec.Name] = spec
}

func addSpecificFrequencySpec(s *daemonState, dir, name, label, group string, minimum, maximum int64) {
	path := filepath.Join(dir, filepath.Base(name))
	suffix := map[string]string{
		"cpu_little_hispeed_freq":   "hispeed_freq",
		"cpu_big_hispeed_freq":      "hispeed_freq",
		"cpu_prime_hispeed_freq":    "hispeed_freq",
		"cpu_little_efficient_freq": "efficient_freq",
		"cpu_big_efficient_freq":    "efficient_freq",
		"cpu_prime_efficient_freq":  "efficient_freq",
		"cpu_little_rtg_boost_freq": "rtg_boost_freq",
		"cpu_big_rtg_boost_freq":    "rtg_boost_freq",
		"cpu_prime_rtg_boost_freq":  "rtg_boost_freq",
	}
	if file, ok := suffix[name]; ok {
		path = filepath.Join(dir, file)
	}
	available := parseIntSet(filepath.Join(dir, "..", "scaling_available_frequencies"))
	if name == "cpu_little_rtg_boost_freq" || name == "cpu_big_rtg_boost_freq" || name == "cpu_prime_rtg_boost_freq" {
		available = nil
	}
	s.addSpec(paramSpec{
		Name: name, Label: label, Group: group, Unit: "kHz", Path: path,
		Min: minimum, Max: maximum, Step: 1, Available: available,
	})
}

func addIntegerSpec(s *daemonState, path, name, label, group, unit string, minimum, maximum, step int64, danger bool) {
	s.addSpec(paramSpec{
		Name: name, Label: label, Group: group, Unit: unit, Path: path,
		Min: minimum, Max: maximum, Step: step, Danger: danger,
	})
}

func addBinarySpec(s *daemonState, path, name, label, group string) {
	addBinarySpecDanger(s, path, name, label, group, false)
}

func addBinarySpecDanger(s *daemonState, path, name, label, group string, danger bool) {
	s.addSpec(paramSpec{
		Name: name, Label: label, Group: group, Path: path,
		Min: 0, Max: 1, Step: 1, Danger: danger,
		Available: map[int64]struct{}{0: {}, 1: {}},
	})
}

func (s *daemonState) readParameters() []parameterReading {
	result := make([]parameterReading, 0, len(s.specs))
	for _, spec := range s.specs {
		value, err := readInt(spec.Path)
		if err != nil {
			continue
		}
		result = append(result, parameterReading{
			Name: spec.Name, Label: spec.Label, Group: spec.Group, Unit: spec.Unit,
			Path: spec.Path, Value: value, Min: spec.Min, Max: spec.Max, Step: spec.Step,
			Danger: spec.Danger, Available: sortedAvailable(spec.Available),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Group == result[j].Group {
			return result[i].Name < result[j].Name
		}
		left, right := parameterGroupRank(result[i].Group), parameterGroupRank(result[j].Group)
		if left == right {
			return result[i].Group < result[j].Group
		}
		return left < right
	})
	return result
}

func parameterGroupRank(group string) int {
	switch group {
	case "CPU · 小核":
		return 10
	case "CPU · 中核":
		return 20
	case "CPU · 超大核":
		return 30
	case "GPU":
		return 40
	case "GPU · 高级":
		return 50
	case "内核调度":
		return 60
	default:
		return 100
	}
}
