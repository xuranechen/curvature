// Package versioning implements a self-contained, git-independent local
// version control store. It powers the "project time machine": every snapshot
// is a content-addressed manifest of the project tree, history is a DAG of
// commits, and any point in time can be browsed, diffed and restored.
//
// The store lives inside the project metadata directory so that it never
// requires an external `git` binary and never touches the working tree unless
// an explicit restore is requested.
//
// Design notes:
//   - Objects are framed like git ("<kind> <size>\x00<payload>") and addressed
//     by the sha256 of the framed bytes, then zlib-compressed at rest.
//   - A commit's own hash is its object address, and tags live under refs/tags,
//     so neither is part of the persisted commit payload (see commitDoc).
//   - Manifests are flat path->entry maps rather than hierarchical trees. This
//     trades directory-level dedup (a negligible share) for a much simpler
//     implementation and a direct path-level diff.
package versioning

import "time"

// Object kinds stored in the content-addressed object database.
const (
	ObjectKindBlob   = "blob"
	ObjectKindTree   = "tree"
	ObjectKindCommit = "commit"
)

// DefaultBranch is used when a store is initialised.
const DefaultBranch = "main"

// HeadRev is the pseudo-revision that resolves to the current snapshot, and the
// default revision for read-only operations.
const HeadRev = "HEAD"

// StoreFormatVersion is bumped when the on-disk layout changes incompatibly.
const StoreFormatVersion = 1

// Status codes used by status/diff results.
const (
	StatusAdded    = "added"
	StatusModified = "modified"
	StatusDeleted  = "deleted"
)

// Origin kinds recorded on a snapshot.
const (
	OriginManual  = "manual"
	OriginAuto    = "auto"
	OriginSession = "session"
	OriginTask    = "task"
	OriginRevert  = "revert"
	OriginRestore = "restore"
	OriginSafety  = "safety"
)

// Restore modes accepted by Restore.
const (
	// RestoreModeOverwrite rewrites the working tree in place and leaves
	// history untouched (the tree becomes dirty relative to HEAD).
	RestoreModeOverwrite = "overwrite"
	// RestoreModeRevert records a brand-new snapshot whose content equals the
	// target revision, so history only ever moves forward.
	RestoreModeRevert = "revert"
)

// ManifestEntry is a single tracked file inside a snapshot manifest.
//
// The modification time is deliberately absent: it lives in the stat cache
// instead, so merely touching a file cannot change a manifest's content
// address or show up as a modification.
type ManifestEntry struct {
	// Hash is the content address of the file payload.
	Hash string `json:"hash"`
	// Size is the file size in bytes.
	Size int64 `json:"size"`
	// Mode is the unix permission bits (portable subset).
	Mode uint32 `json:"mode,omitempty"`
	// Binary marks payloads that are not text (NUL byte heuristic).
	Binary bool `json:"binary,omitempty"`
}

// Manifest is the flattened view of a project tree at one point in time.
type Manifest struct {
	// Entries maps a slash-separated project-relative path to its entry.
	Entries map[string]ManifestEntry `json:"entries"`
}

// Origin records what produced a snapshot, so history is self-explanatory.
type Origin struct {
	// Kind is one of the Origin* constants.
	Kind string `json:"kind,omitempty"`
	// Agent is the producing agent name, e.g. "claude".
	Agent string `json:"agent,omitempty"`
	// SessionID links the snapshot to an agent session.
	SessionID string `json:"session_id,omitempty"`
	// TaskID links the snapshot to a kanban task.
	TaskID string `json:"task_id,omitempty"`
	// Label is a short human-facing marker such as "before rm -rf".
	Label string `json:"label,omitempty"`
	// Detail carries extra free-form context.
	Detail string `json:"detail,omitempty"`
}

// CommitStats summarises how a snapshot differs from its first parent.
type CommitStats struct {
	Files    int   `json:"files"`
	Bytes    int64 `json:"bytes"`
	Added    int   `json:"added"`
	Modified int   `json:"modified"`
	Deleted  int   `json:"deleted"`
	Skipped  int   `json:"skipped"`
}

// Commit is an immutable point in time of the project.
//
// Hash and Tags are derived: Hash is the object address of the commit and Tags
// are resolved from refs/tags at load time. Neither is persisted in the commit
// payload, so neither can perturb the content address.
type Commit struct {
	Hash      string      `json:"hash"`
	Parents   []string    `json:"parents,omitempty"`
	Manifest  string      `json:"manifest"`
	Message   string      `json:"message"`
	Author    string      `json:"author,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
	Branch    string      `json:"branch,omitempty"`
	Origin    *Origin     `json:"origin,omitempty"`
	Tags      []string    `json:"tags,omitempty"`
	Stats     CommitStats `json:"stats"`
}

// commitDoc is the persisted commit payload.
//
// It deliberately excludes Hash (the object address) and Tags (stored under
// refs/tags) so that tagging a commit never rewrites it.
type commitDoc struct {
	Parents   []string    `json:"parents,omitempty"`
	Manifest  string      `json:"manifest"`
	Message   string      `json:"message"`
	Author    string      `json:"author,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
	Branch    string      `json:"branch,omitempty"`
	Origin    *Origin     `json:"origin,omitempty"`
	Stats     CommitStats `json:"stats"`
}

