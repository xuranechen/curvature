package versioning

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults applied when a store has no explicit limits configured.
const (
	defaultMaxFileBytes = 16 << 20 // 16 MiB per tracked file
	defaultMaxFiles     = 200000   // tracked files per snapshot
	staleLockAfter      = 10 * time.Minute
	lockWaitTimeout     = 15 * time.Second
	lockPollInterval    = 50 * time.Millisecond
)

// branchNamePattern restricts timeline names to something safe to use as a
// filename component.
var branchNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ErrBadBranchName is returned for timeline names that cannot be used safely.
var ErrBadBranchName = errors.New("versioning: invalid timeline name")

// Store is a single project's version store.
//
// Every mutating operation takes the in-process mutex and, for cross-process
// safety, an advisory lock file inside the store directory.
type Store struct {
	rootPath string
	storeDir string
	mu       sync.Mutex
}

// StoreDirFor returns the store directory inside a project metadata directory.
func StoreDirFor(metaDir string) string {
	return filepath.Join(metaDir, "versioning")
}

var (
	storeCacheMu sync.Mutex
	storeCache   = map[string]*Store{}
)

// Open returns a process-wide Store for a project root and metadata directory.
//
// Caching by store directory is what lets callers avoid threading a manager
// through the usecase layer: the mutex and stat cache are shared automatically.
func Open(rootPath, metaDir string) *Store {
	storeDir := filepath.Clean(StoreDirFor(metaDir))
	storeCacheMu.Lock()
	defer storeCacheMu.Unlock()
	if existing, ok := storeCache[storeDir]; ok {
		return existing
	}
	store := &Store{
		rootPath: filepath.Clean(rootPath),
		storeDir: storeDir,
	}
	storeCache[storeDir] = store
	return store
}

// Dir returns the absolute store directory.
func (s *Store) Dir() string { return s.storeDir }

// RootPath returns the absolute project root.
func (s *Store) RootPath() string { return s.rootPath }

// Initialized reports whether the store has been created on disk.
func (s *Store) Initialized() bool {
	_, err := os.Stat(filepath.Join(s.storeDir, "HEAD"))
	return err == nil
}

// Init creates the store layout. It is idempotent.
func (s *Store) Init() error {
	if s.Initialized() {
		return nil
	}
	for _, dir := range []string{
		filepath.Join(s.storeDir, "objects"),
		filepath.Join(s.storeDir, "refs", "heads"),
		filepath.Join(s.storeDir, "refs", "tags"),
		filepath.Join(s.storeDir, "cache"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	if _, ok, err := readFileIfExists(s.configPath()); err != nil {
		return err
	} else if !ok {
		cfg := storeConfig{
			Format:       StoreFormatVersion,
			CreatedAt:    now,
			UpdatedAt:    now,
			MaxFileBytes: defaultMaxFileBytes,
			MaxFiles:     defaultMaxFiles,
		}
		if err := s.saveConfig(cfg); err != nil {
			return err
		}
	}
	return atomicWriteString(filepath.Join(s.storeDir, "HEAD"), "ref: refs/heads/"+DefaultBranch+"\n")
}

// defaultConfig returns the config used when none is on disk.
func defaultConfig() storeConfig {
	now := time.Now().UTC()
	return storeConfig{
		Format:       StoreFormatVersion,
		CreatedAt:    now,
		UpdatedAt:    now,
		MaxFileBytes: defaultMaxFileBytes,
		MaxFiles:     defaultMaxFiles,
	}
}

func (s *Store) configPath() string {
	return filepath.Join(s.storeDir, "config.json")
}

func (s *Store) statCachePath() string {
	return filepath.Join(s.storeDir, "cache", "stat.json")
}

// LoadConfig reads the store configuration, falling back to defaults.
//
// A config file that no longer parses means the store was damaged outside this
// process (a mangled copy, a bad archive restore, a dying disk). That must not
// brick the feature: the damaged bytes are kept next to the original for
// forensics and the store continues from defaults, which this call writes back so
// the next open is clean. Individual objects still verify their own addresses, so
// real content damage is reported per read instead of as an unopenable store.
func (s *Store) LoadConfig() (storeConfig, error) {
	data, ok, err := readFileIfExists(s.configPath())
	if err != nil {
		return storeConfig{}, err
	}
	if !ok {
		return defaultConfig(), nil
	}
	cfg := defaultConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		backup := s.configPath() + ".corrupt"
		if backupErr := atomicWriteFile(backup, data, 0o644); backupErr != nil {
			log.Printf("[versioning] config.backup.error path=%s err=%v", backup, backupErr)
		}
		log.Printf("[versioning] config.corrupt path=%s backup=%s err=%v action=reset_to_defaults", s.configPath(), backup, err)
		reset := defaultConfig()
		if saveErr := s.saveConfig(reset); saveErr != nil {
			return storeConfig{}, saveErr
		}
		return reset, nil
	}
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = defaultMaxFileBytes
	}
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = defaultMaxFiles
	}
	return cfg, nil
}

