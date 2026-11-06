package main

import (
	"fmt"
	"log"
	"sort"
)

type profileTarget struct {
	Param string
	Value int64
}

type profileResult struct {
	OK      bool     `json:"ok"`
	Profile string   `json:"profile"`
	Changed []string `json:"changed"`
	Failed  []string `json:"failed,omitempty"`
}

func profileNames() []string {
	return []string{"system", "performance", "benchmark", "safe", "manual"}
}

func validProfile(name string) bool {
	for _, candidate := range profileNames() {
		if candidate == name {
			return true
		}
	}
	return false
}

func (s *daemonState) currentProfile() string {
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	return s.activeProfile
}

func (s *daemonState) setActiveProfile(name string) {
	s.profileMu.Lock()
	s.activeProfile = name
	s.profileMu.Unlock()
}

func (s *daemonState) thermalProtectionActive() bool {
	return s.cpuHot || s.gpuHot || s.hardHot
}

func (s *daemonState) profileTargets(name string) ([]profileTarget, error) {
	maxFreqNames := []string{
		"cpu_little_max_freq",
		"cpu_big_max_freq",
		"cpu_prime_max_freq",
		"gpu_max_freq",
	}
	minFreqNames := []string{
		"cpu_little_min_freq",
		"cpu_big_min_freq",
		"cpu_prime_min_freq",
		"gpu_min_freq",
	}

	targets := make([]profileTarget, 0, len(maxFreqNames)+len(minFreqNames))
	switch name {
	case "system", "manual":
		return targets, nil
	case "performance":
		for _, param := range maxFreqNames {
			if spec, ok := s.specs[param]; ok {
				targets = append(targets, profileTarget{Param: param, Value: spec.Max})
			}
		}
	case "benchmark":
		for _, param := range maxFreqNames {
			if spec, ok := s.specs[param]; ok {
				targets = append(targets, profileTarget{Param: param, Value: spec.Max})
			}
		}
		for _, param := range minFreqNames {
			if spec, ok := s.specs[param]; ok {
				targets = append(targets, profileTarget{
					Param: param,
					Value: frequencyAtRatio(spec, 0.65),
				})
			}
		}
	case "safe":
		for _, param := range maxFreqNames {
			if spec, ok := s.specs[param]; ok && spec.SafeValue > 0 {
				targets = append(targets, profileTarget{Param: param, Value: spec.SafeValue})
			}
		}
	default:
		return nil, fmt.Errorf("unknown profile %q", name)
	}
	return targets, nil
}

func frequencyAtRatio(spec paramSpec, ratio float64) int64 {
	target := int64(float64(spec.Max) * ratio)
	values := sortedAvailable(spec.Available)
	best := spec.Min
	for _, value := range values {
		if value <= target && value > best {
			best = value
		}
	}
	if best < spec.Min {
		best = spec.Min
	}
	if best > spec.Max {
		best = spec.Max
	}
	return best
}

func sortedAvailable(values map[int64]struct{}) []int64 {
	result := make([]int64, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (s *daemonState) applyProfile(name string) (profileResult, error) {
	if !validProfile(name) {
		return profileResult{OK: false, Profile: name}, fmt.Errorf("unknown profile %q", name)
	}
	result := profileResult{OK: true, Profile: name}
	if name == "system" {
		restored, err := s.restoreAll()
		result.Changed = append(result.Changed, restored.Restored...)
		result.Failed = append(result.Failed, restored.Failed...)
		result.OK = len(result.Failed) == 0
		s.setActiveProfile("system")
		if err != nil {
			return result, err
		}
		return result, nil
	}
	if name == "manual" {
		s.setActiveProfile("manual")
		return result, nil
	}

	targets, err := s.profileTargets(name)
	if err != nil {
		return result, err
	}
	s.writeMu.Lock()
	for _, target := range targets {
		spec, ok := s.specs[target.Param]
		if !ok {
			result.Failed = append(result.Failed, target.Param+": unavailable")
			continue
		}
		if err := validateValue(spec, target.Value); err != nil {
			result.Failed = append(result.Failed, target.Param+": "+err.Error())
			continue
		}
		writeResult, err := s.setParamLocked(spec, target.Value)
		if err != nil {
			result.Failed = append(result.Failed, target.Param+": "+err.Error())
			continue
		}
		if changed, _ := writeResult["changed"].(bool); changed {
			result.Changed = append(result.Changed, target.Param)
		}
	}
	s.writeMu.Unlock()
	result.OK = len(result.Failed) == 0
	s.setActiveProfile(name)
	if !result.OK {
		log.Printf("profile %s has %d transient failure(s); enforcement will retry", name, len(result.Failed))
		return result, nil
	}
	log.Printf("profile %s applied; changed=%v", name, result.Changed)
	return result, nil
}

func (s *daemonState) enforceActiveProfile() {
	name := s.currentProfile()
	if name == "system" || name == "manual" {
		return
	}
	targets, err := s.profileTargets(name)
	if err != nil {
		return
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for _, target := range targets {
		spec, ok := s.specs[target.Param]
		if !ok {
			continue
		}
		before, err := readInt(spec.Path)
		if err != nil || before == target.Value {
			continue
		}
		if _, err := s.setParamLocked(spec, target.Value); err != nil {
			log.Printf("profile enforcement failed for %s: %v", target.Param, err)
			continue
		}
		s.recordEvent("profile_enforce", name, 0, target.Param, before, target.Value, "system limit override")
	}
}
