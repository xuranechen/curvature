package versioning

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func newTestScheduler(t *testing.T, store *Store, cfg AutoSchedulerConfig) *AutoScheduler {
	t.Helper()
	cfg.Provider = func(string) (*Store, error) { return store, nil }
	scheduler := NewAutoScheduler(cfg)
	if scheduler == nil {
		t.Fatal("NewAutoScheduler returned nil for a valid provider")
	}
	return scheduler
}

func TestAutoSchedulerWaitsForQuietPeriod(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "one")
	mustSnapshot(t, store, "init")

	start := time.Now()
	current := start
	scheduler := newTestScheduler(t, store, AutoSchedulerConfig{
		Debounce: 30 * time.Second,
		MaxDelay: 5 * time.Minute,
		Now:      func() time.Time { return current },
	})
	writeProjectFile(t, root, "a.txt", "two")
	scheduler.Notify("root-1", "session:abc")

	current = start.Add(29 * time.Second)
	if got := scheduler.Flush(current); len(got) != 0 {
		t.Fatalf("flushed before the quiet period elapsed: %+v", got)
	}
	current = start.Add(30 * time.Second)
	results := scheduler.Flush(current)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(results), results)
	}
	if results[0].Err != nil {
		t.Fatalf("Flush error: %v", results[0].Err)
	}
	if !results[0].Created {
		t.Fatal("expected the pending activity to be recorded as a checkpoint")
	}
	if results[0].Hash == "" {
		t.Fatal("checkpoint reported no commit hash")
	}
}

// Activity that never goes quiet for a full debounce window must still be
// recorded once the maximum delay is reached.
func TestAutoSchedulerForcesCheckpointOnMaxDelay(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "one")
	mustSnapshot(t, store, "init")

	start := time.Now()
	current := start
	scheduler := newTestScheduler(t, store, AutoSchedulerConfig{
		Debounce: 30 * time.Second,
		MaxDelay: 100 * time.Second,
		Now:      func() time.Time { return current },
	})
	for elapsed := time.Duration(0); elapsed <= 90*time.Second; elapsed += 10 * time.Second {
		current = start.Add(elapsed)
		writeProjectFile(t, root, "a.txt", fmt.Sprintf("v%d", elapsed))
		scheduler.Notify("root-1", "session:abc")
		current = start.Add(elapsed + 9*time.Second)
		if got := scheduler.Flush(current); len(got) != 0 {
			t.Fatalf("flushed at %s while activity was still arriving: %+v", elapsed, got)
		}
	}

	current = start.Add(101 * time.Second)
	results := scheduler.Flush(current)
	if len(results) != 1 {
		t.Fatalf("got %d results after the maximum delay, want 1: %+v", len(results), results)
	}
	if !results[0].Created {
		t.Fatal("expected the overdue activity to be recorded")
	}
}

// A checkpoint that finds no new content must not fabricate a commit.
func TestAutoSchedulerSkipsUnchangedContent(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "one")
	mustSnapshot(t, store, "init")

	scheduler := newTestScheduler(t, store, AutoSchedulerConfig{})
	scheduler.Notify("root-1", "session:abc")

	results := scheduler.Flush(time.Now().Add(time.Minute))
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(results), results)
	}
	if results[0].Created {
		t.Fatal("recorded a checkpoint although the working tree was unchanged")
	}
	if results[0].Err != nil {
		t.Fatalf("Flush error: %v", results[0].Err)
	}
}

// Disabling automatic checkpoints must also drop activity already queued, so
// re-enabling later cannot replay a stale backlog.
func TestAutoSchedulerDropsActivityWhileDisabled(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "one")
	mustSnapshot(t, store, "init")

	enabled := false
	scheduler := newTestScheduler(t, store, AutoSchedulerConfig{
		Enabled: func() bool { return enabled },
	})

	writeProjectFile(t, root, "a.txt", "two")
	scheduler.Notify("root-1", "session:abc")
	if got := scheduler.Flush(time.Now().Add(time.Hour)); len(got) != 0 {
		t.Fatalf("flushed while disabled: %+v", got)
	}

	enabled = true
	if got := scheduler.Flush(time.Now().Add(2 * time.Hour)); len(got) != 0 {
		t.Fatalf("replayed a backlog accumulated while disabled: %+v", got)
	}
}

func TestAutoSchedulerReportsProviderFailure(t *testing.T) {
	sentinel := errors.New("root unavailable")
	var reported []string
	scheduler := NewAutoScheduler(AutoSchedulerConfig{
		Provider: func(string) (*Store, error) { return nil, sentinel },
		OnError:  func(rootID string, err error) { reported = append(reported, rootID) },
	})
	if scheduler == nil {
		t.Fatal("NewAutoScheduler returned nil")
	}

	scheduler.Notify("root-9", "session:abc")
	results := scheduler.Flush(time.Now().Add(time.Minute))
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(results), results)
	}
	if !errors.Is(results[0].Err, sentinel) {
		t.Fatalf("result error = %v, want %v", results[0].Err, sentinel)
	}
	if len(reported) != 1 || reported[0] != "root-9" {
		t.Fatalf("OnError reported %v, want [root-9]", reported)
	}
}

// The reason supplied at notification time must survive into the commit so the
// timeline can explain why an automatic checkpoint exists.
func TestAutoSchedulerRecordsOriginReason(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "one")
	mustSnapshot(t, store, "init")

	scheduler := newTestScheduler(t, store, AutoSchedulerConfig{})
	writeProjectFile(t, root, "a.txt", "two")
	scheduler.Notify("root-1", "session:abc123")

	results := scheduler.Flush(time.Now().Add(time.Minute))
	if len(results) != 1 || !results[0].Created {
		t.Fatalf("expected one created checkpoint, got %+v", results)
	}
	commit, err := store.readCommitAt(results[0].Hash)
	if err != nil {
		t.Fatalf("readCommitAt(%s): %v", results[0].Hash, err)
	}
	if commit.Origin == nil {
		t.Fatal("automatic checkpoint has no origin")
	}
	if commit.Origin.Kind != OriginAuto {
		t.Fatalf("origin kind = %q, want %q", commit.Origin.Kind, OriginAuto)
	}
	if commit.Origin.Label != "session:abc123" {
		t.Fatalf("origin label = %q, want %q", commit.Origin.Label, "session:abc123")
	}
}

// The scheduler is attached to shared application state, so a nil receiver must
// stay inert rather than panic at a call site.
func TestAutoSchedulerNilReceiverIsInert(t *testing.T) {
	var scheduler *AutoScheduler
	scheduler.Notify("root-1", "session:abc")
	if got := scheduler.Flush(time.Now()); len(got) != 0 {
		t.Fatalf("nil scheduler flushed %+v", got)
	}
	if scheduler.Enabled() {
		t.Fatal("nil scheduler reported itself enabled")
	}
	if got := NewAutoScheduler(AutoSchedulerConfig{}); got != nil {
		t.Fatalf("NewAutoScheduler without a provider returned %+v, want nil", got)
	}
}
