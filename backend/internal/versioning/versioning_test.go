package versioning

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	metaDir := filepath.Join(root, ".curvature")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir meta dir: %v", err)
	}
	return Open(root, metaDir), root
}

func writeProjectFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func readProjectFile(t *testing.T, root, rel string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false
		}
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data), true
}

func mustSnapshot(t *testing.T, store *Store, message string) Commit {
	t.Helper()
	result, err := store.Snapshot(SnapshotOptions{Message: message})
	if err != nil {
		t.Fatalf("Snapshot(%q): %v", message, err)
	}
	if !result.Created {
		t.Fatalf("Snapshot(%q) was skipped: %s", message, result.Reason)
	}
	return result.Commit
}

// ---------------------------------------------------------------------------
// objects
// ---------------------------------------------------------------------------

func TestObjectRoundTripAndAddressStability(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	first, err := writeBlob(store.Dir(), []byte("hello"))
	if err != nil {
		t.Fatalf("writeBlob: %v", err)
	}
	second, err := writeBlob(store.Dir(), []byte("hello"))
	if err != nil {
		t.Fatalf("writeBlob (repeat): %v", err)
	}
	if first != second {
		t.Fatalf("identical content produced different addresses: %s vs %s", first, second)
	}

	got, err := readBlob(store.Dir(), first)
	if err != nil {
		t.Fatalf("readBlob: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("readBlob = %q, want %q", got, "hello")
	}
}

func TestCorruptObjectIsReportedNotSilentlyIgnored(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "important\n")
	commit := mustSnapshot(t, store, "first")

	manifest, err := readManifest(store.Dir(), commit.Manifest)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	hash := manifest.Entries["a.txt"].Hash

	// Overwrite the object with garbage: a reader must refuse it rather than
	// hand back wrong content.
	objectPath := filepath.Join(store.Dir(), objectRelPath(hash))
	if err := os.WriteFile(objectPath, []byte("not a zlib stream"), 0o644); err != nil {
		t.Fatalf("corrupt object: %v", err)
	}

	if _, _, _, err := store.FileAt(commit.Hash, "a.txt"); !errors.Is(err, errCorruptObject) {
		t.Fatalf("FileAt on corrupt object error = %v, want errCorruptObject", err)
	}
	if err := store.Verify(); !errors.Is(err, errCorruptObject) {
		t.Fatalf("Verify error = %v, want errCorruptObject", err)
	}
}

// ---------------------------------------------------------------------------
// snapshots
// ---------------------------------------------------------------------------

func TestSnapshotTracksFilesAndSkipsIdempotently(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "alpha\n")
	writeProjectFile(t, root, "nested/b.txt", "beta\n")

	first := mustSnapshot(t, store, "baseline")
	if first.Stats.Files != 2 {
		t.Fatalf("Stats.Files = %d, want 2", first.Stats.Files)
	}
	if first.Stats.Added != 2 {
		t.Fatalf("Stats.Added = %d, want 2", first.Stats.Added)
	}

	// Nothing changed: no new commit may be produced.
	again, err := store.Snapshot(SnapshotOptions{Message: "no-op"})
	if err != nil {
		t.Fatalf("Snapshot (repeat): %v", err)
	}
	if again.Created {
		t.Fatalf("repeat snapshot created a commit; want it skipped")
	}
	if again.Commit.Hash != first.Hash {
		t.Fatalf("skipped snapshot HEAD = %s, want %s", again.Commit.Hash, first.Hash)
	}

	// Force still records an explicit checkpoint.
	forced, err := store.Snapshot(SnapshotOptions{Message: "forced", Force: true})
	if err != nil {
		t.Fatalf("Snapshot (force): %v", err)
	}
	if !forced.Created {
		t.Fatalf("forced snapshot was skipped")
	}
}

func TestTouchingAFileDoesNotCreateANewSnapshot(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "stable\n")
	first := mustSnapshot(t, store, "baseline")

	// Change only the mtime. Content addresses must not care about it.
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "a.txt"), future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	result, err := store.Snapshot(SnapshotOptions{Message: "after touch"})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if result.Created {
		t.Fatalf("touching a file created a new snapshot; mtime leaked into the manifest")
	}
	if result.Commit.Hash != first.Hash {
		t.Fatalf("HEAD moved from %s to %s on a touch", first.Hash, result.Commit.Hash)
	}
}

