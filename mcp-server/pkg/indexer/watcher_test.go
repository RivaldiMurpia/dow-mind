package indexer

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWatcherFiresOnChange verifies onChange is called (debounced) after a
// file write in the watched tree.
func TestWatcherFiresOnChange(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	fired := make(chan struct{}, 4)
	w, err := NewWatcher(dir, 100*time.Millisecond, func() { fired <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Burst of writes should collapse into (at least) one callback.
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n// edit\n"), 0644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("onChange was not called after file writes")
	}
}

// TestWatcherWatchesNewSubdir verifies directories created after Start are
// picked up automatically.
func TestWatcherWatchesNewSubdir(t *testing.T) {
	dir := t.TempDir()

	fired := make(chan struct{}, 4)
	w, err := NewWatcher(dir, 100*time.Millisecond, func() { fired <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	sub := filepath.Join(dir, "newsub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	// Give the watcher a moment to register the new directory.
	time.Sleep(300 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(sub, "b.go"), []byte("package newsub\n"), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("onChange was not called for file in new subdirectory")
	}
}

// TestWatcherSkipsIgnoredDirs verifies defaultIgnores directories are not
// watched (no callback for changes inside them).
func TestWatcherSkipsIgnoredDirs(t *testing.T) {
	dir := t.TempDir()
	nm := filepath.Join(dir, "node_modules")
	if err := os.MkdirAll(nm, 0755); err != nil {
		t.Fatal(err)
	}

	fired := make(chan struct{}, 4)
	w, err := NewWatcher(dir, 100*time.Millisecond, func() { fired <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := os.WriteFile(filepath.Join(nm, "x.js"), []byte("1"), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-fired:
		t.Fatal("onChange fired for change inside ignored directory")
	case <-time.After(600 * time.Millisecond):
		// expected: no callback
	}
}

// TestNewWatcherRejectsBadDir verifies constructor validation.
func TestNewWatcherRejectsBadDir(t *testing.T) {
	if _, err := NewWatcher(filepath.Join(t.TempDir(), "nope"), 0, nil); err == nil {
		t.Error("expected error for missing directory, got nil")
	}
	f, err := os.CreateTemp(t.TempDir(), "file")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := NewWatcher(f.Name(), 0, nil); err == nil {
		t.Error("expected error for non-directory, got nil")
	}
}
