package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestWatchProjectLifecycle verifies watch start/status/stop and the
// double-watch guard.
func TestWatchProjectLifecycle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewService(&mockProvider{}, nil, nil)
	ctx := context.Background()

	result, err := svc.WatchProject(ctx, "proj1", dir)
	if err != nil {
		t.Fatalf("WatchProject: %v", err)
	}
	if result.Indexed == 0 {
		t.Error("expected initial index to index at least 1 chunk")
	}

	info, ok := svc.WatchStatus("proj1")
	if !ok {
		t.Fatal("WatchStatus: not found after WatchProject")
	}
	if info.ProjectID != "proj1" {
		t.Errorf("ProjectID = %q", info.ProjectID)
	}
	if info.Directory == "" {
		t.Error("Directory empty in watch info")
	}

	// Second watch on the same project must fail.
	if _, err := svc.WatchProject(ctx, "proj1", dir); err == nil {
		t.Error("expected error on double watch, got nil")
	}

	if err := svc.UnwatchProject("proj1"); err != nil {
		t.Fatalf("UnwatchProject: %v", err)
	}
	if _, ok := svc.WatchStatus("proj1"); ok {
		t.Error("WatchStatus: found after UnwatchProject")
	}

	// Unwatching a non-watched project is a no-op, not an error.
	if err := svc.UnwatchProject("proj1"); err != nil {
		t.Errorf("UnwatchProject (idempotent): %v", err)
	}

	if got := len(svc.ListWatches()); got != 0 {
		t.Errorf("ListWatches = %d, want 0", got)
	}
}

// TestWatchProjectRejectsBadInput verifies validation.
func TestWatchProjectRejectsBadInput(t *testing.T) {
	svc := NewService(&mockProvider{}, nil, nil)
	ctx := context.Background()

	if _, err := svc.WatchProject(ctx, "", t.TempDir()); err == nil {
		t.Error("expected error for empty project id")
	}
	if _, err := svc.WatchProject(ctx, "p", filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected error for missing directory")
	}
}

// newWatchTestService returns a Service whose watch registry persists to a
// temp file, so tests never touch the real ~/.dow-mind.
func newWatchTestService(t *testing.T) *Service {
	t.Helper()
	svc := NewService(&mockProvider{}, nil, nil)
	svc.watchesPath = filepath.Join(t.TempDir(), "watches.json")
	return svc
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestWatchPersistsRegistry verifies WatchProject writes the registry to
// disk and UnwatchProject removes the entry.
func TestWatchPersistsRegistry(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "a.go", "package main\n")

	svc := newWatchTestService(t)
	ctx := context.Background()

	if _, err := svc.WatchProject(ctx, "proj1", dir); err != nil {
		t.Fatalf("WatchProject: %v", err)
	}
	defer svc.UnwatchProject("proj1")

	data, err := os.ReadFile(svc.watchesFilePath())
	if err != nil {
		t.Fatalf("registry file not written: %v", err)
	}
	abs, _ := filepath.Abs(dir)
	var entries map[string]string
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("registry not valid JSON: %v", err)
	}
	if entries["proj1"] != abs {
		t.Errorf("registry[proj1] = %q, want %q", entries["proj1"], abs)
	}

	if err := svc.UnwatchProject("proj1"); err != nil {
		t.Fatalf("UnwatchProject: %v", err)
	}
	data, err = os.ReadFile(svc.watchesFilePath())
	if err != nil {
		t.Fatalf("registry file missing after unwatch: %v", err)
	}
	entries = map[string]string{}
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("registry not valid JSON: %v", err)
	}
	if _, ok := entries["proj1"]; ok {
		t.Error("registry still contains proj1 after UnwatchProject")
	}
}

// TestRestoreWatches verifies a new Service picks up watches persisted by a
// previous one, including the catch-up re-index.
func TestRestoreWatches(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "a.go", "package main\n")

	svc1 := newWatchTestService(t)
	// Share one registry file between the two services to simulate restart.
	regFile := filepath.Join(t.TempDir(), "watches.json")
	svc1.watchesPath = regFile
	ctx := context.Background()

	if _, err := svc1.WatchProject(ctx, "proj1", dir); err != nil {
		t.Fatalf("WatchProject: %v", err)
	}
	// Simulate server shutdown: drop the in-memory registry without
	// unwatching (UnwatchProject would delete the persisted entry).
	svc1.watchMu.Lock()
	for _, h := range svc1.watches {
		h.watcher.Close()
	}
	svc1.watches = make(map[string]*watchHandle)
	svc1.watchMu.Unlock()

	svc2 := NewService(&mockProvider{}, nil, nil)
	svc2.watchesPath = regFile
	restored, errs := svc2.RestoreWatches(ctx)
	for _, err := range errs {
		t.Errorf("RestoreWatches error: %v", err)
	}
	if restored != 1 {
		t.Errorf("restored = %d, want 1", restored)
	}
	if _, ok := svc2.WatchStatus("proj1"); !ok {
		t.Error("WatchStatus: proj1 not active after restore")
	}
	defer svc2.UnwatchProject("proj1")
}

// TestRestoreWatchesPrunesDeadDir verifies entries whose directory vanished
// are dropped from the registry on restore.
func TestRestoreWatchesPrunesDeadDir(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "a.go", "package main\n")

	regFile := filepath.Join(t.TempDir(), "watches.json")
	svc1 := NewService(&mockProvider{}, nil, nil)
	svc1.watchesPath = regFile
	ctx := context.Background()
	if _, err := svc1.WatchProject(ctx, "proj1", dir); err != nil {
		t.Fatalf("WatchProject: %v", err)
	}
	svc1.watchMu.Lock()
	for _, h := range svc1.watches {
		h.watcher.Close()
	}
	svc1.watches = make(map[string]*watchHandle)
	svc1.watchMu.Unlock()

	// Delete the watched directory before restore.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	svc2 := NewService(&mockProvider{}, nil, nil)
	svc2.watchesPath = regFile
	restored, errs := svc2.RestoreWatches(ctx)
	if restored != 0 {
		t.Errorf("restored = %d, want 0", restored)
	}
	if len(errs) == 0 {
		t.Error("expected an error for the pruned watch, got none")
	}
	if _, ok := svc2.WatchStatus("proj1"); ok {
		t.Error("WatchStatus: proj1 active after its directory was deleted")
	}
	data, err := os.ReadFile(regFile)
	if err != nil {
		t.Fatalf("registry file missing: %v", err)
	}
	entries := map[string]string{}
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("registry not valid JSON: %v", err)
	}
	if _, ok := entries["proj1"]; ok {
		t.Error("registry still contains pruned proj1")
	}
}