func TestIdenticalContentIsStoredOnce(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "one.txt", strings.Repeat("duplicate payload\n", 500))
	writeProjectFile(t, root, "two.txt", strings.Repeat("duplicate payload\n", 500))
	commit := mustSnapshot(t, store, "dedup")

	manifest, err := readManifest(store.Dir(), commit.Manifest)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	left := manifest.Entries["one.txt"].Hash
	right := manifest.Entries["two.txt"].Hash
	if left != right {
		t.Fatalf("identical content hashed differently: %s vs %s", left, right)
	}
	// The two files must share a single stored blob; the manifest and the
	// commit are the only other objects.
	if blobs := countObjectsByKind(t, store, ObjectKindBlob); blobs != 1 {
		t.Fatalf("blob count = %d, want 1 (identical content must be deduplicated)", blobs)
	}
	if count, _ := store.objectStats(); count != 3 {
		t.Fatalf("object count = %d, want 3 (one blob + one manifest + one commit)", count)
	}
}

// countObjectsByKind counts stored objects of one kind.
func countObjectsByKind(t *testing.T, store *Store, kind string) int {
	t.Helper()
	count := 0
	root := filepath.Join(store.Dir(), "objects")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		hash := filepath.Base(filepath.Dir(path)) + entry.Name()
		if got, _, err := readObject(store.Dir(), hash); err == nil && got == kind {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk objects: %v", err)
	}
	return count
}

func TestStatusReportsEachKindOfChange(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "keep.txt", "keep\n")
	writeProjectFile(t, root, "modify.txt", "before\n")
	writeProjectFile(t, root, "delete.txt", "gone\n")
	mustSnapshot(t, store, "baseline")

	writeProjectFile(t, root, "modify.txt", "after\n")
	writeProjectFile(t, root, "added.txt", "new\n")
	if err := os.Remove(filepath.Join(root, "delete.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	status, err := store.Status(0)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Clean || status.Dirty != 3 {
		t.Fatalf("Dirty = %d (clean=%v), want 3", status.Dirty, status.Clean)
	}
	byPath := map[string]string{}
	for _, item := range status.Items {
		byPath[item.Path] = item.Status
	}
	for path, want := range map[string]string{
		"added.txt":  StatusAdded,
		"modify.txt": StatusModified,
		"delete.txt": StatusDeleted,
	} {
		if byPath[path] != want {
			t.Fatalf("status[%s] = %q, want %q", path, byPath[path], want)
		}
	}
}

// ---------------------------------------------------------------------------
// restore
// ---------------------------------------------------------------------------

func TestRestoreRoundTrip(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "version one\n")
	writeProjectFile(t, root, "sub/b.txt", "nested one\n")
	baseline := mustSnapshot(t, store, "baseline")

	writeProjectFile(t, root, "a.txt", "version two\n")
	writeProjectFile(t, root, "sub/b.txt", "nested two\n")
	result, err := store.Snapshot(SnapshotOptions{Message: "second"})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if result.Commit.Stats.Modified != 2 {
		t.Fatalf("Stats.Modified = %d, want 2", result.Commit.Stats.Modified)
	}

	// Overwrite mode is asked for by name here: its whole point is that it leaves
	// HEAD untouched, so it can only be tested against that mode explicitly.
	if _, err := store.Restore(RestoreOptions{Revision: baseline.Hash, Mode: RestoreModeOverwrite}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if content, _ := readProjectFile(t, root, "a.txt"); content != "version one\n" {
		t.Fatalf("a.txt = %q, want %q", content, "version one\n")
	}
	if content, _ := readProjectFile(t, root, "sub/b.txt"); content != "nested one\n" {
		t.Fatalf("sub/b.txt = %q, want %q", content, "nested one\n")
	}

	// Overwrite mode deliberately leaves HEAD untouched, so the tree is now
	// dirty relative to the newer commit. The invariant that matters is that
	// every tracked byte matches the target revision.
	baselineManifest, err := readManifest(store.Dir(), baseline.Manifest)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	for path, entry := range baselineManifest.Entries {
		_, _, hash, err := store.FileAt(WorktreeRev, path)
		if err != nil {
			t.Fatalf("FileAt(worktree, %s): %v", path, err)
		}
		if hash != entry.Hash {
			t.Fatalf("%s does not match the target revision", path)
		}
	}
	status, err := store.Status(0)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Dirty != 2 {
		t.Fatalf("overwrite restore left %d dirty paths, want 2 (HEAD is unchanged by design)", status.Dirty)
	}
}

