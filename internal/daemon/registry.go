package daemon

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"ccmc/internal/config"
	"ccmc/pkg/ccmc"
)

// Registry is a concurrent-safe in-memory store of active ccmc.Session entries.
// It snapshots its state to disk periodically and can load from a prior snapshot
// on startup. The snapshot format is a JSON array of ccmc.Session values.
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]ccmc.Session
	snapPath string
}

// NewRegistry creates an empty Registry. The snapshot is persisted to snapPath;
// pass an empty string to use the default path from config.CcmcRegistryPath().
func NewRegistry(snapPath string) *Registry {
	if snapPath == "" {
		snapPath = config.CcmcRegistryPath()
	}
	return &Registry{
		sessions: make(map[string]ccmc.Session),
		snapPath: snapPath,
	}
}

// Add inserts a session. If a session with the same ID already exists it is
// replaced. This is intentional: hook events may replay a SessionStart.
func (r *Registry) Add(s ccmc.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[s.ID] = s
}

// Update replaces the session with the matching ID. Returns false when no
// session with that ID exists (the caller should call Add instead).
func (r *Registry) Update(s ccmc.Session) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[s.ID]; !ok {
		return false
	}
	r.sessions[s.ID] = s
	return true
}

// Remove deletes a session by ID. It is a no-op when the ID is not present.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, id)
}

// Get retrieves a single session by ID. The second return value is false when
// the ID is not found.
func (r *Registry) Get(id string) (ccmc.Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[id]
	return s, ok
}

// List returns a snapshot copy of all sessions in no guaranteed order.
// Callers receive their own slice — mutations do not affect the registry.
func (r *Registry) List() []ccmc.Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ccmc.Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	return out
}

// LoadFromSnapshot reads the snapshot file at r.snapPath and populates the
// registry. A missing file is silently ignored (fresh start). A present but
// unreadable or malformed file is logged and treated as an empty registry —
// the daemon still starts cleanly.
func (r *Registry) LoadFromSnapshot() {
	data, err := os.ReadFile(r.snapPath)
	if err != nil {
		if os.IsNotExist(err) {
			return // Normal: first run
		}
		log.Printf("registry: ignoring unreadable snapshot %s: %v", r.snapPath, err)
		return
	}

	var sessions []ccmc.Session
	if err := json.Unmarshal(data, &sessions); err != nil {
		log.Printf("registry: ignoring corrupt snapshot %s: %v", r.snapPath, err)
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range sessions {
		r.sessions[s.ID] = s
	}
}

// snapshot writes the current registry state to r.snapPath atomically. It
// writes to a sibling .tmp file then renames to avoid leaving a partial file
// visible to concurrent readers.
func (r *Registry) snapshot() error {
	sessions := r.List() // acquires/releases RLock internally

	data, err := json.Marshal(sessions)
	if err != nil {
		return err
	}

	dir := filepath.Dir(r.snapPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	tmp := r.snapPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, r.snapPath)
}

// StartSnapshotLoop starts a background goroutine that calls snapshot every
// interval (30 seconds in production). It stops cleanly when ctx is cancelled.
// This method is intentionally parameterised by interval so tests can pass a
// short value without sleeping.
func (r *Registry) StartSnapshotLoop(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				// Final snapshot on shutdown so no in-flight state is lost.
				if err := r.snapshot(); err != nil {
					log.Printf("registry: final snapshot failed: %v", err)
				}
				return
			case <-ticker.C:
				if err := r.snapshot(); err != nil {
					log.Printf("registry: snapshot failed: %v", err)
				}
			}
		}
	}()
}
