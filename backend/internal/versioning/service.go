package versioning

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// WorktreeRev is a pseudo-revision that reads a path from the working tree
// instead of from history.
const WorktreeRev = "WORKTREE"

// ErrPathNotFound is returned when a path does not exist at a revision.
var ErrPathNotFound = errors.New("versioning: path not found at revision")

// SnapshotOptions configures a manual snapshot.
type SnapshotOptions struct {
	Message string
	Author  string
	Origin  *Origin
	// Tag names the new snapshot as a bookmark in the same operation.
	Tag string
	// Force records a commit even when the content is unchanged.
	Force bool
	// Checkpoint records a parentless commit under the automatic-checkpoint ref
	// namespace instead of advancing the current timeline.
	//
	// Parentless is the whole point: a chained checkpoint would keep every
	// earlier checkpoint reachable through its parent pointer, so retention
	// could never drop one.
	Checkpoint bool
}

// SnapshotResult reports what a snapshot call actually did.
type SnapshotResult struct {
	Commit Commit `json:"commit"`
	// Created is false when the content was unchanged and no commit was made;
	// Commit then describes the existing HEAD.
	Created bool `json:"created"`
	// Reason explains a skipped snapshot.
	Reason string `json:"reason,omitempty"`
}

// LogOptions filters a history walk.
type LogOptions struct {
	Limit  int
	Branch string
	// Before starts the walk at the parent of this revision (pagination).
	Before string
	// Path restricts the walk to commits that changed this path.
	Path string
}

// RestoreOptions configures a working-tree restore.
type RestoreOptions struct {
	Revision string
	// Paths limits the restore to specific files or directories. Empty means
	// the whole project. Revert mode always restores everything.
	Paths []string
	// Mode is RestoreModeRevert (default) or RestoreModeOverwrite.
	Mode string
	// Message is used for the commit created by revert mode.
	Message string
	// SkipSafety disables the automatic pre-restore snapshot. It is only
	// appropriate when the caller has already secured the current state.
	SkipSafety bool
}

// GCOptions configures garbage collection.
type GCOptions struct {
	// GracePeriod protects freshly written objects. Defaults to one hour.
	GracePeriod time.Duration
	// Immediate treats every unreachable object as eligible regardless of age.
	//
	// It is for objects the caller has just orphaned on purpose, such as refs
	// removed by a retention pass. Without it the grace period would keep those
	// objects for another hour, so an explicit prune would appear to free
	// nothing.
	Immediate bool
}

// TreeEntry is one file below a directory inside a snapshot.
type TreeEntry struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Binary bool   `json:"binary,omitempty"`
}

// ---------------------------------------------------------------------------
// info
// ---------------------------------------------------------------------------

// Info summarises the store for a UI header.
func (s *Store) Info() (RepoInfo, error) {
	info := RepoInfo{
		RootPath:  s.rootPath,
		StorePath: s.storeDir,
	}
	if !s.Initialized() {
		return info, nil
	}
	info.Initialized = true

	if cfg, err := s.LoadConfig(); err == nil {
		info.CreatedAt = cfg.CreatedAt
		info.UpdatedAt = cfg.UpdatedAt
	}
	branch, head, detached, err := s.Head()
	if err != nil {
		return info, err
	}
	if detached {
		info.Branch = "(detached)"
	} else {
		info.Branch = branch
	}
	info.Head = head

	if head != "" {
		if commit, err := readCommit(s.storeDir, head); err == nil {
			info.Tracked = commit.Stats.Files
		}
	}
	commits, err := s.countCommits()
	if err != nil {
		return info, err
	}
	info.Commits = commits

	branches, err := s.Branches()
	if err != nil {
		return info, err
	}
	info.Branches = len(branches)

	tags, err := s.Tags()
	if err != nil {
		return info, err
	}
	info.Tags = len(tags)

	objects, bytes := s.objectStats()
	info.Objects = objects
	info.StoreBytes = bytes
	return info, nil
}

// countCommits counts reachable commits across all timelines and tags.
func (s *Store) countCommits() (int, error) {
	seen := map[string]bool{}
	for _, ref := range s.allRefHashes() {
		if err := s.walkParents(ref, func(Commit) error { return nil }, seen); err != nil {
			return 0, err
		}
	}
	return len(seen), nil
}

// allRefHashes returns every commit hash referenced by HEAD, timelines or tags.
func (s *Store) allRefHashes() []string {
	var hashes []string
	if _, head, _, err := s.Head(); err == nil && head != "" {
		hashes = append(hashes, head)
	}
	for _, dir := range []string{
		filepath.Join(s.storeDir, "refs", "heads"),
		filepath.Join(s.storeDir, "refs", "tags"),
		// Automatic checkpoints must be counted here. They are reachable only
		// through their own refs, so omitting this directory would let GC
		// collect every checkpoint on the next run.
		s.checkpointsRoot(),
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if hash, err := s.readRef(filepath.Join(dir, entry.Name())); err == nil && hash != "" {
				hashes = append(hashes, hash)
			}
		}
	}
	return hashes
}