func TestRestoreDeletesFilesAddedAfterTarget(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "one\n")
	baseline := mustSnapshot(t, store, "baseline")

	writeProjectFile(t, root, "later.txt", "added later\n")
	writeProjectFile(t, root, "deep/nested/later.txt", "added later\n")
	mustSnapshot(t, store, "later")

	if _, err := store.Restore(RestoreOptions{Revision: baseline.Hash}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	for _, path := range []string{"later.txt", "deep/nested/later.txt"} {
		if _, exists := readProjectFile(t, root, path); exists {
			t.Fatalf("%s still exists after a whole-tree restore to an earlier revision", path)
		}
	}
	if content, _ := readProjectFile(t, root, "a.txt"); content != "one\n" {
		t.Fatalf("a.txt = %q, want %q", content, "one\n")
	}
}

func TestRestoreCanBeUndoneViaSafetySnapshot(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "original\n")
	baseline := mustSnapshot(t, store, "baseline")

	writeProjectFile(t, root, "a.txt", "agent rewrite\n")
	mustSnapshot(t, store, "agent")

	result, err := store.Restore(RestoreOptions{Revision: baseline.Hash})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if result.SafetyCommit == "" {
		t.Fatalf("Restore did not record a safety snapshot")
	}
	if content, _ := readProjectFile(t, root, "a.txt"); content != "original\n" {
		t.Fatalf("a.txt = %q, want %q", content, "original\n")
	}

	// Undo the undo: the safety snapshot must hold the pre-restore content.
	if _, err := store.Restore(RestoreOptions{Revision: result.SafetyCommit}); err != nil {
		t.Fatalf("Restore(safety): %v", err)
	}
	if content, _ := readProjectFile(t, root, "a.txt"); content != "agent rewrite\n" {
		t.Fatalf("after undoing the restore a.txt = %q, want %q", content, "agent rewrite\n")
	}
}

// Scope must mean the same thing in both modes. It used to be skipped for revert,
// which rewrote the whole tree instead.
func TestScopedRestoreLeavesOtherPathsAlone(t *testing.T) {
	for _, mode := range []string{RestoreModeRevert, RestoreModeOverwrite} {
		t.Run(mode, func(t *testing.T) {
			store, root := newTestStore(t)
			writeProjectFile(t, root, "left/a.txt", "left one\n")
			writeProjectFile(t, root, "right/b.txt", "right one\n")
			baseline := mustSnapshot(t, store, "baseline")

			writeProjectFile(t, root, "left/a.txt", "left two\n")
			writeProjectFile(t, root, "right/b.txt", "right two\n")
			mustSnapshot(t, store, "second")

			if _, err := store.Restore(RestoreOptions{Revision: baseline.Hash, Mode: mode, Paths: []string{"left"}}); err != nil {
				t.Fatalf("Restore: %v", err)
			}
			if content, _ := readProjectFile(t, root, "left/a.txt"); content != "left one\n" {
				t.Fatalf("left/a.txt = %q, want %q", content, "left one\n")
			}
			if content, _ := readProjectFile(t, root, "right/b.txt"); content != "right two\n" {
				t.Fatalf("scoped restore touched right/b.txt: %q", content)
			}
		})
	}
}

func TestScopedRestoreDeletesOnlyInsideScope(t *testing.T) {
	for _, mode := range []string{RestoreModeRevert, RestoreModeOverwrite} {
		t.Run(mode, func(t *testing.T) {
			store, root := newTestStore(t)
			writeProjectFile(t, root, "keep/a.txt", "a\n")
			baseline := mustSnapshot(t, store, "baseline")

			writeProjectFile(t, root, "keep/new.txt", "new in scope\n")
			writeProjectFile(t, root, "outside/new.txt", "new out of scope\n")
			mustSnapshot(t, store, "later")

			if _, err := store.Restore(RestoreOptions{Revision: baseline.Hash, Mode: mode, Paths: []string{"keep"}}); err != nil {
				t.Fatalf("Restore: %v", err)
			}
			if _, exists := readProjectFile(t, root, "keep/new.txt"); exists {
				t.Fatalf("keep/new.txt should have been deleted by the scoped restore")
			}
			if _, exists := readProjectFile(t, root, "outside/new.txt"); !exists {
				t.Fatalf("outside/new.txt was deleted by a restore scoped to keep/")
			}
		})
	}
}

