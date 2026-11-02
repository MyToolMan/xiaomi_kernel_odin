package main

import (
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
