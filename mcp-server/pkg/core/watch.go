package core

import (
	"context"
	"fmt"
	"os"
	"time"

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

// WatchProject starts watching dir for a project: the directory is indexed
// immediately, then re-indexed automatically (debounced) whenever files
// change. Only one watch per project; watching the same project twice is an
// error. Watches are in-memory and do not survive restarts.
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