// A scoped revert commits the tree it actually produced. Committing the target
// manifest wholesale would record right/b.txt as reverted even though the scope
// excluded it, and the next Status would then call the tree clean while the file
// still holds the newer content.
func TestScopedRevertCommitsOnlyTheAppliedPaths(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "left/a.txt", "left one\n")
	writeProjectFile(t, root, "right/b.txt", "right one\n")
	baseline := mustSnapshot(t, store, "baseline")

	writeProjectFile(t, root, "left/a.txt", "left two\n")
	writeProjectFile(t, root, "right/b.txt", "right two\n")
	mustSnapshot(t, store, "second")

	result, err := store.Restore(RestoreOptions{
		Revision: baseline.Hash,
		Mode:     RestoreModeRevert,
		Paths:    []string{"left"},
	})
	if err != nil {
		t.Fatalf("Restore(revert, scoped): %v", err)
	}
	if result.Revision == baseline.Hash {
		t.Fatalf("scoped revert reused the target commit %s, want a new one", result.Revision)
	}

	// The commit must agree with the working tree: anything else and Status would
	// report the tree as dirty (or worse, as clean when it is not).
	status, err := store.Status(0)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Clean {
		t.Fatalf("tree dirty after a scoped revert: %d item(s)", status.Dirty)
	}

	// right/b.txt is out of scope, so the commit must NOT have rolled it back.
	_, _, committed, err := store.FileAt(result.Revision, "right/b.txt")
	if err != nil {
		t.Fatalf("FileAt(revert commit): %v", err)
	}
	_, _, rollback, err := store.FileAt(baseline.Hash, "right/b.txt")
	if err != nil {
		t.Fatalf("FileAt(baseline): %v", err)
	}
	if committed == rollback {
		t.Fatalf("the revert commit recorded right/b.txt as rolled back although the scope excluded it")
	}
}

func TestRevertRecordsForwardOnlyCommit(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "original\n")
	baseline := mustSnapshot(t, store, "baseline")

	writeProjectFile(t, root, "a.txt", "regression\n")
	broken := mustSnapshot(t, store, "broken")

	restored, err := store.Restore(RestoreOptions{
		Revision: baseline.Hash,
		Mode:     RestoreModeRevert,
		Message:  "roll back the regression",
	})
	if err != nil {
		t.Fatalf("Restore(revert): %v", err)
	}
	if restored.Revision == baseline.Hash || restored.Revision == broken.Hash {
		t.Fatalf("revert reused an existing commit %s; want a new one", restored.Revision)
	}
	if content, _ := readProjectFile(t, root, "a.txt"); content != "original\n" {
		t.Fatalf("a.txt = %q, want %q", content, "original\n")
	}

	// History must have moved forward: new commit -> broken -> baseline.
	commits, err := store.Log(LogOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("log length = %d, want 3", len(commits))
	}
	if commits[0].Hash != restored.Revision {
		t.Fatalf("newest commit = %s, want %s", commits[0].Hash, restored.Revision)
	}
	if len(commits[0].Parents) != 1 || commits[0].Parents[0] != broken.Hash {
		t.Fatalf("revert parents = %v, want [%s]", commits[0].Parents, broken.Hash)
	}
	if commits[0].Origin == nil || commits[0].Origin.Kind != OriginRevert {
		t.Fatalf("revert origin = %+v, want kind %q", commits[0].Origin, OriginRevert)
	}

	// The reverted content is committed, so the tree is clean.
	status, err := store.Status(0)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Clean {
		t.Fatalf("tree dirty after revert: %d items", status.Dirty)
	}
}

func TestRestoreRejectsPathsOutsideRoot(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "a\n")
	baseline := mustSnapshot(t, store, "baseline")

	if _, _, _, err := store.FileAt(baseline.Hash, "../../etc/passwd"); !errors.Is(err, ErrBadPath) {
		t.Fatalf("FileAt traversal error = %v, want ErrBadPath", err)
	}
	// The check lives inside path selection, so a mode that skipped selection also
	// skipped validation. Both modes must reject it.
	for _, mode := range []string{RestoreModeRevert, RestoreModeOverwrite} {
		if _, err := store.Restore(RestoreOptions{Revision: baseline.Hash, Mode: mode, Paths: []string{"../evil"}}); !errors.Is(err, ErrBadPath) {
			t.Fatalf("Restore(%s) traversal error = %v, want ErrBadPath", mode, err)
		}
	}
	if _, err := store.Timeline("../../etc/passwd", 10); !errors.Is(err, ErrBadPath) {
		t.Fatalf("Timeline traversal error = %v, want ErrBadPath", err)
	}
}

