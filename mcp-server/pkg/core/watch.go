package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/RivaldiMurpia/dow-mind/pkg/config"
	"github.com/RivaldiMurpia/dow-mind/pkg/indexer"
)

// WatchInfo describes an active directory watch.
type WatchInfo struct {
	ProjectID string     `json:"project_id"`
	Directory string     `json:"directory"`
	StartedAt time.Time  `json:"started_at"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	LastError string     `json:"last_error,omitempty"`
}

// watchHandle pairs a Watcher with its bookkeeping.
type watchHandle struct {
	watcher *indexer.Watcher
	info    WatchInfo
}

// watchesFilePath returns where the watch registry is persisted. Tests
// override it via the Service.watchesPath field so they never touch the
// real ~/.dow-mind.
func (s *Service) watchesFilePath() string {
	if s.watchesPath != "" {
		return s.watchesPath
	}
	return config.WatchesPath()
}

// persistWatches writes the active watches (projectID -> directory) to disk.
// Watches whose watcher failed to start are never registered, so the file
// always reflects the live registry.
func (s *Service) persistWatches() error {
	s.watchMu.Lock()
	entries := make(map[string]string, len(s.watches))
	for id, h := range s.watches {
		entries[id] = h.info.Directory
	}
	s.watchMu.Unlock()

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	path := s.watchesFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// loadPersistedWatches reads the watch registry from disk. A missing file
// is not an error — it just means no watches were registered yet.
func (s *Service) loadPersistedWatches() (map[string]string, error) {
	data, err := os.ReadFile(s.watchesFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	entries := make(map[string]string)
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("corrupt watch registry %s: %w", s.watchesFilePath(), err)
	}
	return entries, nil
}

// WatchProject starts watching dir for a project: the directory is indexed
// immediately, then re-indexed automatically (debounced) whenever files
// change. Only one watch per project; watching the same project twice is an
// error. Watches are persisted to ~/.dow-mind/watches.json and restored on
// the next server start (see RestoreWatches).
func (s *Service) WatchProject(ctx context.Context, projectID, dir string) (*indexer.SyncResult, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", dir)
	}

	s.watchMu.Lock()
	if _, ok := s.watches[projectID]; ok {
		s.watchMu.Unlock()
		return nil, fmt.Errorf("project %q is already being watched", projectID)
	}
	s.watchMu.Unlock()

	result, err := s.startWatch(ctx, projectID, dir)
	if err != nil {
		return nil, err
	}
	if err := s.persistWatches(); err != nil {
		// The watch itself is live; a persistence failure only affects
		// restore-after-restart, so report it on stderr via the event bus
		// instead of failing the whole call.
		s.events.Publish(Event{
			Type:      "watch_persist_failed",
			Timestamp: time.Now(),
			Data:      map[string]any{"project_id": projectID, "error": err.Error()},
		})
	}
	return result, nil
}

// startWatch performs the initial (catch-up) index and starts the fsnotify
// watcher for dir. Shared by WatchProject and RestoreWatches. The initial
// index is incremental (unchanged chunks are hash-skipped), so restoring a
// watch after downtime is cheap.
func (s *Service) startWatch(ctx context.Context, projectID, dir string) (*indexer.SyncResult, error) {
	// Initial index so the watch starts from a fresh state.
	result, err := s.IndexProject(ctx, projectID, dir)
	if err != nil {
		return nil, err
	}

	handle := &watchHandle{info: WatchInfo{
		ProjectID: projectID,
		StartedAt: time.Now(),
	}}
	w, err := indexer.NewWatcher(dir, indexer.DefaultWatchDebounce, func() {
		now := time.Now()
		_, runErr := s.IndexProject(context.Background(), projectID, dir)
		s.watchMu.Lock()
		handle.info.LastRunAt = &now
		if runErr != nil {
			handle.info.LastError = runErr.Error()
		} else {
			handle.info.LastError = ""
		}
		s.watchMu.Unlock()
	})
	if err != nil {
		return nil, err
	}
	if err := w.Start(); err != nil {
		return nil, err
	}
	handle.watcher = w
	handle.info.Directory = w.Dir()

	s.watchMu.Lock()
	// Re-check under lock: another goroutine may have started one meanwhile.
	if _, ok := s.watches[projectID]; ok {
		s.watchMu.Unlock()
		_ = w.Close()
		return nil, fmt.Errorf("project %q is already being watched", projectID)
	}
	if s.watches == nil {
		s.watches = make(map[string]*watchHandle)
	}
	s.watches[projectID] = handle
	s.watchMu.Unlock()

	return result, nil
}

// RestoreWatches re-registers watches persisted by earlier WatchProject
// calls (see watches.json). It is meant to run once at server startup,
// before serving traffic. Each restored watch gets a catch-up re-index
// (incremental — unchanged chunks are skipped, so this is cheap).
//
// Directories that no longer exist are pruned from the registry. A watch
// that fails to restore is kept in the registry so a later restart retries
// it; all failures are returned for the caller to log.
func (s *Service) RestoreWatches(ctx context.Context) (restored int, errs []error) {
	entries, err := s.loadPersistedWatches()
	if err != nil {
		return 0, []error{err}
	}
	kept := make(map[string]string, len(entries))
	for projectID, dir := range entries {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			errs = append(errs, fmt.Errorf("watch %q: directory %s gone, dropped", projectID, dir))
			continue
		}
		if _, err := s.startWatch(ctx, projectID, dir); err != nil {
			errs = append(errs, fmt.Errorf("watch %q: %w", projectID, err))
			kept[projectID] = dir
			continue
		}
		kept[projectID] = dir
		restored++
	}
	if err := s.persistWatches(); err != nil {
		errs = append(errs, fmt.Errorf("persisting pruned watch registry: %w", err))
	}
	return restored, errs
}

// UnwatchProject stops the watch for a project. It is not an error if the
// project was not being watched.
func (s *Service) UnwatchProject(projectID string) error {
	s.watchMu.Lock()
	handle, ok := s.watches[projectID]
	if ok {
		delete(s.watches, projectID)
	}
	s.watchMu.Unlock()
	if !ok {
		return nil
	}
	if err := s.persistWatches(); err != nil {
		_ = handle.watcher.Close()
		return fmt.Errorf("unwatch ok, but failed to persist registry: %w", err)
	}
	return handle.watcher.Close()
}

// WatchStatus returns the watch info for a project, or false if not watched.
func (s *Service) WatchStatus(projectID string) (WatchInfo, bool) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	handle, ok := s.watches[projectID]
	if !ok {
		return WatchInfo{}, false
	}
	return handle.info, true
}

// ListWatches returns all active watches.
func (s *Service) ListWatches() []WatchInfo {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	out := make([]WatchInfo, 0, len(s.watches))
	for _, h := range s.watches {
		out = append(out, h.info)
	}
	return out
}
