package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"curvature/backend/internal/fs"
	"curvature/backend/internal/preferences"
	"curvature/backend/internal/versioning"
)

// newAutoCheckpointEnv wires an AppContext to a real root without an HTTP layer,
// so the automatic-checkpoint wiring can be exercised directly.
func newAutoCheckpointEnv(t *testing.T) (*AppContext, string, string) {
	t.Helper()
	base := t.TempDir()
	rootPath := filepath.Join(base, "project")
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	registry := fs.NewRegistry(filepath.Join(base, "registry.json"))
	// Pin the metadata to the project so the test never writes to the real home
	// directory.
	root, err := registry.UpsertWithMetaLocation(rootPath, fs.MetaLocationProject)
	if err != nil {
		t.Fatalf("register root: %v", err)
	}
	return &AppContext{Dirs: registry}, root.ID, rootPath
}

func writeAutoCheckpointFile(t *testing.T, rootPath, rel, content string) {
	t.Helper()
	full := filepath.Join(rootPath, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// The session hook reaches the scheduler through an optional interface, so the
// whole chain matters: provider resolution, notification, debounce, checkpoint.
func TestAutoCheckpointWiringRecordsSessionActivity(t *testing.T) {
	app, rootID, rootPath := newAutoCheckpointEnv(t)

	store, err := app.VersioningStore(rootID)
	if err != nil {
		t.Fatalf("VersioningStore: %v", err)
	}
	if store == nil {
		t.Fatal("VersioningStore returned no store for a registered root")
	}
	baseline, err := store.Snapshot(versioning.SnapshotOptions{Message: "baseline"})
	if err != nil {
		t.Fatalf("baseline snapshot: %v", err)
	}

	// A long debounce plus manual Flush keeps the test deterministic while still
	// going through the real Notify path.
	scheduler := versioning.NewAutoScheduler(versioning.AutoSchedulerConfig{
		Provider: app.VersioningStore,
		Debounce: time.Minute,
	})
	if scheduler == nil {
		t.Fatal("NewAutoScheduler returned nil")
	}
	app.AutoCheckpoints = scheduler

	// An agent writes a file; the session hook then reports the activity.
	writeAutoCheckpointFile(t, rootPath, "agent.txt", "produced by an agent")
	app.NotifyVersioningAutoCheckpoint(rootID, "sess-1")

	results := scheduler.Flush(time.Now().Add(2 * time.Minute))
	if len(results) != 1 {
		t.Fatalf("got %d checkpoint results, want 1: %+v", len(results), results)
	}
	if results[0].Err != nil {
		t.Fatalf("checkpoint failed: %v", results[0].Err)
	}
	if !results[0].Created {
		t.Fatal("session activity produced no checkpoint")
	}

	// A checkpoint must land on the automatic-checkpoint refs, never on the
	// user's timeline: recording a safety net must not move HEAD.
	branch, head, detached, err := store.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if detached || branch != versioning.DefaultBranch {
		t.Fatalf("branch = %q detached=%v, want %q", branch, detached, versioning.DefaultBranch)
	}
	if head != baseline.Commit.Hash {
		t.Fatalf("HEAD moved to %s, want the baseline %s", head, baseline.Commit.Hash)
	}

	// Read back the commit the scheduler reported by resolving it as a revision.
	commits, err := store.Log(versioning.LogOptions{Limit: 1, Branch: results[0].Hash})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("got %d commits for the checkpoint, want 1", len(commits))
	}
	if commits[0].Hash != results[0].Hash {
		t.Fatalf("Log returned %s, want the checkpoint %s", commits[0].Hash, results[0].Hash)
	}
	if len(commits[0].Parents) != 0 {
		t.Fatalf("checkpoint has parents %v, want none so retention can drop it", commits[0].Parents)
	}
	if commits[0].Origin == nil {
		t.Fatal("automatic checkpoint has no origin")
	}
	if commits[0].Origin.Kind != versioning.OriginAuto {
		t.Fatalf("origin kind = %q, want %q", commits[0].Origin.Kind, versioning.OriginAuto)
	}
	if commits[0].Origin.Label != "session:sess-1" {
		t.Fatalf("origin label = %q, want %q", commits[0].Origin.Label, "session:sess-1")
	}
}

// A root that cannot be resolved must not abort the scheduler: the provider
// reports it and the remaining roots still get their checkpoint.
func TestAutoCheckpointWiringIgnoresUnknownRoot(t *testing.T) {
	app, rootID, rootPath := newAutoCheckpointEnv(t)

	scheduler := versioning.NewAutoScheduler(versioning.AutoSchedulerConfig{
		Provider: app.VersioningStore,
		Debounce: time.Minute,
	})
	app.AutoCheckpoints = scheduler

	writeAutoCheckpointFile(t, rootPath, "agent.txt", "one")
	app.NotifyVersioningAutoCheckpoint("no-such-root", "sess-1")
	app.NotifyVersioningAutoCheckpoint(rootID, "sess-2")

	results := scheduler.Flush(time.Now().Add(2 * time.Minute))
	if len(results) != 2 {
		t.Fatalf("got %d checkpoint results, want 2: %+v", len(results), results)
	}
	var created, failed int
	for _, result := range results {
		switch {
		case result.Err != nil:
			failed++
		case result.Created:
			created++
		}
	}
	if failed != 1 {
		t.Fatalf("expected the unknown root to fail exactly once, got %d failures: %+v", failed, results)
	}
	if created != 1 {
		t.Fatalf("expected one checkpoint for the known root, got %d: %+v", created, results)
	}
}

// The user preference must gate the scheduler, and toggling it must take effect
// without restarting anything.
func TestAutoCheckpointWiringHonoursPreference(t *testing.T) {
	app, rootID, rootPath := newAutoCheckpointEnv(t)

	// A preference store backed by a temporary file, so the real user
	// preferences file is never touched.
	base := t.TempDir()
	prefs, err := preferences.NewStoreAt(filepath.Join(base, "preferences.json"))
	if err != nil {
		t.Fatalf("preferences.NewStoreAt: %v", err)
	}
	app.Prefs = prefs
	if err := prefs.UpdateVersioningAutoCheckpointsEnabled(false); err != nil {
		t.Fatalf("disable automatic checkpoints: %v", err)
	}

	scheduler := versioning.NewAutoScheduler(versioning.AutoSchedulerConfig{
		Provider: app.VersioningStore,
		Enabled:  app.versioningAutoCheckpointsEnabled,
		Debounce: time.Minute,
	})
	if scheduler == nil {
		t.Fatal("NewAutoScheduler returned nil")
	}
	app.AutoCheckpoints = scheduler

	writeAutoCheckpointFile(t, rootPath, "agent.txt", "one")
	app.NotifyVersioningAutoCheckpoint(rootID, "sess-1")
	if got := scheduler.Flush(time.Now().Add(2 * time.Minute)); len(got) != 0 {
		t.Fatalf("checkpointed while the preference was off: %+v", got)
	}

	// Re-enabling takes effect immediately: the scheduler reads the preference
	// per notification rather than caching it at startup.
	if err := prefs.UpdateVersioningAutoCheckpointsEnabled(true); err != nil {
		t.Fatalf("enable automatic checkpoints: %v", err)
	}
	app.NotifyVersioningAutoCheckpoint(rootID, "sess-2")
	results := scheduler.Flush(time.Now().Add(4 * time.Minute))
	if len(results) != 1 {
		t.Fatalf("got %d checkpoint results after re-enabling, want 1: %+v", len(results), results)
	}
	if results[0].Err != nil {
		t.Fatalf("checkpoint failed: %v", results[0].Err)
	}
	if !results[0].Created {
		t.Fatal("no checkpoint recorded after re-enabling")
	}
}

// The operator override can only force the feature off.
func TestAutoCheckpointsEnvOverrideForcesOff(t *testing.T) {
	for _, value := range []string{"0", "false", "OFF", "no", "No"} {
		t.Setenv(autoCheckpointsEnv, value)
		if autoCheckpointsEnvEnabled() {
			t.Fatalf("%q should disable automatic checkpoints", value)
		}
	}
	for _, value := range []string{"", "1", "true", "on", "yes", "unexpected"} {
		t.Setenv(autoCheckpointsEnv, value)
		if !autoCheckpointsEnvEnabled() {
			t.Fatalf("%q should leave the decision to the user preference", value)
		}
	}
}

// VersioningStore is the scheduler's provider; it must stay inert rather than
// panic for inputs it cannot serve.
// A kanban task reaching a terminal state must reach the scheduler too, tagged
// so the checkpoint can be told apart from a session-turn one.
func TestObserveTaskTerminalRecordsCheckpoint(t *testing.T) {
	app, rootID, rootPath := newAutoCheckpointEnv(t)

	store, err := app.VersioningStore(rootID)
	if err != nil {
		t.Fatalf("VersioningStore: %v", err)
	}
	if _, err := store.Snapshot(versioning.SnapshotOptions{Message: "baseline"}); err != nil {
		t.Fatalf("baseline snapshot: %v", err)
	}
	scheduler := versioning.NewAutoScheduler(versioning.AutoSchedulerConfig{
		Provider: app.VersioningStore,
		Debounce: time.Minute,
	})
	app.AutoCheckpoints = scheduler

	writeAutoCheckpointFile(t, rootPath, "task-output.txt", "produced by a kanban task")
	app.ObserveTaskTerminal(rootID, "task-42", "success")

	results := scheduler.Flush(time.Now().Add(2 * time.Minute))
	if len(results) != 1 {
		t.Fatalf("got %d checkpoint results, want 1: %+v", len(results), results)
	}
	if results[0].Err != nil {
		t.Fatalf("checkpoint failed: %v", results[0].Err)
	}
	if !results[0].Created {
		t.Fatal("a finished task produced no checkpoint")
	}

	commits, err := store.Log(versioning.LogOptions{Limit: 1, Branch: results[0].Hash})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 1 || commits[0].Origin == nil {
		t.Fatalf("checkpoint has no origin: %+v", commits)
	}
	if want := "task:task-42:success"; commits[0].Origin.Label != want {
		t.Fatalf("origin label = %q, want %q", commits[0].Origin.Label, want)
	}
	if commits[0].Origin.Kind != versioning.OriginAuto {
		t.Fatalf("origin kind = %q, want %q", commits[0].Origin.Kind, versioning.OriginAuto)
	}
	if len(commits[0].Parents) != 0 {
		t.Fatalf("checkpoint has parents %v, want none", commits[0].Parents)
	}
}

// The label must stay useful when the caller supplies nothing to identify the
// task with.
func TestObserveTaskTerminalWithoutIdentifiers(t *testing.T) {
	app, rootID, rootPath := newAutoCheckpointEnv(t)
	store, err := app.VersioningStore(rootID)
	if err != nil {
		t.Fatalf("VersioningStore: %v", err)
	}
	if _, err := store.Snapshot(versioning.SnapshotOptions{Message: "baseline"}); err != nil {
		t.Fatalf("baseline snapshot: %v", err)
	}
	scheduler := versioning.NewAutoScheduler(versioning.AutoSchedulerConfig{
		Provider: app.VersioningStore,
		Debounce: time.Minute,
	})
	app.AutoCheckpoints = scheduler

	writeAutoCheckpointFile(t, rootPath, "task-output.txt", "content")
	app.ObserveTaskTerminal(rootID, "  ", "")

	results := scheduler.Flush(time.Now().Add(2 * time.Minute))
	if len(results) != 1 || !results[0].Created {
		t.Fatalf("blank identifiers produced no checkpoint: %+v", results)
	}
	commits, err := store.Log(versioning.LogOptions{Limit: 1, Branch: results[0].Hash})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if commits[0].Origin == nil || commits[0].Origin.Label != "task" {
		t.Fatalf("origin label = %+v, want \"task\"", commits[0].Origin)
	}
}

func TestVersioningStoreHandlesMissingInputs(t *testing.T) {
	app, rootID, _ := newAutoCheckpointEnv(t)

	var nilApp *AppContext
	store, err := nilApp.VersioningStore(rootID)
	if err != nil || store != nil {
		t.Fatalf("nil AppContext returned (%v, %v), want (nil, nil)", store, err)
	}

	store, err = app.VersioningStore("   ")
	if err != nil || store != nil {
		t.Fatalf("blank root returned (%v, %v), want (nil, nil)", store, err)
	}

	if _, err := app.VersioningStore("no-such-root"); err == nil {
		t.Fatal("unknown root did not report an error")
	}
}