// ---------------------------------------------------------------------------
// history views
// ---------------------------------------------------------------------------

func TestTimelineListsOnlyRevisionsTouchingPath(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "target.txt", "one\n")
	writeProjectFile(t, root, "other.txt", "x\n")
	first := mustSnapshot(t, store, "add target")

	writeProjectFile(t, root, "other.txt", "y\n")
	mustSnapshot(t, store, "touch other only")

	writeProjectFile(t, root, "target.txt", "two\n")
	second := mustSnapshot(t, store, "change target")

	revisions, err := store.Timeline("target.txt", 10)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(revisions) != 2 {
		t.Fatalf("revisions = %d, want 2", len(revisions))
	}
	if revisions[0].Hash != second.Hash || revisions[0].Status != StatusModified {
		t.Fatalf("newest revision = %+v, want %s modified", revisions[0], second.Hash)
	}
	if revisions[0].Additions != 1 || revisions[0].Deletions != 1 {
		t.Fatalf("line delta = +%d -%d, want +1 -1", revisions[0].Additions, revisions[0].Deletions)
	}
	if revisions[1].Hash != first.Hash || revisions[1].Status != StatusAdded {
		t.Fatalf("oldest revision = %+v, want %s added", revisions[1], first.Hash)
	}
}

func TestChangesBetweenRevisions(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "line1\nline2\nline3\n")
	first := mustSnapshot(t, store, "first")

	writeProjectFile(t, root, "a.txt", "line1\nchanged\nline3\n")
	writeProjectFile(t, root, "b.txt", "brand new\n")
	second := mustSnapshot(t, store, "second")

	items, err := store.Changes(first.Hash, second.Hash)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("changes = %d, want 2", len(items))
	}
	byPath := map[string]ChangeItem{}
	for _, item := range items {
		byPath[item.Path] = item
	}
	if got := byPath["b.txt"]; got.Status != StatusAdded || got.Additions != 1 {
		t.Fatalf("b.txt = %+v, want added with 1 addition", got)
	}
	if got := byPath["a.txt"]; got.Status != StatusModified || got.Additions != 1 || got.Deletions != 1 {
		t.Fatalf("a.txt = %+v, want modified +1 -1", got)
	}
}

func TestUnifiedDiffUsesGitShape(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "x.go", "package main\n\nfunc main() {\n\tprintln(\"one\")\n}\n")
	first := mustSnapshot(t, store, "first")

	writeProjectFile(t, root, "x.go", "package main\n\nfunc main() {\n\tprintln(\"two\")\n}\n")
	second := mustSnapshot(t, store, "second")

	diff, err := store.FileDiff(first.Hash, second.Hash, "x.go", 3)
	if err != nil {
		t.Fatalf("FileDiff: %v", err)
	}
	if diff.Additions != 1 || diff.Deletions != 1 {
		t.Fatalf("diff = +%d -%d, want +1 -1", diff.Additions, diff.Deletions)
	}
	for _, want := range []string{
		"diff --git a/x.go b/x.go\n",
		"--- a/x.go\n",
		"+++ b/x.go\n",
		"@@ -1,5 +1,5 @@\n",
		"-\tprintln(\"one\")\n",
		"+\tprintln(\"two\")\n",
	} {
		if !strings.Contains(diff.Content, want) {
			t.Fatalf("diff missing %q:\n%s", want, diff.Content)
		}
	}
}

func TestBinaryFilesAreFlaggedAndNotLineDiffed(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "blob.bin", "head\x00tail")
	first := mustSnapshot(t, store, "first")

	manifest, err := readManifest(store.Dir(), first.Manifest)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if !manifest.Entries["blob.bin"].Binary {
		t.Fatalf("blob.bin was not flagged as binary")
	}

	writeProjectFile(t, root, "blob.bin", "head\x00changed")
	second := mustSnapshot(t, store, "second")

	diff, err := store.FileDiff(first.Hash, second.Hash, "blob.bin", 3)
	if err != nil {
		t.Fatalf("FileDiff: %v", err)
	}
	if !diff.Binary {
		t.Fatalf("binary diff was not flagged")
	}
	if diff.Content != "" {
		t.Fatalf("binary diff produced line content: %q", diff.Content)
	}
	if !strings.Contains(diff.Message, "Binary file changed") {
		t.Fatalf("binary diff message = %q", diff.Message)
	}
}

