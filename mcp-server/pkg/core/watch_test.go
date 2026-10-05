package core

import (
	"context"
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