// walkParents visits a commit and its ancestors in depth-first order.
func (s *Store) walkParents(hash string, visit func(Commit) error, seen map[string]bool) error {
	stack := []string{hash}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current == "" || seen[current] {
			continue
		}
		seen[current] = true
		commit, err := readCommit(s.storeDir, current)
		if err != nil {
			return err
		}
		if err := visit(commit); err != nil {
			return err
		}
		stack = append(stack, commit.Parents...)
	}
	return nil
}

// objectStats counts objects and their compressed size on disk.
func (s *Store) objectStats() (count int, bytes int64) {
	root := filepath.Join(s.storeDir, "objects")
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		count++
		bytes += info.Size()
		return nil
	})
	return count, bytes
}

// ---------------------------------------------------------------------------
// status / snapshot
// ---------------------------------------------------------------------------

// Status compares the working tree against HEAD.
func (s *Store) Status(limit int) (StatusResult, error) {
	result := StatusResult{Clean: true}
	if !s.Initialized() {
		return result, nil
	}
	result.Initialized = true

	cfg, err := s.LoadConfig()
	if err != nil {
		return result, err
	}
	manifest, _, _, err := s.scanWorkingTree(false, cfg)
	if err != nil {
		return result, err
	}

	branch, head, detached, err := s.Head()
	if err != nil {
		return result, err
	}
	if detached {
		result.Branch = "(detached)"
	} else {
		result.Branch = branch
	}
	result.Head = head

	items := make([]ChangeItem, 0)
	if head == "" {
		for path, entry := range manifest.Entries {
			items = append(items, ChangeItem{
				Path: path, Status: StatusAdded,
				NewHash: entry.Hash, NewSize: entry.Size, Binary: entry.Binary,
			})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	} else {
		commit, err := readCommit(s.storeDir, head)
		if err != nil {
			return result, err
		}
		headManifest, err := readManifest(s.storeDir, commit.Manifest)
		if err != nil {
			return result, err
		}
		items = diffManifests(headManifest, manifest)
	}

	result.Total = len(items)
	result.Dirty = len(items)
	result.Clean = len(items) == 0
	if limit > 0 && len(items) > limit {
		items = items[:limit]
		result.Truncated = true
	}
	for _, item := range items {
		result.Items = append(result.Items, FileStatus{
			Path: item.Path, Status: item.Status,
			Size: item.NewSize, Binary: item.Binary,
		})
	}
	return result, nil
}

// Snapshot records the working tree as a new point in time.
func (s *Store) Snapshot(opts SnapshotOptions) (SnapshotResult, error) {
	var result SnapshotResult
	err := s.withLock(func() error {
		restored, err := s.snapshotLocked(opts)
		if err != nil {
			return err
		}
		result = restored
		return nil
	})
	return result, err
}

// snapshotMessage applies the default message for an empty one.
func snapshotMessage(message string) string {
	if strings.TrimSpace(message) == "" {
		return "snapshot " + time.Now().UTC().Format("2006-01-02 15:04:05")
	}
	return message
}

func (s *Store) touchConfig() error {
	cfg, err := s.LoadConfig()
	if err != nil {
		return err
	}
	cfg.UpdatedAt = time.Now().UTC()
	return s.saveConfig(cfg)
}

// ---------------------------------------------------------------------------
// history
// ---------------------------------------------------------------------------

// Log walks history newest first.
func (s *Store) Log(opts LogOptions) ([]Commit, error) {
	if !s.Initialized() {
		return nil, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	start := opts.Branch
	if opts.Before != "" {
		start = strings.TrimSpace(opts.Before) + "~1"
	}
	hash, err := s.resolveCommit(start)
	if err != nil {
		if errors.Is(err, ErrBadRevision) {
			return nil, nil
		}
		return nil, err
	}

	var filter string
	if opts.Path != "" {
		if filter, err = normalizeRelPath(opts.Path); err != nil {
			return nil, err
		}
	}

	commits := make([]Commit, 0, limit)
	for hash != "" && len(commits) < limit {
		commit, err := readCommit(s.storeDir, hash)
		if err != nil {
			return nil, err
		}
		if filter == "" || s.commitTouches(commit, filter) {
			commit.Tags = s.tagsFor(hash)
			commits = append(commits, commit)
		}
		hash = firstParent(commit)
	}
	return commits, nil
}

// firstParent returns a commit's first parent, or "" for a root commit.
func firstParent(commit Commit) string {
	if len(commit.Parents) == 0 {
		return ""
	}
	return commit.Parents[0]
}

// commitTouches reports whether a path changed between a commit and its parent.
func (s *Store) commitTouches(commit Commit, rel string) bool {
	manifest, err := readManifest(s.storeDir, commit.Manifest)
	if err != nil {
		return false
	}
	current, ok := manifest.Entries[rel]
	if parent := firstParent(commit); parent != "" {
		parentCommit, err := readCommit(s.storeDir, parent)
		if err != nil {
			return true
		}
		parentManifest, err := readManifest(s.storeDir, parentCommit.Manifest)
		if err != nil {
			return true
		}
		previous, existed := parentManifest.Entries[rel]
		return !existed || previous.Hash != current.Hash
	}
	return ok
}

// Changes lists path-level differences between two revisions.
func (s *Store) Changes(fromRev, toRev string) ([]ChangeItem, error) {
	fromManifest, err := s.manifestOrEmpty(fromRev)
	if err != nil {
		return nil, err
	}
	toManifest, err := s.manifestOrEmpty(toRev)
	if err != nil {
		return nil, err
	}
	items := diffManifests(fromManifest, toManifest)
	return summarizeChanges(s.storeDir, items), nil
}

// manifestOrEmpty loads a manifest, treating an unborn revision as empty.
func (s *Store) manifestOrEmpty(rev string) (Manifest, error) {
	if strings.TrimSpace(rev) == "" || rev == WorktreeRev {
		return Manifest{Entries: map[string]ManifestEntry{}}, nil
	}
	manifest, _, err := s.manifestAt(rev)
	if err != nil {
		if errors.Is(err, ErrBadRevision) {
			return Manifest{Entries: map[string]ManifestEntry{}}, nil
		}
		return Manifest{}, err
	}
	return manifest, nil
}

// FileAt returns a path's content at a revision.
//
// The presence flag distinguishes "absent at this revision" from "present but
// empty", which a restore needs in order to delete correctly.
func (s *Store) FileAt(rev, pathStr string) (data []byte, present bool, hash string, err error) {
	rel, err := normalizeRelPath(pathStr)
	if err != nil {
		return nil, false, "", err
	}
	if rel == "" {
		return nil, false, "", ErrBadPath
	}

	switch strings.TrimSpace(rev) {
	case WorktreeRev:
		full, err := resolveInsideRoot(s.rootPath, rel)
		if err != nil {
			return nil, false, "", err
		}
		content, err := os.ReadFile(full)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, false, "", nil
			}
			return nil, false, "", err
		}
		return content, true, hashFramed(frameObject(ObjectKindBlob, content)), nil
	case "":
		return nil, false, "", nil
	}

	manifest, commitHash, err := s.manifestAt(rev)
	if err != nil {
		return nil, false, "", err
	}
	entry, ok := manifest.Entries[rel]
	if !ok {
		return nil, false, commitHash, nil
	}
	content, err := readBlob(s.storeDir, entry.Hash)
	if err != nil {
		return nil, false, commitHash, err
	}
	return content, true, entry.Hash, nil
}