// ---------------------------------------------------------------------------
// timelines and tags
// ---------------------------------------------------------------------------

func TestBranchCreateAndCheckout(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "main one\n")
	first := mustSnapshot(t, store, "main baseline")

	if err := store.CreateBranch("experiment", first.Hash); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := store.CreateBranch("experiment", first.Hash); err == nil {
		t.Fatalf("CreateBranch allowed a duplicate timeline name")
	}

	writeProjectFile(t, root, "a.txt", "main two\n")
	mustSnapshot(t, store, "main advance")

	if _, err := store.Checkout("experiment", RestoreModeOverwrite); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if content, _ := readProjectFile(t, root, "a.txt"); content != "main one\n" {
		t.Fatalf("after checkout a.txt = %q, want %q", content, "main one\n")
	}

	branches, err := store.Branches()
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	current := ""
	for _, branch := range branches {
		if branch.Current {
			current = branch.Name
		}
	}
	if current != "experiment" {
		t.Fatalf("current timeline = %q, want %q", current, "experiment")
	}

	if _, err := store.Checkout("no-such-timeline", RestoreModeOverwrite); err == nil {
		t.Fatalf("Checkout accepted an unknown timeline")
	}
}

func TestTagsRoundTripAndSurviveGC(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "one\n")
	first := mustSnapshot(t, store, "first")

	if err := store.CreateTag("v1.0", first.Hash); err != nil {
		t.Fatalf("CreateTag: %v", err)
	}
	tags, err := store.Tags()
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 1 || tags[0].Name != "v1.0" || tags[0].Hash != first.Hash {
		t.Fatalf("tags = %+v, want v1.0 -> %s", tags, first.Hash)
	}

	loaded, err := store.readCommitAt("v1.0")
	if err != nil {
		t.Fatalf("resolve tag: %v", err)
	}
	if len(loaded.Tags) != 1 || loaded.Tags[0] != "v1.0" {
		t.Fatalf("commit tags = %v, want [v1.0]", loaded.Tags)
	}

	if _, err := store.GC(GCOptions{GracePeriod: 0}); err != nil {
		t.Fatalf("GC: %v", err)
	}
	if _, err := store.readCommitAt("v1.0"); err != nil {
		t.Fatalf("commit became unreadable after GC: %v", err)
	}

	if err := store.DeleteTag("v1.0"); err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}
	if err := store.DeleteTag("v1.0"); err == nil {
		t.Fatalf("DeleteTag allowed deleting a missing tag")
	}
}

func TestGCKeepsReachableAndRemovesStaleOrphans(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "tracked\n")
	commit := mustSnapshot(t, store, "baseline")

	// An orphan object that no ref reaches, old enough to be collected.
	orphan, err := writeBlob(store.Dir(), []byte("orphan payload"))
	if err != nil {
		t.Fatalf("writeBlob: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(store.Dir(), objectRelPath(orphan)), old, old); err != nil {
		t.Fatalf("chtimes orphan: %v", err)
	}

	result, err := store.GC(GCOptions{GracePeriod: time.Minute})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if result.Removed != 1 {
		t.Fatalf("GC removed %d objects, want 1", result.Removed)
	}
	if hasObject(store.Dir(), orphan) {
		t.Fatalf("orphan object survived GC")
	}
	if !hasObject(store.Dir(), orphan) && !hasObject(store.Dir(), commit.Manifest) {
		t.Fatalf("GC removed the reachable manifest")
	}
	if err := store.Verify(); err != nil {
		t.Fatalf("Verify after GC: %v", err)
	}
	if content, _ := readProjectFile(t, root, "a.txt"); content != "tracked\n" {
		t.Fatalf("working tree changed during GC: %q", content)
	}
}

