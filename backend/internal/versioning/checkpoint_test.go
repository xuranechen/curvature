package versioning

import (
	"testing"
	"time"
)

// takeCheckpoint records one automatic checkpoint.
func takeCheckpoint(t *testing.T, store *Store) Commit {
	t.Helper()
	result, err := store.Snapshot(SnapshotOptions{Message: "auto checkpoint", Checkpoint: true})
	if err != nil {
		t.Fatalf("checkpoint snapshot: %v", err)
	}
	if !result.Created {
		t.Fatalf("checkpoint was skipped: %s", result.Reason)
	}
	return result.Commit
}

// Live checkpoints are reachable only through refs/checkpoints. If GC's
// reachability walk forgets that namespace, every checkpoint is collected on the
// next run — so this is the guard for that specific mistake.
func TestCheckpointsSurviveGC(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "main")
	mustSnapshot(t, store, "main")

	var checkpoints []Commit
	for _, content := range []string{"c1", "c2", "c3"} {
		writeProjectFile(t, root, "a.txt", content)
		checkpoints = append(checkpoints, takeCheckpoint(t, store))
	}

	// A nanosecond grace makes every object eligible by age, so only
	// reachability can keep them.
	result, err := store.GC(GCOptions{GracePeriod: time.Nanosecond})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if result.Removed != 0 {
		t.Fatalf("GC removed %d objects although the checkpoints are referenced", result.Removed)
	}
	for _, checkpoint := range checkpoints {
		if !hasObject(store.Dir(), checkpoint.Hash) {
			t.Fatalf("checkpoint %s was collected while its ref still exists", checkpoint.Hash)
		}
	}
	if store.CheckpointCount() != 3 {
		t.Fatalf("CheckpointCount = %d, want 3", store.CheckpointCount())
	}
}

// Checkpoints must be parentless. A parent link would keep every earlier
// checkpoint reachable and therefore unprunable.
func TestCheckpointsAreParentless(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "main")
	mustSnapshot(t, store, "main")

	for _, content := range []string{"c1", "c2", "c3"} {
		writeProjectFile(t, root, "a.txt", content)
		checkpoint := takeCheckpoint(t, store)
		if len(checkpoint.Parents) != 0 {
			t.Fatalf("checkpoint %s has parents %v, want none", checkpoint.Hash, checkpoint.Parents)
		}
	}
}

// Recording a checkpoint must not move the user's timeline or HEAD.
func TestCheckpointsDoNotAdvanceTheBranch(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "main")
	mainCommit := mustSnapshot(t, store, "main")

	writeProjectFile(t, root, "a.txt", "changed by an agent")
	takeCheckpoint(t, store)

	branch, head, detached, err := store.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if detached {
		t.Fatal("HEAD became detached")
	}
	if branch != DefaultBranch {
		t.Fatalf("branch = %q, want %q", branch, DefaultBranch)
	}
	if head != mainCommit.Hash {
		t.Fatalf("HEAD moved to %s, want the main snapshot %s", head, mainCommit.Hash)
	}
	// The checkpoint content is recorded, but the user's timeline still shows
	// the tree as dirty relative to it.
	status, err := store.Status(0)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Clean {
		t.Fatal("working tree reported clean although it differs from the user's snapshot")
	}
}

// An unchanged tree must not produce a second checkpoint, or the 30s debounce
// would fill the store with identical entries.
func TestUnchangedTreeDoesNotRepeatCheckpoint(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "main")
	mustSnapshot(t, store, "main")

	writeProjectFile(t, root, "a.txt", "once")
	takeCheckpoint(t, store)

	result, err := store.Snapshot(SnapshotOptions{Message: "auto checkpoint", Checkpoint: true})
	if err != nil {
		t.Fatalf("second checkpoint: %v", err)
	}
	if result.Created {
		t.Fatal("an unchanged tree produced a second checkpoint")
	}
	if store.CheckpointCount() != 1 {
		t.Fatalf("CheckpointCount = %d, want 1", store.CheckpointCount())
	}
}