// FileDiff renders a unified diff for one path between two revisions.
func (s *Store) FileDiff(fromRev, toRev, pathStr string, context int) (DiffResult, error) {
	rel, err := normalizeRelPath(pathStr)
	if err != nil {
		return DiffResult{}, err
	}
	if rel == "" {
		return DiffResult{}, ErrBadPath
	}
	if context <= 0 {
		context = defaultContext
	}

	oldContent, oldPresent, oldHash, err := s.FileAt(fromRev, rel)
	if err != nil {
		return DiffResult{}, err
	}
	newContent, newPresent, newHash, err := s.FileAt(toRev, rel)
	if err != nil {
		return DiffResult{}, err
	}

	switch {
	case !oldPresent && !newPresent:
		return DiffResult{Path: rel, Message: "path does not exist at either revision"}, nil
	case !oldPresent:
		return buildUnifiedDiff(rel, StatusAdded, "", newHash, nil, newContent, context), nil
	case !newPresent:
		return buildUnifiedDiff(rel, StatusDeleted, oldHash, "", oldContent, nil, context), nil
	default:
		return buildUnifiedDiff(rel, StatusModified, oldHash, newHash, oldContent, newContent, context), nil
	}
}

// Tree lists files under a directory at a revision.
func (s *Store) Tree(rev, dir string) ([]TreeEntry, error) {
	manifest, _, err := s.manifestAt(rev)
	if err != nil {
		return nil, err
	}
	prefix := ""
	if strings.TrimSpace(dir) != "" {
		normalized, err := normalizeRelPath(dir)
		if err != nil {
			return nil, err
		}
		prefix = normalized
	}

	entries := make([]TreeEntry, 0)
	for path, entry := range manifest.Entries {
		if prefix != "" && path != prefix && !strings.HasPrefix(path, prefix+"/") {
			continue
		}
		entries = append(entries, TreeEntry{
			Path:   path,
			Name:   path[strings.LastIndex(path, "/")+1:],
			Size:   entry.Size,
			Binary: entry.Binary,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// Timeline lists the revisions that changed a path, newest first.
func (s *Store) Timeline(pathStr string, limit int) ([]RevisionInfo, error) {
	if !s.Initialized() {
		return nil, nil
	}
	rel, err := normalizeRelPath(pathStr)
	if err != nil {
		return nil, err
	}
	if rel == "" {
		return nil, ErrBadPath
	}
	if limit <= 0 {
		limit = 50
	}

	_, head, _, err := s.Head()
	if err != nil {
		return nil, err
	}
	if head == "" {
		return nil, nil
	}

	revisions := make([]RevisionInfo, 0, limit)
	hash := head
	for hash != "" && len(revisions) < limit {
		commit, err := readCommit(s.storeDir, hash)
		if err != nil {
			return nil, err
		}
		current, currentOK := s.entryAt(commit, rel)

		parent := firstParent(commit)
		var previous ManifestEntry
		previousOK := false
		if parent != "" {
			parentCommit, err := readCommit(s.storeDir, parent)
			if err != nil {
				return nil, err
			}
			previous, previousOK = s.entryAt(parentCommit, rel)
		}

		if currentOK != previousOK || (currentOK && previousOK && current.Hash != previous.Hash) {
			info := RevisionInfo{
				Hash:      commit.Hash,
				Message:   commit.Message,
				Author:    commit.Author,
				CreatedAt: commit.CreatedAt,
				Origin:    commit.Origin,
				Binary:    current.Binary || previous.Binary,
			}
			switch {
			case !previousOK:
				info.Status = StatusAdded
			case !currentOK:
				info.Status = StatusDeleted
			default:
				info.Status = StatusModified
			}
			if currentOK {
				info.Size = current.Size
			} else {
				info.Size = previous.Size
			}
			info.Additions, info.Deletions = s.lineDelta(previous, previousOK, current, currentOK)
			revisions = append(revisions, info)
		}
		hash = parent
	}
	return revisions, nil
}

// entryAt returns a path's manifest entry inside a commit.
func (s *Store) entryAt(commit Commit, rel string) (ManifestEntry, bool) {
	manifest, err := readManifest(s.storeDir, commit.Manifest)
	if err != nil {
		return ManifestEntry{}, false
	}
	entry, ok := manifest.Entries[rel]
	return entry, ok
}

// lineDelta counts added and removed lines between two blob versions.
func (s *Store) lineDelta(oldEntry ManifestEntry, oldOK bool, newEntry ManifestEntry, newOK bool) (int, int) {
	if (oldOK && oldEntry.Binary) || (newOK && newEntry.Binary) {
		return 0, 0
	}
	var oldContent, newContent []byte
	if oldOK {
		if data, err := readBlob(s.storeDir, oldEntry.Hash); err == nil {
			oldContent = data
		}
	}
	if newOK {
		if data, err := readBlob(s.storeDir, newEntry.Hash); err == nil {
			newContent = data
		}
	}
	ops, _ := diffLines(splitLines(string(oldContent)), splitLines(string(newContent)))
	return countChanges(ops)
}

// ---------------------------------------------------------------------------
// timelines and tags
// ---------------------------------------------------------------------------

// Branches lists timelines.
func (s *Store) Branches() ([]BranchItem, error) {
	if !s.Initialized() {
		return nil, nil
	}
	current, _, detached, err := s.Head()
	if err != nil {
		return nil, err
	}
	if detached {
		current = ""
	}

	entries, err := os.ReadDir(filepath.Join(s.storeDir, "refs", "heads"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	items := make([]BranchItem, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		hash, err := s.readRef(s.branchRefPath(name))
		if err != nil || hash == "" {
			continue
		}
		item := BranchItem{Name: name, Head: hash, Current: name == current}
		if commit, err := readCommit(s.storeDir, hash); err == nil {
			item.Subject = commit.Message
			item.Created = commit.CreatedAt
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Current != items[j].Current {
			return items[i].Current
		}
		return items[i].Name < items[j].Name
	})
	return items, nil
}

// CreateBranch starts a new timeline at a revision (HEAD by default).
func (s *Store) CreateBranch(name, from string) error {
	return s.withLock(func() error {
		if err := s.Init(); err != nil {
			return err
		}
		if err := validateBranchName(name); err != nil {
			return err
		}
		if existing, err := s.readRef(s.branchRefPath(name)); err != nil {
			return err
		} else if existing != "" {
			return fmt.Errorf("versioning: timeline %q already exists", name)
		}
		hash, err := s.resolveCommit(from)
		if err != nil {
			return err
		}
		return s.writeRef(s.branchRefPath(name), hash)
	})
}

// Checkout switches HEAD to a timeline and restores its content.
//
// A dirty working tree is snapshotted first, so switching is never destructive.
func (s *Store) Checkout(name string, mode string) (RestoreResult, error) {
	if err := validateBranchName(name); err != nil {
		return RestoreResult{}, err
	}
	var result RestoreResult
	err := s.withLock(func() error {
		if err := s.Init(); err != nil {
			return err
		}
		target, err := s.readRef(s.branchRefPath(name))
		if err != nil {
			return err
		}
		if target == "" {
			return fmt.Errorf("%w: timeline %q", ErrBadRevision, name)
		}
		if err := s.SetHeadBranch(name); err != nil {
			return err
		}
		restored, err := s.restoreLocked(RestoreOptions{
			Revision: target,
			Mode:     normalizeRestoreMode(mode),
			Message:  "switch to timeline " + name,
		})
		if err != nil {
			return err
		}
		result = restored
		return nil
	})
	return result, err
}

// Tags lists bookmarks.
func (s *Store) Tags() ([]TagItem, error) {
	if !s.Initialized() {
		return nil, nil
	}
	entries, err := os.ReadDir(filepath.Join(s.storeDir, "refs", "tags"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	items := make([]TagItem, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		hash, err := s.readRef(s.tagRefPath(entry.Name()))
		if err != nil || hash == "" {
			continue
		}
		item := TagItem{Name: entry.Name(), Hash: hash}
		if info, err := entry.Info(); err == nil {
			item.CreatedAt = info.ModTime().UTC()
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

// CreateTag bookmarks a revision.
func (s *Store) CreateTag(name, rev string) error {
	return s.withLock(func() error {
		if err := s.Init(); err != nil {
			return err
		}
		if err := validateBranchName(name); err != nil {
			return err
		}
		hash, err := s.resolveCommit(rev)
		if err != nil {
			return err
		}
		return s.writeRef(s.tagRefPath(name), hash)
	})
}

// DeleteTag removes a bookmark.
func (s *Store) DeleteTag(name string) error {
	return s.withLock(func() error {
		if err := validateBranchName(name); err != nil {
			return err
		}
		path := s.tagRefPath(name)
		_, ok, err := readFileIfExists(path)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("versioning: tag %q not found", name)
		}
		return os.Remove(path)
	})
}

// ---------------------------------------------------------------------------
// restore
// ---------------------------------------------------------------------------

// Restore rewinds the working tree to a revision.
//
// The current state is snapshotted first (unless disabled), so a restore can
// always be undone. Applying is per-file atomic and rolls back on failure.
func (s *Store) Restore(opts RestoreOptions) (RestoreResult, error) {
	var result RestoreResult
	err := s.withLock(func() error {
		if err := s.Init(); err != nil {
			return err
		}
		restored, err := s.restoreLocked(opts)
		if err != nil {
			return err
		}
		result = restored
		return nil
	})
	return result, err
}

// normalizeRestoreMode resolves the requested restore mode.
//
// An unspecified mode means revert (D3): revert records the rollback as a new
// commit, so history only ever moves forward and the replaced state stays
// reachable from the log. Overwrite has to be asked for by name.
func normalizeRestoreMode(mode string) string {
	if strings.TrimSpace(mode) == RestoreModeOverwrite {
		return RestoreModeOverwrite
	}
	return RestoreModeRevert
}

// restoreLocked implements Restore; the caller must hold the store lock.
func (s *Store) restoreLocked(opts RestoreOptions) (RestoreResult, error) {
	mode := normalizeRestoreMode(opts.Mode)
	commit, err := s.readCommitAt(opts.Revision)
	if err != nil {
		return RestoreResult{}, err
	}
	targetManifest, err := readManifest(s.storeDir, commit.Manifest)
	if err != nil {
		return RestoreResult{}, err
	}

	result := RestoreResult{Revision: commit.Hash, Mode: mode, Message: commit.Message}
	branch, _, _, err := s.Head()
	if err != nil {
		return RestoreResult{}, err
	}
	result.Branch = branch

	// Scan the working tree before selecting paths: a whole-tree restore must
	// also delete files that were created after the target revision, so the
	// candidate set is the union of the current and target manifests.
	cfg, err := s.LoadConfig()
	if err != nil {
		return RestoreResult{}, err
	}
	currentManifest, _, _, err := s.scanWorkingTree(false, cfg)
	if err != nil {
		return RestoreResult{}, err
	}

	wanted, err := selectRestorePaths(targetManifest, currentManifest, opts.Paths)
	if err != nil {
		return RestoreResult{}, err
	}

	// Phase 1: verify every source object exists before touching the tree, so
	// a missing or corrupt object cannot leave a half-applied restore.
	for _, path := range wanted {
		entry, ok := targetManifest.Entries[path]
		if !ok {
			continue
		}
		if !hasObject(s.storeDir, entry.Hash) {
			return RestoreResult{}, fmt.Errorf("%w: missing blob %s for %s", errCorruptObject, entry.Hash, path)
		}
	}

	// Phase 2: secure the current state so the restore itself can be undone.
	if !opts.SkipSafety {
		safety, err := s.snapshotLocked(SnapshotOptions{
			Message: "before restore to " + shortHash(commit.Hash),
			Origin:  &Origin{Kind: OriginSafety, Detail: commit.Hash},
		})
		if err != nil {
			return RestoreResult{}, err
		}
		result.SafetyCommit = safety.Commit.Hash
	}

	// Phase 3: apply. Each file is written atomically; on failure the files
	// already changed are rolled back from the current manifest.
	type applied struct {
		path     string
		existed  bool
		prevHash string
	}
	var done []applied
	rollback := func() {
		for i := len(done) - 1; i >= 0; i-- {
			item := done[i]
			full, err := resolveInsideRoot(s.rootPath, item.path)
			if err != nil {
				continue
			}
			if !item.existed {
				_ = os.Remove(full)
				continue
			}
			if data, err := readBlob(s.storeDir, item.prevHash); err == nil {
				_ = atomicWriteFile(full, data, 0o644)
			}
		}
	}

	for _, path := range wanted {
		previous, existed := currentManifest.Entries[path]
		entry, present := targetManifest.Entries[path]

		full, err := resolveInsideRoot(s.rootPath, path)
		if err != nil {
			rollback()
			return RestoreResult{}, err
		}

		if !present {
			if existed {
				if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
					rollback()
					return RestoreResult{}, err
				}
				done = append(done, applied{path: path, existed: true, prevHash: previous.Hash})
				result.Deleted = append(result.Deleted, path)
			}
			continue
		}
		if existed && previous.Hash == entry.Hash {
			continue
		}

		data, err := readBlob(s.storeDir, entry.Hash)
		if err != nil {
			rollback()
			return RestoreResult{}, err
		}
		if err := atomicWriteFile(full, data, os.FileMode(entry.Mode)); err != nil {
			rollback()
			return RestoreResult{}, err
		}
		done = append(done, applied{path: path, existed: existed, prevHash: previous.Hash})
		result.Restored = append(result.Restored, path)
	}
	sort.Strings(result.Restored)
	sort.Strings(result.Deleted)

	if err := s.rebuildStatCache(true, cfg); err != nil {
		return RestoreResult{}, err
	}

	// Phase 4: revert mode records the rollback as a forward-only snapshot so
	// the timeline keeps moving in one direction.
	//
	// The commit has to describe the tree this restore actually produced, not the
	// target tree. For a whole-tree restore the two are the same, but a scoped
	// revert leaves the out-of-scope paths at their current contents, so
	// committing the target manifest wholesale would claim changes that were
	// never applied and make the next Status report the tree as clean when it is
	// not.
	if mode == RestoreModeRevert {
		applied := mergeRestoredManifest(currentManifest, targetManifest, wanted)
		hash, err := s.commitManifestLocked(applied, "", opts.Message, commit.Hash)
		if err != nil {
			return RestoreResult{}, err
		}
		result.Revision = hash
	}
	return result, nil
}

// mergeRestoredManifest returns the manifest describing the tree after a restore
// touched exactly wanted: the current manifest with those paths taken from the
// target, and dropped when the target does not have them.
func mergeRestoredManifest(current, target Manifest, wanted []string) Manifest {
	entries := make(map[string]ManifestEntry, len(current.Entries))
	for path, entry := range current.Entries {
		entries[path] = entry
	}
	for _, path := range wanted {
		if entry, ok := target.Entries[path]; ok {
			entries[path] = entry
			continue
		}
		delete(entries, path)
	}
	return Manifest{Entries: entries}
}

// selectRestorePaths decides which manifest paths a restore touches.
//
// The candidate set is always the union of the target and current manifests:
// restoring has to delete files that were added after the target revision, and
// a file absent from both is not worth touching.
//
// Scoping is deliberately independent of the restore mode. It used to be skipped
// for revert, which meant a scoped revert silently rewrote the whole tree and --
// because the traversal check lives in this loop -- also skipped path validation
// entirely. Revert now applies exactly the same selection and the same checks; it
// differs only in whether the result is committed.
func selectRestorePaths(target, current Manifest, paths []string) ([]string, error) {
	union := make(map[string]struct{}, len(target.Entries)+len(current.Entries))
	for path := range target.Entries {
		union[path] = struct{}{}
	}
	for path := range current.Entries {
		union[path] = struct{}{}
	}

	if len(paths) == 0 {
		return sortedKeys(union), nil
	}

	inScope := func(path, scope string) bool {
		return path == scope || strings.HasPrefix(path, scope+"/")
	}
	set := map[string]struct{}{}
	for _, raw := range paths {
		rel, err := normalizeRelPath(raw)
		if err != nil {
			return nil, err
		}
		if rel == "" {
			continue
		}
		// Always include the requested path itself so that a file which exists
		// now but not in the target is deleted.
		set[rel] = struct{}{}
		for path := range union {
			if inScope(path, rel) {
				set[path] = struct{}{}
			}
		}
	}
	return sortedKeys(set), nil
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// commitManifestLocked records a commit with an explicit manifest.
func (s *Store) commitManifestLocked(manifest Manifest, manifestHash, message, revertedFrom string) (string, error) {
	branch, parent, _, err := s.Head()
	if err != nil {
		return "", err
	}
	if branch == "" {
		branch = DefaultBranch
		if err := s.SetHeadBranch(branch); err != nil {
			return "", err
		}
	}
	if manifestHash == "" {
		if manifestHash, err = writeManifest(s.storeDir, manifest); err != nil {
			return "", err
		}
	}

	stats := CommitStats{Files: len(manifest.Entries)}
	for _, entry := range manifest.Entries {
		stats.Bytes += entry.Size
	}
	if parent != "" {
		if parentCommit, err := readCommit(s.storeDir, parent); err == nil {
			if parentManifest, err := readManifest(s.storeDir, parentCommit.Manifest); err == nil {
				for _, item := range diffManifests(parentManifest, manifest) {
					switch item.Status {
					case StatusAdded:
						stats.Added++
					case StatusModified:
						stats.Modified++
					case StatusDeleted:
						stats.Deleted++
					}
				}
			}
		}
	} else {
		stats.Added = stats.Files
	}

	if strings.TrimSpace(message) == "" {
		message = "revert to " + shortHash(revertedFrom)
	}
	doc := commitDoc{
		Parents:   nil,
		Manifest:  manifestHash,
		Message:   message,
		CreatedAt: time.Now().UTC(),
		Branch:    branch,
		Origin:    &Origin{Kind: OriginRevert, Detail: revertedFrom},
		Stats:     stats,
	}
	if parent != "" {
		doc.Parents = []string{parent}
	}
	hash, err := writeCommit(s.storeDir, doc)
	if err != nil {
		return "", err
	}
	if err := s.writeRef(s.branchRefPath(branch), hash); err != nil {
		return "", err
	}
	if err := s.touchConfig(); err != nil {
		return "", err
	}
	return hash, nil
}

// snapshotLocked implements Snapshot; the caller must hold the store lock.
func (s *Store) snapshotLocked(opts SnapshotOptions) (SnapshotResult, error) {
	if err := s.Init(); err != nil {
		return SnapshotResult{}, err
	}
	cfg, err := s.LoadConfig()
	if err != nil {
		return SnapshotResult{}, err
	}
	manifest, stats, cache, err := s.scanWorkingTree(true, cfg)
	if err != nil {
		return SnapshotResult{}, err
	}
	manifestHash, err := writeManifest(s.storeDir, manifest)
	if err != nil {
		return SnapshotResult{}, err
	}

	var branch, parent, refPath string
	if opts.Checkpoint {
		branch = CheckpointsDir
		refPath = s.checkpointRefPath(time.Now().UTC().Format(CheckpointStampLayout))
		// Compare against the previous checkpoint so an unchanged tree does not
		// produce another one, but deliberately do not record it as a parent.
		// With no checkpoint yet the user's timeline is the reference, so the
		// very first checkpoint is likewise skipped when nothing changed.
		parent = s.newestCheckpoint()
		if parent == "" {
			if _, head, _, headErr := s.Head(); headErr == nil {
				parent = head
			}
		}
	} else {
		var detached bool
		branch, parent, detached, err = s.Head()
		if err != nil {
			return SnapshotResult{}, err
		}
		if detached || branch == "" {
			branch = DefaultBranch
			if err := s.SetHeadBranch(branch); err != nil {
				return SnapshotResult{}, err
			}
		}
		refPath = s.branchRefPath(branch)
	}

	if parent != "" {
		parentCommit, err := readCommit(s.storeDir, parent)
		if err != nil {
			return SnapshotResult{}, err
		}
		if parentCommit.Manifest == manifestHash && !opts.Force {
			if err := s.saveStatCache(cache); err != nil {
				return SnapshotResult{}, err
			}
			parentCommit.Tags = s.tagsFor(parentCommit.Hash)
			return SnapshotResult{Commit: parentCommit, Created: false, Reason: "no changes"}, nil
		}
		if parentCommit.Manifest != "" {
			var added, modified, deleted int
			if parentManifest, err := readManifest(s.storeDir, parentCommit.Manifest); err == nil {
				for _, item := range diffManifests(parentManifest, manifest) {
					switch item.Status {
					case StatusAdded:
						added++
					case StatusModified:
						modified++
					case StatusDeleted:
						deleted++
					}
				}
			}
			stats.Added, stats.Modified, stats.Deleted = added, modified, deleted
		}
	} else {
		stats.Added = stats.Files
	}

	origin := opts.Origin
	if origin == nil && opts.Checkpoint {
		// Without this a checkpoint would carry no origin and the UI would fall
		// back to labelling it a manual snapshot.
		origin = &Origin{Kind: OriginAuto}
	}
	doc := commitDoc{
		Manifest:  manifestHash,
		Message:   snapshotMessage(opts.Message),
		Author:    opts.Author,
		CreatedAt: time.Now().UTC(),
		Branch:    branch,
		Origin:    origin,
		Stats:     stats,
	}
	if parent != "" && !opts.Checkpoint {
		doc.Parents = []string{parent}
	}
	hash, err := writeCommit(s.storeDir, doc)
	if err != nil {
		return SnapshotResult{}, err
	}
	if err := s.writeRef(refPath, hash); err != nil {
		return SnapshotResult{}, err
	}
	if opts.Tag != "" {
		if err := validateBranchName(opts.Tag); err != nil {
			return SnapshotResult{}, err
		}
		if err := s.writeRef(s.tagRefPath(opts.Tag), hash); err != nil {
			return SnapshotResult{}, err
		}
	}
	if err := s.saveStatCache(cache); err != nil {
		return SnapshotResult{}, err
	}
	if err := s.touchConfig(); err != nil {
		return SnapshotResult{}, err
	}

	commit, err := readCommit(s.storeDir, hash)
	if err != nil {
		return SnapshotResult{}, err
	}
	commit.Tags = s.tagsFor(hash)
	return SnapshotResult{Commit: commit, Created: true}, nil
}

// rebuildStatCache refreshes the stat cache after an external tree mutation.
func (s *Store) rebuildStatCache(writeBlobs bool, cfg storeConfig) error {
	_, _, cache, err := s.scanWorkingTree(writeBlobs, cfg)
	if err != nil {
		return err
	}
	return s.saveStatCache(cache)
}

// ---------------------------------------------------------------------------
// garbage collection
// ---------------------------------------------------------------------------

// GC removes objects that are unreachable from any ref and older than the
// grace period. Reachable history is never touched.
func (s *Store) GC(opts GCOptions) (GCResult, error) {
	var result GCResult
	err := s.withLock(func() error {
		if !s.Initialized() {
			return ErrNotInitialized
		}
		grace := opts.GracePeriod
		if grace <= 0 {
			grace = time.Hour
		}
		cutoff := time.Now().Add(-grace)
		if opts.Immediate {
			cutoff = time.Now()
		}
		reachable := map[string]bool{}
		for _, ref := range s.allRefHashes() {
			if err := s.walkParents(ref, func(commit Commit) error {
				reachable[commit.Hash] = true
				reachable[commit.Manifest] = true
				manifest, err := readManifest(s.storeDir, commit.Manifest)
				if err != nil {
					return err
				}
				for _, entry := range manifest.Entries {
					reachable[entry.Hash] = true
				}
				return nil
			}, map[string]bool{}); err != nil {
				return err
			}
		}

		objectsRoot := filepath.Join(s.storeDir, "objects")
		shards, err := os.ReadDir(objectsRoot)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, shard := range shards {
			if !shard.IsDir() {
				continue
			}
			shardDir := filepath.Join(objectsRoot, shard.Name())
			files, err := os.ReadDir(shardDir)
			if err != nil {
				continue
			}
			for _, file := range files {
				if file.IsDir() {
					continue
				}
				hash := shard.Name() + file.Name()
				if reachable[hash] {
					result.Kept++
					continue
				}
				info, err := file.Info()
				if err != nil {
					continue
				}
				if info.ModTime().After(cutoff) {
					// Too fresh: it may belong to an in-flight operation.
					result.Kept++
					continue
				}
				size := info.Size()
				if err := os.Remove(filepath.Join(shardDir, file.Name())); err != nil {
					continue
				}
				result.Removed++
				result.BytesFreed += size
			}
		}
		return nil
	})
	return result, err
}

// Verify checks that every object reachable from the refs is readable and
// that its content address matches. It is the store's integrity self-test.
func (s *Store) Verify() error {
	if !s.Initialized() {
		return ErrNotInitialized
	}
	for _, ref := range s.allRefHashes() {
		if err := s.walkParents(ref, func(commit Commit) error {
			if _, _, err := readObject(s.storeDir, commit.Hash); err != nil {
				return err
			}
			manifest, err := readManifest(s.storeDir, commit.Manifest)
			if err != nil {
				return err
			}
			for path, entry := range manifest.Entries {
				if _, err := readBlob(s.storeDir, entry.Hash); err != nil {
					return fmt.Errorf("%w: %s: %v", errCorruptObject, path, err)
				}
			}
			return nil
		}, map[string]bool{}); err != nil {
			return err
		}
	}
	return nil
}