// Retention cannot be expressed as garbage collection.
//
// GC computes reachability from refs (HEAD, branches, tags) by walking parent
// pointers, so every commit in a branch's history is reachable by construction
// and no commit is ever collectable. History only shrinks when a ref stops
// pointing at it. This is the constraint behind Q4: a retention policy needs a
// ref or history-model change, not a smarter GC.
func TestGCNeverCollectsCommitsOnABranchHistory(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "v1")
	oldest := mustSnapshot(t, store, "oldest")
	for _, content := range []string{"v2", "v3", "v4", "v5", "v6"} {
		writeProjectFile(t, root, "a.txt", content)
		mustSnapshot(t, store, "snapshot "+content)
	}

	// A grace period of one nanosecond makes every object eligible by age, so
	// reachability is the only thing that can keep them alive.
	result, err := store.GC(GCOptions{GracePeriod: time.Nanosecond})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if result.Removed != 0 {
		t.Fatalf("GC removed %d objects although every commit is reachable from HEAD", result.Removed)
	}
	if result.Kept == 0 {
		t.Fatal("GC reported keeping nothing, so the assertion above proves nothing")
	}
	if _, err := store.readCommitAt(oldest.Hash); err != nil {
		t.Fatalf("the oldest commit disappeared: %v", err)
	}
}

// Retention over a chained timeline is impossible by the constraint above: the
// oldest commits are ancestors of the newest, so walking parents always reaches
// them. Retention therefore relies on checkpoints being parentless; see
// checkpoint_test.go.

// ---------------------------------------------------------------------------
// ignore rules
// ---------------------------------------------------------------------------

func TestIgnoreRulesExcludeVCSDependenciesAndStore(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "keep.txt", "keep\n")
	writeProjectFile(t, root, ".git/config", "[core]\n")
	writeProjectFile(t, root, "node_modules/pkg/index.js", "module\n")
	writeProjectFile(t, root, ".curvature/state.json", "{}\n")
	writeProjectFile(t, root, "build.log", "noise\n")
	writeProjectFile(t, root, "important.log", "keep me\n")
	writeProjectFile(t, root, ".gitignore", "*.log\n!important.log\n")

	commit := mustSnapshot(t, store, "ignores")
	manifest, err := readManifest(store.Dir(), commit.Manifest)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}

	for _, path := range []string{".git/config", "node_modules/pkg/index.js", ".curvature/state.json", "build.log"} {
		if _, ok := manifest.Entries[path]; ok {
			t.Fatalf("%s should have been ignored", path)
		}
	}
	for _, path := range []string{"keep.txt", "important.log", ".gitignore"} {
		if _, ok := manifest.Entries[path]; !ok {
			t.Fatalf("%s should have been tracked (have %v)", path, keysOf(manifest.Entries))
		}
	}
}

func TestVersioningIgnoreFileIsHonored(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, ".versioningignore", "secrets/\n*.pem\n")
	writeProjectFile(t, root, "secrets/token.txt", "shh\n")
	writeProjectFile(t, root, "server.pem", "key\n")
	writeProjectFile(t, root, "app.go", "package main\n")

	commit := mustSnapshot(t, store, "custom ignores")
	manifest, err := readManifest(store.Dir(), commit.Manifest)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if _, ok := manifest.Entries["secrets/token.txt"]; ok {
		t.Fatalf("secrets/ was not ignored")
	}
	if _, ok := manifest.Entries["server.pem"]; ok {
		t.Fatalf("*.pem was not ignored")
	}
	if _, ok := manifest.Entries["app.go"]; !ok {
		t.Fatalf("app.go was incorrectly ignored")
	}
}

func keysOf(entries map[string]ManifestEntry) []string {
	out := make([]string, 0, len(entries))
	for key := range entries {
		out = append(out, key)
	}
	return out
}

// ---------------------------------------------------------------------------
// diff algorithm
// ---------------------------------------------------------------------------

func TestMyersDiffProducesApplicableEditScript(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
	}{
		{"both empty", nil, nil},
		{"identical", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"pure insert", []string{"a", "c"}, []string{"a", "b", "c"}},
		{"pure delete", []string{"a", "b", "c"}, []string{"a", "c"}},
		{"replace", []string{"a", "b", "c"}, []string{"a", "x", "c"}},
		{"empty to content", nil, []string{"a", "b"}},
		{"content to empty", []string{"a", "b"}, nil},
		{"disjoint", []string{"a", "b", "c"}, []string{"x", "y", "z"}},
		{"reorder", []string{"a", "b", "c", "d"}, []string{"b", "a", "d", "c"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ops, degraded := diffLines(testCase.a, testCase.b)
			if degraded {
				t.Fatalf("small input unexpectedly degraded")
			}

			// Replaying the script must reproduce b exactly.
			var rebuilt []string
			for _, op := range ops {
				if op.kind != diffDelete {
					rebuilt = append(rebuilt, op.text)
				}
			}
			if strings.Join(rebuilt, "\x00") != strings.Join(testCase.b, "\x00") {
				t.Fatalf("rebuilt = %v, want %v", rebuilt, testCase.b)
			}

			// Deleted lines must appear in a, in order.
			var deleted []string
			for _, op := range ops {
				if op.kind == diffDelete {
					deleted = append(deleted, op.text)
				}
			}
			if !isSubsequence(deleted, testCase.a) {
				t.Fatalf("deleted %v is not a subsequence of %v", deleted, testCase.a)
			}
		})
	}
}