func (s *Store) saveConfig(cfg storeConfig) error {
	cfg.Format = StoreFormatVersion
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(s.configPath(), append(data, '\n'), 0o644)
}

func (s *Store) loadStatCache() *statCache {
	cache := &statCache{Entries: map[string]statCacheEntry{}}
	data, ok, err := readFileIfExists(s.statCachePath())
	if err != nil || !ok {
		return cache
	}
	if err := json.Unmarshal(data, cache); err != nil || cache.Entries == nil {
		return &statCache{Entries: map[string]statCacheEntry{}}
	}
	return cache
}

func (s *Store) saveStatCache(cache *statCache) error {
	if cache == nil {
		return nil
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	return atomicWriteFile(s.statCachePath(), data, 0o644)
}

// withLock runs fn while holding both the in-process mutex and the store's
// advisory lock file.
func (s *Store) withLock(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	release, err := s.acquireFileLock()
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// acquireFileLock creates an exclusive lock file, stealing it when the previous
// holder appears to have died.
func (s *Store) acquireFileLock() (func(), error) {
	if err := os.MkdirAll(s.storeDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(s.storeDir, "lock")
	deadline := time.Now().Add(lockWaitTimeout)

	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = fmt.Fprintf(file, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
			_ = file.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > staleLockAfter {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("versioning: store is locked by another process")
		}
		time.Sleep(lockPollInterval)
	}
}

// ---------------------------------------------------------------------------
// refs
// ---------------------------------------------------------------------------

func (s *Store) headPath() string {
	return filepath.Join(s.storeDir, "HEAD")
}

func (s *Store) branchRefPath(name string) string {
	return filepath.Join(s.storeDir, "refs", "heads", name)
}

func (s *Store) tagRefPath(name string) string {
	return filepath.Join(s.storeDir, "refs", "tags", name)
}

// Head returns the current branch and its commit hash. An unborn branch yields
// an empty hash and no error.
func (s *Store) Head() (branch string, hash string, detached bool, err error) {
	raw, ok, err := readFileIfExists(s.headPath())
	if err != nil {
		return "", "", false, err
	}
	if !ok {
		return "", "", false, ErrNotInitialized
	}
	content := strings.TrimSpace(string(raw))
	if strings.HasPrefix(content, "ref: ") {
		branch = strings.TrimSpace(strings.TrimPrefix(content, "ref: "))
		branch = strings.TrimPrefix(branch, "refs/heads/")
		ref, found, err := readFileIfExists(s.branchRefPath(branch))
		if err != nil {
			return branch, "", false, err
		}
		if !found {
			return branch, "", false, nil
		}
		return branch, strings.TrimSpace(string(ref)), false, nil
	}
	return "", content, true, nil
}

func (s *Store) readRef(path string) (string, error) {
	data, ok, err := readFileIfExists(path)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	return strings.TrimSpace(string(data)), nil
}

func (s *Store) writeRef(path, hash string) error {
	return atomicWriteString(path, hash+"\n")
}

// SetHeadBranch points HEAD at a timeline.
func (s *Store) SetHeadBranch(name string) error {
	if err := validateBranchName(name); err != nil {
		return err
	}
	return atomicWriteString(s.headPath(), "ref: refs/heads/"+name+"\n")
}

// validateBranchName rejects names that are unsafe as path components.
func validateBranchName(name string) error {
	if !branchNamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrBadBranchName, name)
	}
	return nil
}

// resolveCommit turns a revision expression into a commit hash.
//
// Supported forms: "", "HEAD", a timeline name, a tag name, a full or
// abbreviated commit hash, and any of the above suffixed with "~N" to walk N
// first parents back.
func (s *Store) resolveCommit(rev string) (string, error) {
	rev = strings.TrimSpace(rev)
	if rev == "" {
		rev = "HEAD"
	}

	base, depth := splitTilde(rev)
	hash, err := s.resolveBase(base)
	if err != nil {
		return "", err
	}
	for i := 0; i < depth; i++ {
		commit, err := readCommit(s.storeDir, hash)
		if err != nil {
			return "", err
		}
		if len(commit.Parents) == 0 {
			return "", fmt.Errorf("%w: %s has no parent", ErrBadRevision, rev)
		}
		hash = commit.Parents[0]
	}
	return hash, nil
}

// splitTilde splits "rev~3" into ("rev", 3).
func splitTilde(rev string) (string, int) {
	idx := strings.LastIndex(rev, "~")
	if idx < 0 {
		return rev, 0
	}
	depth, err := strconv.Atoi(rev[idx+1:])
	if err != nil || depth < 0 {
		return rev, 0
	}
	return rev[:idx], depth
}

func (s *Store) resolveBase(base string) (string, error) {
	switch base {
	case "", "HEAD":
		_, hash, _, err := s.Head()
		if err != nil {
			return "", err
		}
		if hash == "" {
			return "", fmt.Errorf("%w: no snapshot yet", ErrBadRevision)
		}
		return hash, nil
	}

	if err := validateBranchName(base); err == nil {
		if hash, err := s.readRef(s.branchRefPath(base)); err != nil {
			return "", err
		} else if hash != "" {
			return hash, nil
		}
		if hash, err := s.readRef(s.tagRefPath(base)); err != nil {
			return "", err
		} else if hash != "" {
			return hash, nil
		}
	}

	if len(base) >= 6 && len(base) <= hashHexLen && isLowerHex(base) {
		if len(base) == hashHexLen {
			if hasObject(s.storeDir, base) {
				return base, nil
			}
		} else if hash, err := s.expandHashPrefix(base); err != nil {
			return "", err
		} else if hash != "" {
			return hash, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrBadRevision, base)
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// expandHashPrefix finds the unique object whose hash starts with prefix.
func (s *Store) expandHashPrefix(prefix string) (string, error) {
	dir := filepath.Join(s.storeDir, "objects", prefix[:2])
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	match := ""
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		full := prefix[:2] + entry.Name()
		if !strings.HasPrefix(full, prefix) {
			continue
		}
		if match != "" {
			return "", fmt.Errorf("%w: ambiguous prefix %q", ErrBadRevision, prefix)
		}
		match = full
	}
	return match, nil
}

// readCommitAt resolves a revision and loads the commit.
func (s *Store) readCommitAt(rev string) (Commit, error) {
	hash, err := s.resolveCommit(rev)
	if err != nil {
		return Commit{}, err
	}
	commit, err := readCommit(s.storeDir, hash)
	if err != nil {
		return Commit{}, err
	}
	commit.Tags = s.tagsFor(hash)
	return commit, nil
}

// manifestAt resolves a revision and loads its manifest.
func (s *Store) manifestAt(rev string) (Manifest, string, error) {
	commit, err := s.readCommitAt(rev)
	if err != nil {
		return Manifest{}, "", err
	}
	manifest, err := readManifest(s.storeDir, commit.Manifest)
	if err != nil {
		return Manifest{}, "", err
	}
	return manifest, commit.Hash, nil
}

// tagsFor returns the tag names pointing at a commit.
func (s *Store) tagsFor(hash string) []string {
	entries, err := os.ReadDir(filepath.Join(s.storeDir, "refs", "tags"))
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if ref, err := s.readRef(s.tagRefPath(entry.Name())); err == nil && ref == hash {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

// ---------------------------------------------------------------------------
// scanning
// ---------------------------------------------------------------------------

// scanWorkingTree walks the project, hashing tracked files into a manifest.
//
// When writeBlobs is true the payloads are stored as objects; when false only
// hashes are computed, which is what plain status checks need.
// scanCandidate is one file found by the walk, together with the content address
// resolved for it. Discovery and resolution are separate phases so the expensive
// half — reading and hashing — can run in parallel.
type scanCandidate struct {
	path   string
	rel    string
	size   int64
	mtime  int64
	mode   os.FileMode
	hash   string
	binary bool
	// resolved marks a candidate whose address came from the stat cache and so
	// needs no read at all.
	resolved bool
	err      error
}

// maxScanHashWorkers caps hashing parallelism so a machine with very many cores
// does not open an unbounded number of files at once.
const maxScanHashWorkers = 16

// scanHashWorkers picks how many files to hash at once.
func scanHashWorkers(pending int) int {
	if pending <= 1 {
		return 1
	}
	workers := runtime.NumCPU()
	if workers > pending {
		workers = pending
	}
	if workers > maxScanHashWorkers {
		workers = maxScanHashWorkers
	}
	if workers < 1 {
		workers = 1
	}
	return workers
}

func (s *Store) scanWorkingTree(writeBlobs bool, cfg storeConfig) (Manifest, CommitStats, *statCache, error) {
	matcher, err := buildIgnoreMatcher(s.rootPath, s.storeDir, cfg.ExtraIgnore)
	if err != nil {
		return Manifest{}, CommitStats{}, nil, err
	}
	previous := s.loadStatCache()
	var stats CommitStats

	// Phase 1: discover candidates and settle the ones the stat cache covers. The
	// walk stays serial because it owns the ignore matcher, the file cap and the
	// running statistics.
	candidates := make([]scanCandidate, 0, 256)
	walkErr := filepath.WalkDir(s.rootPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory should not abort the whole snapshot.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			stats.Skipped++
			return nil
		}
		rel := relativeInsideRoot(s.rootPath, path)
		if rel == "" {
			return nil // the root itself
		}
		isDir := entry.IsDir()
		if matcher.ignored(rel, isDir) {
			if isDir {
				return fs.SkipDir
			}
			return nil
		}
		if isDir {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			stats.Skipped++
			return nil
		}
		// Symlinks and special files are never followed or stored.
		if !info.Mode().IsRegular() {
			stats.Skipped++
			return nil
		}
		size := info.Size()
		if cfg.MaxFileBytes > 0 && size > cfg.MaxFileBytes {
			stats.Skipped++
			return nil
		}
		if cfg.MaxFiles > 0 && len(candidates) >= cfg.MaxFiles {
			stats.Skipped++
			return nil
		}

		candidate := scanCandidate{
			path:  path,
			rel:   rel,
			size:  size,
			mtime: info.ModTime().UnixNano(),
			mode:  info.Mode(),
		}
		if cached, ok := cachedHashHit(s.storeDir, rel, candidate.size, candidate.mtime, previous); ok {
			candidate.hash = cached.Hash
			candidate.binary = cached.Binary
			candidate.resolved = true
		}
		candidates = append(candidates, candidate)
		stats.Files++
		stats.Bytes += size
		return nil
	})
	if walkErr != nil {
		return Manifest{}, CommitStats{}, nil, walkErr
	}

	// Phase 2: hash whatever the cache could not answer, several files at once.
	if err := s.resolveCandidates(candidates, writeBlobs); err != nil {
		return Manifest{}, CommitStats{}, nil, err
	}

	// Phase 3: assemble serially, so the maps are written from a single goroutine.
	manifest := Manifest{Entries: make(map[string]ManifestEntry, len(candidates))}
	next := &statCache{Entries: make(map[string]statCacheEntry, len(candidates))}
	for i := range candidates {
		candidate := &candidates[i]
		manifest.Entries[candidate.rel] = ManifestEntry{
			Hash:   candidate.hash,
			Size:   candidate.size,
			Mode:   uint32(candidate.mode.Perm()),
			Binary: candidate.binary,
		}
		next.Entries[candidate.rel] = statCacheEntry{
			Size:   candidate.size,
			MTime:  candidate.mtime,
			Hash:   candidate.hash,
			Binary: candidate.binary,
		}
	}
	return manifest, stats, next, nil
}

// resolveCandidates fills in the content address of every candidate the stat
// cache did not already answer.
//
// Reads, framing and compression run in parallel; only the store step is
// serialized. Concurrent writes are NOT safe on their own: a writer checks
// whether the object exists and then renames onto it, so two workers holding the
// same content both miss the check and both rename onto one path — which on
// Windows fails with a sharing violation.
func (s *Store) resolveCandidates(candidates []scanCandidate, writeBlobs bool) error {
	pending := 0
	for i := range candidates {
		if !candidates[i].resolved {
			pending++
		}
	}
	if pending > 0 {
		indexes := make(chan int)
		var writeMu sync.Mutex
		var workers sync.WaitGroup
		for w := 0; w < scanHashWorkers(pending); w++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for i := range indexes {
					candidate := &candidates[i]
					data, err := readFileForHash(candidate.path)
					if err != nil {
						candidate.err = err
						continue
					}
					candidate.binary = IsBinaryPayload(data)
					if !writeBlobs {
						candidate.hash = hashFramed(frameObject(ObjectKindBlob, data))
						continue
					}
					hash, encoded, err := encodeObject(ObjectKindBlob, data)
					if err != nil {
						candidate.err = err
						continue
					}
					writeMu.Lock()
					err = storeEncoded(s.storeDir, hash, encoded)
					writeMu.Unlock()
					candidate.hash, candidate.err = hash, err
				}
			}()
		}
		for i := range candidates {
			if !candidates[i].resolved {
				indexes <- i
			}
		}
		close(indexes)
		workers.Wait()
	}

	// Reported in walk order rather than completion order, so the error a caller
	// sees does not depend on how the work happened to be scheduled.
	for i := range candidates {
		if candidates[i].err != nil {
			return candidates[i].err
		}
	}
	return nil
}

