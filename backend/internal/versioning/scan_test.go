package versioning

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// The stat cache is what makes a repeated scan cheap: an unchanged tree must not
// be read from disk at all. Without this the 30s auto-checkpoint would re-read
// every file in the repository on every tick.
func TestStatCacheSkipsRehashingUnchangedFiles(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "one")
	writeProjectFile(t, root, "nested/b.txt", "two")
	writeProjectFile(t, root, "nested/deep/c.txt", "three")
	mustSnapshot(t, store, "first")

	var reads int64
	previous := readFileForHash
	readFileForHash = func(name string) ([]byte, error) {
		atomic.AddInt64(&reads, 1)
		return previous(name)
	}
	defer func() { readFileForHash = previous }()

	// An unchanged tree must not be re-read at all.
	if _, err := store.Status(0); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := atomic.LoadInt64(&reads); got != 0 {
		t.Fatalf("re-read %d unchanged files, want 0", got)
	}

	// Changing one file must re-read exactly that file.
	writeProjectFile(t, root, "nested/b.txt", "changed contents")
	if _, err := store.Status(0); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := atomic.LoadInt64(&reads); got != 1 {
		t.Fatalf("re-read %d files after a single change, want 1", got)
	}
}

// A hash the cache remembers is only usable while its object still exists;
// otherwise the manifest would point at a collected blob.
func TestStatCacheMissesWhenObjectIsGone(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "content")
	mustSnapshot(t, store, "first")

	cache := store.loadStatCache()
	entry, ok := cache.Entries["a.txt"]
	if !ok {
		t.Fatal("the stat cache did not record the file")
	}
	if _, ok := cachedHashHit(store.Dir(), "a.txt", entry.Size, entry.MTime, cache); !ok {
		t.Fatal("a live object was not accepted by the cache")
	}

	// Remove the object behind the remembered hash.
	path := filepath.Join(store.Dir(), objectRelPath(entry.Hash))
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove object: %v", err)
	}
	if _, ok := cachedHashHit(store.Dir(), "a.txt", entry.Size, entry.MTime, cache); ok {
		t.Fatal("the cache trusted a hash whose object is gone")
	}

	// A size or mtime change must also invalidate it.
	if _, ok := cachedHashHit(store.Dir(), "a.txt", entry.Size+1, entry.MTime, cache); ok {
		t.Fatal("the cache trusted a changed size")
	}
	if _, ok := cachedHashHit(store.Dir(), "a.txt", entry.Size, entry.MTime+1, cache); ok {
		t.Fatal("the cache trusted a changed mtime")
	}
}

func TestScanHashWorkersStayBounded(t *testing.T) {
	for _, pending := range []int{0, 1} {
		if got := scanHashWorkers(pending); got != 1 {
			t.Fatalf("scanHashWorkers(%d) = %d, want 1", pending, got)
		}
	}
	for _, pending := range []int{2, 8, 1000} {
		got := scanHashWorkers(pending)
		if got < 1 || got > maxScanHashWorkers {
			t.Fatalf("scanHashWorkers(%d) = %d, want 1..%d", pending, got, maxScanHashWorkers)
		}
		if got > pending {
			t.Fatalf("scanHashWorkers(%d) = %d, want no more than the pending count", pending, got)
		}
	}
}

// The parallel hash phase must not change the result: two trees with identical
// content must yield identical manifests, whatever order the workers finished in.
func TestParallelScanProducesIdenticalManifests(t *testing.T) {
	storeA, rootA := newTestStore(t)
	storeB, rootB := newTestStore(t)
	for i := 0; i < 96; i++ {
		rel := fmt.Sprintf("dir%02d/file%02d.txt", i%8, i)
		content := fmt.Sprintf("content %d", i)
		writeProjectFile(t, rootA, rel, content)
		writeProjectFile(t, rootB, rel, content)
	}

	first := mustSnapshot(t, storeA, "same")
	second := mustSnapshot(t, storeB, "same")
	// Commit hashes include a timestamp, so the manifest is what must match.
	if first.Manifest != second.Manifest {
		t.Fatalf("identical trees produced different manifests: %s vs %s",
			first.Manifest, second.Manifest)
	}
	if first.Stats.Files != 96 {
		t.Fatalf("scanned %d files, want 96", first.Stats.Files)
	}

	// Re-scanning the same tree is a no-op, which also requires every hash to be
	// reproduced exactly.
	again, err := storeA.Snapshot(SnapshotOptions{Message: "again"})
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if again.Created {
		t.Fatalf("an unchanged tree produced another commit: %s", again.Reason)
	}
}

// Many files holding identical content all resolve to a single object. This is
// the case that breaks when object writes run concurrently: every worker misses
// the existence check, and they all rename onto the same path.
func TestConcurrentIdenticalContentSharesOneObject(t *testing.T) {
	store, root := newTestStore(t)
	const files = 128
	for i := 0; i < files; i++ {
		rel := fmt.Sprintf("dir%02d/copy%03d.txt", i%8, i)
		writeProjectFile(t, root, rel, "identical payload")
	}

	commit := mustSnapshot(t, store, "identical")
	if commit.Stats.Files != files {
		t.Fatalf("scanned %d files, want %d", commit.Stats.Files, files)
	}
	if got := countObjectsByKind(t, store, ObjectKindBlob); got != 1 {
		t.Fatalf("stored %d blobs for identical content, want 1", got)
	}
	if err := store.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// A file that cannot be read must surface as an error rather than a silently
// missing entry. The store has to be initialized first, or Status returns before
// it ever scans.
func TestParallelScanReportsReadFailure(t *testing.T) {
	store, root := newTestStore(t)
	writeProjectFile(t, root, "a.txt", "content")
	mustSnapshot(t, store, "baseline")

	// Change the file so it must be re-read, then make that read fail.
	writeProjectFile(t, root, "a.txt", "different content")
	previous := readFileForHash
	readFileForHash = func(string) ([]byte, error) {
		return nil, fmt.Errorf("simulated read failure")
	}
	defer func() { readFileForHash = previous }()

	if _, err := store.Status(0); err == nil {
		t.Fatal("Status reported success although a file could not be read")
	}
}