// Retention: keep the newest N and let GC reclaim the rest, without touching the
// user's timeline.
func TestPruneCheckpointsDropsOldestAndGCReclaims(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "main")
	mainCommit := mustSnapshot(t, store, "main")

	var checkpoints []Commit
	for _, content := range []string{"c1", "c2", "c3", "c4"} {
		writeProjectFile(t, root, "a.txt", content)
		checkpoints = append(checkpoints, takeCheckpoint(t, store))
	}

	pruned, err := store.PruneCheckpoints(2)
	if err != nil {
		t.Fatalf("PruneCheckpoints: %v", err)
	}
	if pruned.Dropped != 2 || pruned.Kept != 2 {
		t.Fatalf("pruned = %+v, want 2 dropped and 2 kept", pruned)
	}
	if store.CheckpointCount() != 2 {
		t.Fatalf("CheckpointCount = %d, want 2", store.CheckpointCount())
	}

	result, err := store.GC(GCOptions{GracePeriod: time.Nanosecond})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if result.Removed == 0 {
		t.Fatal("GC reclaimed nothing after the checkpoint refs were pruned")
	}
	for _, dropped := range checkpoints[:2] {
		if hasObject(store.Dir(), dropped.Hash) {
			t.Fatalf("pruned checkpoint %s survived GC", dropped.Hash)
		}
	}
	for _, kept := range checkpoints[2:] {
		if !hasObject(store.Dir(), kept.Hash) {
			t.Fatalf("retained checkpoint %s was collected", kept.Hash)
		}
	}
	// The user's own timeline must be completely unaffected.
	if _, err := store.readCommitAt(mainCommit.Hash); err != nil {
		t.Fatalf("the main branch commit was collected: %v", err)
	}
	if err := store.Verify(); err != nil {
		t.Fatalf("Verify after prune + GC: %v", err)
	}
}

// A user's snapshots must never be touched by checkpoint retention, even when
// the two coexist.
func TestPruneCheckpointsLeavesUserHistoryAlone(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "one")
	first := mustSnapshot(t, store, "first")
	writeProjectFile(t, root, "a.txt", "two")
	second := mustSnapshot(t, store, "second")

	for _, content := range []string{"c1", "c2", "c3"} {
		writeProjectFile(t, root, "a.txt", content)
		takeCheckpoint(t, store)
	}

	if _, err := store.PruneCheckpoints(0); err != nil {
		t.Fatalf("PruneCheckpoints(0): %v", err)
	}
	if store.CheckpointCount() != 0 {
		t.Fatalf("CheckpointCount = %d, want 0", store.CheckpointCount())
	}
	if _, err := store.GC(GCOptions{GracePeriod: time.Nanosecond}); err != nil {
		t.Fatalf("GC: %v", err)
	}
	for _, commit := range []Commit{first, second} {
		if _, err := store.readCommitAt(commit.Hash); err != nil {
			t.Fatalf("user snapshot %s was lost: %v", commit.Hash, err)
		}
	}
	commits, err := store.Log(LogOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("user history has %d commits, want 2", len(commits))
	}
}

// Pruning below the current count must be a no-op rather than an error.
func TestPruneCheckpointsNoOpUnderLimit(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "main")
	mustSnapshot(t, store, "main")
	writeProjectFile(t, root, "a.txt", "c1")
	takeCheckpoint(t, store)

	for _, keep := range []int{1, 5, 0 + 100} {
		pruned, err := store.PruneCheckpoints(keep)
		if err != nil {
			t.Fatalf("PruneCheckpoints(%d): %v", keep, err)
		}
		if pruned.Dropped != 0 {
			t.Fatalf("PruneCheckpoints(%d) dropped %d, want 0", keep, pruned.Dropped)
		}
	}
	if store.CheckpointCount() != 1 {
		t.Fatalf("CheckpointCount = %d, want 1", store.CheckpointCount())
	}
}