// hashFile resolves a file's content address, reusing the stat cache when the
// size and mtime are unchanged and the object is still present.
// readFileForHash is the read used when a file's content must be hashed.
//
// It is a variable so tests can prove the stat cache short-circuits unchanged
// files instead of re-reading them, and because this read is the scan's hot spot.
var readFileForHash = os.ReadFile

// cachedHashHit reports whether the stat cache can be trusted for a file.
//
// Trusting a hash whose object has been collected would yield a manifest that
// points at nothing, so the object's presence is checked too.
func cachedHashHit(storeDir, rel string, size, mtime int64, previous *statCache) (statCacheEntry, bool) {
	if previous == nil {
		return statCacheEntry{}, false
	}
	cached, ok := previous.Entries[rel]
	if !ok || cached.Size != size || cached.MTime != mtime || !validHash(cached.Hash) {
		return statCacheEntry{}, false
	}
	if !hasObject(storeDir, cached.Hash) {
		return statCacheEntry{}, false
	}
	return cached, true
}

// diffManifests lists the paths that differ between two manifests.
func diffManifests(oldManifest, newManifest Manifest) []ChangeItem {
	items := make([]ChangeItem, 0)
	for path, entry := range newManifest.Entries {
		previous, existed := oldManifest.Entries[path]
		switch {
		case !existed:
			items = append(items, ChangeItem{
				Path: path, Status: StatusAdded,
				NewHash: entry.Hash, NewSize: entry.Size, Binary: entry.Binary,
			})
		case previous.Hash != entry.Hash:
			items = append(items, ChangeItem{
				Path: path, Status: StatusModified,
				OldHash: previous.Hash, NewHash: entry.Hash,
				OldSize: previous.Size, NewSize: entry.Size,
				Binary: previous.Binary || entry.Binary,
			})
		}
	}
	for path, entry := range oldManifest.Entries {
		if _, ok := newManifest.Entries[path]; !ok {
			items = append(items, ChangeItem{
				Path: path, Status: StatusDeleted,
				OldHash: entry.Hash, OldSize: entry.Size, Binary: entry.Binary,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	return items
}
