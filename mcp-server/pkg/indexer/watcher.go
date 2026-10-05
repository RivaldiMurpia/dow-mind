package indexer

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DefaultWatchDebounce is how long the watcher waits for filesystem activity
// to settle before triggering a re-index. Editors and build tools often emit
// bursts of events for a single save; debouncing collapses them into one.
const DefaultWatchDebounce = 2 * time.Second

// Watcher monitors a directory tree for file changes and invokes onChange
// (debounced) so the index stays fresh without manual re-indexing.
// Subdirectories listed in defaultIgnores (node_modules, .git, ...) are
// never watched.
type Watcher struct {
	dir      string
	debounce time.Duration
	onChange func()

	mu      sync.Mutex
	watcher *fsnotify.Watcher
	done    chan struct{}
	wg      sync.WaitGroup
	closed  bool
}

// NewWatcher creates a Watcher for dir. onChange is called (debounced) after
// filesystem activity settles. Call Start to begin watching.
func NewWatcher(dir string, debounce time.Duration, onChange func()) (*Watcher, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", abs)
	}
	if debounce <= 0 {
		debounce = DefaultWatchDebounce
	}
	return &Watcher{dir: abs, debounce: debounce, onChange: onChange}, nil
}

// Dir returns the watched root directory.
func (w *Watcher) Dir() string { return w.dir }

// Start begins watching the directory tree recursively.
func (w *Watcher) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.watcher != nil {
		return nil // already started
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	w.watcher = fw
	w.done = make(chan struct{})

	// Add every existing subdirectory (except ignored ones).
	err = filepath.WalkDir(w.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != w.dir && defaultIgnores[d.Name()] {
				return filepath.SkipDir
			}
			// Best effort: a dir that can't be watched shouldn't kill startup.
			_ = fw.Add(path)
		}
		return nil
	})
	if err != nil {
		_ = fw.Close()
		w.watcher = nil
		return err
	}

	w.wg.Add(1)
	go w.loop()
	return nil
}

// addRecursive adds dir and all its subdirectories to the watcher.
func (w *Watcher) addRecursive(dir string) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != dir && defaultIgnores[d.Name()] {
				return filepath.SkipDir
			}
			_ = w.watcher.Add(path)
		}
		return nil
	})
}

// loop processes filesystem events with debouncing.
func (w *Watcher) loop() {
	defer w.wg.Done()
	var timer *time.Timer
	fire := func() {
		if w.onChange != nil {
			w.onChange()
		}
	}
	for {
		select {
		case <-w.done:
			if timer != nil {
				timer.Stop()
			}
			return
		case ev, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			// New directories need to be watched too.
			if ev.Op&(fsnotify.Create) != 0 {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
					w.addRecursive(ev.Name)
				}
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue // ignore chmod-only noise
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(w.debounce, fire)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			_ = err // best effort: a single failed watch shouldn't stop the loop
		}
	}
}

// Close stops the watcher and waits for the event loop to exit.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	if w.done != nil {
		close(w.done)
	}
	fw := w.watcher
	w.mu.Unlock()

	w.wg.Wait()
	if fw != nil {
		return fw.Close()
	}
	return nil
}
