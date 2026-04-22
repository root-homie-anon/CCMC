package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ccmc/pkg/ccmc"
)

// newTestRegistry creates a Registry backed by a temp directory so tests
// never touch the real ~/.ccmc path.
func newTestRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "registry.json")
	return NewRegistry(snapPath), snapPath
}

// makeSession is a zero-dependency helper that builds a minimal ccmc.Session.
func makeSession(id string, status ccmc.SessionStatus) ccmc.Session {
	return ccmc.Session{
		ID:          id,
		ProjectPath: "/tmp/" + id,
		ProjectName: id,
		Status:      status,
		StartedAt:   time.Now().Truncate(time.Second),
		LastActivity: time.Now().Truncate(time.Second),
	}
}

// --- CRUD ---

func TestAdd_insertsSession(t *testing.T) {
	r, _ := newTestRegistry(t)
	s := makeSession("abc", ccmc.SessionActive)
	r.Add(s)

	got, ok := r.Get("abc")
	if !ok {
		t.Fatal("expected session to be present after Add")
	}
	if got.ID != "abc" {
		t.Fatalf("got ID %q, want %q", got.ID, "abc")
	}
}

func TestAdd_replacesExisting(t *testing.T) {
	r, _ := newTestRegistry(t)
	r.Add(makeSession("abc", ccmc.SessionActive))

	updated := makeSession("abc", ccmc.SessionIdle)
	r.Add(updated)

	got, _ := r.Get("abc")
	if got.Status != ccmc.SessionIdle {
		t.Fatalf("expected replaced status %q, got %q", ccmc.SessionIdle, got.Status)
	}
}

func TestUpdate_returnsFalseForMissing(t *testing.T) {
	r, _ := newTestRegistry(t)
	ok := r.Update(makeSession("ghost", ccmc.SessionActive))
	if ok {
		t.Fatal("Update should return false for non-existent session")
	}
}

func TestUpdate_modifiesExisting(t *testing.T) {
	r, _ := newTestRegistry(t)
	r.Add(makeSession("s1", ccmc.SessionActive))

	s := makeSession("s1", ccmc.SessionDead)
	ok := r.Update(s)
	if !ok {
		t.Fatal("Update should return true for existing session")
	}

	got, _ := r.Get("s1")
	if got.Status != ccmc.SessionDead {
		t.Fatalf("expected status %q, got %q", ccmc.SessionDead, got.Status)
	}
}

func TestRemove_deletesSession(t *testing.T) {
	r, _ := newTestRegistry(t)
	r.Add(makeSession("rm-me", ccmc.SessionActive))
	r.Remove("rm-me")

	if _, ok := r.Get("rm-me"); ok {
		t.Fatal("session should be absent after Remove")
	}
}

func TestRemove_noopForMissing(t *testing.T) {
	r, _ := newTestRegistry(t)
	// Should not panic.
	r.Remove("does-not-exist")
}

func TestGet_returnsFalseForMissing(t *testing.T) {
	r, _ := newTestRegistry(t)
	if _, ok := r.Get("nope"); ok {
		t.Fatal("Get should return false for non-existent session")
	}
}

func TestList_returnsAllSessions(t *testing.T) {
	r, _ := newTestRegistry(t)
	ids := []string{"a", "b", "c"}
	for _, id := range ids {
		r.Add(makeSession(id, ccmc.SessionActive))
	}

	list := r.List()
	if len(list) != len(ids) {
		t.Fatalf("expected %d sessions, got %d", len(ids), len(list))
	}
}

func TestList_returnsCopy(t *testing.T) {
	r, _ := newTestRegistry(t)
	r.Add(makeSession("copy-test", ccmc.SessionActive))

	list := r.List()
	list[0].Status = ccmc.SessionDead // mutate the copy

	got, _ := r.Get("copy-test")
	if got.Status == ccmc.SessionDead {
		t.Fatal("mutating List() result should not affect registry")
	}
}

// --- Concurrent access ---

func TestConcurrentAddGet(t *testing.T) {
	r, _ := newTestRegistry(t)
	const workers = 50
	var wg sync.WaitGroup
	wg.Add(workers * 2)

	// writers
	for i := range workers {
		go func(n int) {
			defer wg.Done()
			id := string(rune('A' + n%26))
			r.Add(makeSession(id, ccmc.SessionActive))
		}(i)
	}

	// readers
	for range workers {
		go func() {
			defer wg.Done()
			_ = r.List()
		}()
	}

	wg.Wait() // race detector will flag any concurrent-write violations
}