func isSubsequence(sub, full []string) bool {
	i := 0
	for _, item := range full {
		if i < len(sub) && sub[i] == item {
			i++
		}
	}
	return i == len(sub)
}

func TestDiffLinesDegradesGracefullyOnHugeChange(t *testing.T) {
	a := make([]string, 0, 3000)
	b := make([]string, 0, 3000)
	for i := 0; i < 3000; i++ {
		a = append(a, "old-"+strings.Repeat("x", i%7))
		b = append(b, "new-"+strings.Repeat("y", i%11))
	}
	ops, degraded := diffLines(a, b)
	// Either a minimal script or an explicit degradation, but never silent
	// corruption: replaying must still yield b.
	var rebuilt []string
	for _, op := range ops {
		if op.kind != diffDelete {
			rebuilt = append(rebuilt, op.text)
		}
	}
	if strings.Join(rebuilt, "\x00") != strings.Join(b, "\x00") {
		t.Fatalf("rebuilt content differs (degraded=%v)", degraded)
	}
}

// ---------------------------------------------------------------------------
// ignore matcher unit coverage
// ---------------------------------------------------------------------------

func TestIgnoreMatcherSemantics(t *testing.T) {
	matcher := newIgnoreMatcher([]string{
		"*.log",
		"build/",
		"/root-only.txt",
		"docs/**/*.md",
		"temp",
		"cache/",
		"!keep.log",
	})

	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"app.log", false, true},
		{"deep/nested/app.log", false, true},
		{"keep.log", false, false},
		{"build", true, true},
		{"build/out.js", false, true},
		{"src/build/out.js", false, true},
		{"root-only.txt", false, true},
		{"nested/root-only.txt", false, false},
		{"docs/guide.md", false, true},
		{"docs/a/b/guide.md", false, true},
		// A bare name matches files and directories alike.
		{"temp", true, true},
		{"temp", false, true},
		// A trailing slash restricts the pattern to directories.
		{"cache", true, true},
		{"cache", false, false},
		{"cache/entry.js", false, true},
		{"src/main.go", false, false},
	}

	for _, testCase := range cases {
		if got := matcher.ignored(testCase.path, testCase.isDir); got != testCase.want {
			t.Fatalf("ignored(%q, dir=%v) = %v, want %v", testCase.path, testCase.isDir, got, testCase.want)
		}
	}
}

func TestInfoReportsStoreShape(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "one\n")
	writeProjectFile(t, root, "b.txt", "two\n")
	commit := mustSnapshot(t, store, "baseline")
	if err := store.CreateTag("checkpoint", commit.Hash); err != nil {
		t.Fatalf("CreateTag: %v", err)
	}

	info, err := store.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if !info.Initialized {
		t.Fatalf("Info reports uninitialized store")
	}
	if info.Branch != DefaultBranch || info.Head != commit.Hash {
		t.Fatalf("Info head = %s/%s, want %s/%s", info.Branch, info.Head, DefaultBranch, commit.Hash)
	}
	if info.Commits != 1 || info.Branches != 1 || info.Tags != 1 {
		t.Fatalf("Info counts = commits:%d branches:%d tags:%d, want 1/1/1",
			info.Commits, info.Branches, info.Tags)
	}
	if info.Tracked != 2 {
		t.Fatalf("Info.Tracked = %d, want 2", info.Tracked)
	}
	if info.Objects == 0 || info.StoreBytes == 0 {
		t.Fatalf("Info object stats = %d/%d, want non-zero", info.Objects, info.StoreBytes)
	}

	// Status on an untouched tree must be clean and initialized.
	status, err := store.Status(0)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Initialized || !status.Clean {
		t.Fatalf("Status = %+v, want initialized and clean", status)
	}
}