// FileStatus is one changed path in a working-tree comparison.
type FileStatus struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Size   int64  `json:"size,omitempty"`
	Binary bool   `json:"binary,omitempty"`
}

// StatusResult describes the working tree relative to a revision.
type StatusResult struct {
	Initialized bool         `json:"initialized"`
	Branch      string       `json:"branch,omitempty"`
	Head        string       `json:"head,omitempty"`
	Clean       bool         `json:"clean"`
	Dirty       int          `json:"dirty"`
	Total       int          `json:"total"`
	Truncated   bool         `json:"truncated,omitempty"`
	Items       []FileStatus `json:"items"`
}

// ChangeItem is one path-level difference between two revisions.
type ChangeItem struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	OldHash   string `json:"old_hash,omitempty"`
	NewHash   string `json:"new_hash,omitempty"`
	OldSize   int64  `json:"old_size,omitempty"`
	NewSize   int64  `json:"new_size,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
}

// DiffResult carries a git-compatible unified diff for a single path.
//
// Content is deliberately emitted in git's format ("diff --git", "--- a/…",
// "+++ b/…", "@@ -l,s +l,s @@") so the existing web diff parser
// (web/src/components/gitDiffModel.ts) can render it without changes.
type DiffResult struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Binary    bool   `json:"binary,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	OldSize   int64  `json:"old_size,omitempty"`
	NewSize   int64  `json:"new_size,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Content   string `json:"content"`
	Message   string `json:"message,omitempty"`
}

// RevisionInfo describes one revision of a path in the time machine timeline.
type RevisionInfo struct {
	Hash      string    `json:"hash"`
	Message   string    `json:"message"`
	Author    string    `json:"author,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Status    string    `json:"status"`
	Additions int       `json:"additions"`
	Deletions int       `json:"deletions"`
	Size      int64     `json:"size,omitempty"`
	Binary    bool      `json:"binary,omitempty"`
	Origin    *Origin   `json:"origin,omitempty"`
}

// BranchItem is one named timeline.
type BranchItem struct {
	Name    string    `json:"name"`
	Head    string    `json:"head"`
	Current bool      `json:"current"`
	Subject string    `json:"subject,omitempty"`
	Created time.Time `json:"created_at,omitzero"`
}

// TagItem is one named bookmark.
type TagItem struct {
	Name      string    `json:"name"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// RestoreResult reports the outcome of a working-tree restore.
type RestoreResult struct {
	Revision     string   `json:"revision"`
	Branch       string   `json:"branch,omitempty"`
	Mode         string   `json:"mode"`
	Restored     []string `json:"restored"`
	Deleted      []string `json:"deleted"`
	Skipped      []string `json:"skipped,omitempty"`
	SafetyCommit string   `json:"safety_commit,omitempty"`
	Message      string   `json:"message,omitempty"`
}

// GCResult reports reclaimed storage.
type GCResult struct {
	Removed    int   `json:"removed"`
	Kept       int   `json:"kept"`
	BytesFreed int64 `json:"bytes_freed"`
}

// RepoInfo summarises a store for UI consumption.
type RepoInfo struct {
	Initialized bool      `json:"initialized"`
	RootPath    string    `json:"root_path"`
	StorePath   string    `json:"store_path"`
	Branch      string    `json:"branch,omitempty"`
	Head        string    `json:"head,omitempty"`
	Commits     int       `json:"commits"`
	Branches    int       `json:"branches"`
	Tags        int       `json:"tags"`
	Tracked     int       `json:"tracked"`
	Objects     int       `json:"objects"`
	StoreBytes  int64     `json:"store_bytes"`
	CreatedAt   time.Time `json:"created_at,omitzero"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
}

// storeConfig is persisted as config.json inside the store.
type storeConfig struct {
	Format    int       `json:"format"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// MaxFileBytes caps a single tracked file. Larger files are skipped and
	// counted in CommitStats.Skipped.
	MaxFileBytes int64 `json:"max_file_bytes,omitempty"`
	// MaxFiles caps the number of tracked files per snapshot.
	MaxFiles int `json:"max_files,omitempty"`
	// ExtraIgnore holds additional root-relative ignore patterns.
	ExtraIgnore []string `json:"extra_ignore,omitempty"`
}

// statCacheEntry remembers a file's identity between scans so unchanged files
// do not have to be re-read and re-hashed.
type statCacheEntry struct {
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime"`
	Hash   string `json:"hash"`
	Binary bool   `json:"binary,omitempty"`
}

// statCache is persisted as cache/stat.json.
type statCache struct {
	Entries map[string]statCacheEntry `json:"entries"`
}