func TestConcurrentUpdateRemove(t *testing.T) {
	r, _ := newTestRegistry(t)
	const sessions = 20
	for i := range sessions {
		id := string(rune('a' + i))
		r.Add(makeSession(id, ccmc.SessionActive))
	}

	var wg sync.WaitGroup
	wg.Add(sessions * 2)

	for i := range sessions {
		go func(n int) {
			defer wg.Done()
			id := string(rune('a' + n))
			r.Update(makeSession(id, ccmc.SessionIdle))
		}(i)
		go func(n int) {
			defer wg.Done()
			id := string(rune('a' + n))
			r.Remove(id)
		}(i)
	}

	wg.Wait()
}

// --- Snapshot round-trip ---

func TestSnapshotRoundTrip(t *testing.T) {
	r, snapPath := newTestRegistry(t)

	want := []ccmc.Session{
		makeSession("snap-1", ccmc.SessionActive),
		makeSession("snap-2", ccmc.SessionIdle),
	}
	for _, s := range want {
		r.Add(s)
	}

	if err := r.snapshot(); err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}

	// Verify the file exists and is valid JSON.
	data, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("could not read snapshot file: %v", err)
	}
	var decoded []ccmc.Session
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("snapshot is not valid JSON: %v", err)
	}

	// Load into a fresh registry and verify parity.
	r2 := NewRegistry(snapPath)
	r2.LoadFromSnapshot()

	list := r2.List()
	if len(list) != len(want) {
		t.Fatalf("expected %d sessions after reload, got %d", len(want), len(list))
	}
	for _, s := range want {
		got, ok := r2.Get(s.ID)
		if !ok {
			t.Errorf("session %q missing after reload", s.ID)
			continue
		}
		if got.Status != s.Status {
			t.Errorf("session %q: status %q, want %q", s.ID, got.Status, s.Status)
		}
	}
}

// --- Edge cases ---

func TestLoadFromSnapshot_missingFile(t *testing.T) {
	r, _ := newTestRegistry(t)
	// snapPath does not exist — LoadFromSnapshot must silently succeed.
	r.LoadFromSnapshot()

	if list := r.List(); len(list) != 0 {
		t.Fatalf("expected empty registry for missing snapshot, got %d sessions", len(list))
	}
}

func TestLoadFromSnapshot_corruptFile(t *testing.T) {
	r, snapPath := newTestRegistry(t)

	// Write garbage to the snapshot path.
	if err := os.WriteFile(snapPath, []byte("not { valid } json ]["), 0o600); err != nil {
		t.Fatalf("could not write corrupt snapshot: %v", err)
	}

	// Must not panic; registry should be empty.
	r.LoadFromSnapshot()

	if list := r.List(); len(list) != 0 {
		t.Fatalf("expected empty registry after corrupt snapshot, got %d sessions", len(list))
	}
}

// --- Snapshot loop ---

func TestStartSnapshotLoop_writesFile(t *testing.T) {
	r, snapPath := newTestRegistry(t)
	r.Add(makeSession("loop-1", ccmc.SessionActive))

	ctx, cancel := context.WithCancel(context.Background())
	r.StartSnapshotLoop(ctx, 50*time.Millisecond)

	// Wait for at least one tick.
	time.Sleep(120 * time.Millisecond)
	cancel() // triggers final snapshot + goroutine exit

	// Give the goroutine a moment to finish.
	time.Sleep(50 * time.Millisecond)

	if _, err := os.Stat(snapPath); os.IsNotExist(err) {
		t.Fatal("snapshot file not created by loop")
	}

	r2 := NewRegistry(snapPath)
	r2.LoadFromSnapshot()
	if _, ok := r2.Get("loop-1"); !ok {
		t.Fatal("session not found in registry reloaded from loop snapshot")
	}
}

func TestStartSnapshotLoop_finalSnapshotOnCancel(t *testing.T) {
	r, snapPath := newTestRegistry(t)

	ctx, cancel := context.WithCancel(context.Background())
	// Very long interval so no periodic tick fires; only final snapshot on cancel.
	r.StartSnapshotLoop(ctx, 10*time.Second)

	r.Add(makeSession("final-snap", ccmc.SessionActive))
	cancel()

	time.Sleep(100 * time.Millisecond)

	r2 := NewRegistry(snapPath)
	r2.LoadFromSnapshot()
	if _, ok := r2.Get("final-snap"); !ok {
		t.Fatal("session added before cancel not present in final snapshot")
	}
}
