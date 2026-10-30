package fs

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	fileChangeBatchDelay = time.Second
	maxWatchDirs         = 1024
)

var ignoredWatchNames = map[string]struct{}{
	".DS_Store":     {},
	".cache":        {},
	".git":          {},
	".gradle":       {},
	".curvature":    {},
	".mypy_cache":   {},
	".next":         {},
	".nuxt":         {},
	".output":       {},
	".parcel-cache": {},
	".pytest_cache": {},
	".ruff_cache":   {},
	".svelte-kit":   {},
	".turbo":        {},
	".venv":         {},
	".vercel":       {},
	".vite":         {},
	"__pycache__":   {},
	"build":         {},
	"coverage":      {},
	"dist":          {},
	"node_modules":  {},
	"target":        {},
	"tmp":           {},
	"vendor":        {},
	"venv":          {},
	// Scratch and toolchain trees. A project can carry tens of thousands of
	// directories under these names (a checked-out Flutter SDK, a package cache),
	// and every one of them would otherwise be a candidate watch 闂?against a cap
	// of maxWatchDirs, on a tree that is only ever rewritten by tooling.
	".dart_tool":  {},
	".npm-cache":  {},
	".pnpm-store": {},
	".publish":    {},
	".shots":      {},
	".tmp":        {},
	".toolchain":  {},
}

// SharedFileWatcher manages file watching for one root shared by multiple sessions.
type SharedFileWatcher struct {
	root         RootInfo
	watcher      *fsnotify.Watcher
	sessionStore SessionFileRecorder

	// addMu serializes the calls that talk to fsnotify. It is deliberately not
	// sw.mu: adding a watch is the one operation here that can block for a long
	// time (an unresponsive volume, a handle the OS will not hand out), and sw.mu
	// is on the path of every directory listing and every file read.
	//
	// watchedDirList mirrors watchedDirs in insertion order. Rescan walks it,
	// and keeping the order stable means the directories known first are
	// checked first.
	addMu             sync.Mutex
	mu                sync.RWMutex
	sessions          map[string]*sessionInfo
	watchedDirs       map[string]struct{}
	watchedDirList    []string
	pendingWrites     map[string]string
	pendingChanges    map[string]FileChangeEvent
	pendingChangeDirs map[string]struct{}
	fileChangeTimer   *time.Timer
	fileChangeVersion uint64
	onFileChange      func(FileChangeEvent)
	onFileChangeBatch func(FileChangeBatchEvent)
	onRelatedFile     func(RelatedFileEvent)
	worktreeResolver  WorktreeResolver

	// rescanMu serializes Rescan passes and owns scanSnapshot; both are only
	// touched from Rescan. lastRescan rate limits them.
	rescanMu     sync.Mutex
	scanSnapshot map[string]scanEntry
	lastRescan   time.Time

	done chan struct{}
}

type SessionFileRecorder interface {
	RecordOutputFile(ctx context.Context, key, path string) error
	RecordOutputFileAtHead(ctx context.Context, key, path, head string) error
	RecordOutputFileInRepo(ctx context.Context, key, rootID, repoKind, repoPath, repoName, path, head string) error
	RecordRelatedWorktree(ctx context.Context, key, rootID, path, branch, head string) (bool, error)
}

type sessionInfo struct {
	key string
}

type FileChangeEvent struct {
	RootID string `json:"root_id"`
	Path   string `json:"path"`
	Op     string `json:"op"`
	IsDir  bool   `json:"is_dir"`
}

type FileChangeBatchEvent struct {
	RootID string            `json:"root_id"`
	Paths  []string          `json:"paths"`
	Dirs   []string          `json:"dirs"`
	Events []FileChangeEvent `json:"events"`
}

type RelatedFileEvent struct {
	RootID          string                `json:"root_id"`
	SessionKey      string                `json:"session_key"`
	Path            string                `json:"path"`
	RelatedWorktree *RelatedWorktreeMatch `json:"related_worktree,omitempty"`
}

type RelatedWorktreeMatch struct {
	Path    string
	Branch  string
	Head    string
	Current bool
}

type relatedFileRecord struct {
	rootID   string
	repoKind string
	repoPath string
	repoName string
	path     string
	head     string
}

type WorktreeResolver func(ctx context.Context, root RootInfo, filePath string) (RelatedWorktreeMatch, bool)

func NewSharedFileWatcher(root RootInfo, sessions SessionFileRecorder, worktreeResolvers ...WorktreeResolver) (*SharedFileWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	var resolver WorktreeResolver
	if len(worktreeResolvers) > 0 {
		resolver = worktreeResolvers[0]
	}
	sw := &SharedFileWatcher{
		root:              root,
		watcher:           w,
		sessionStore:      sessions,
		worktreeResolver:  resolver,
		sessions:          make(map[string]*sessionInfo),
		watchedDirs:       make(map[string]struct{}),
		pendingWrites:     make(map[string]string),
		pendingChanges:    make(map[string]FileChangeEvent),
		pendingChangeDirs: make(map[string]struct{}),
		scanSnapshot:      make(map[string]scanEntry),
		done:              make(chan struct{}),
	}
	if err := sw.WatchDir("."); err != nil {
		if closeErr := w.Close(); closeErr != nil {
		}
		return nil, err
	}
	go sw.run()
	return sw, nil
}

func (sw *SharedFileWatcher) RegisterSession(sessionKey string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.sessions[sessionKey] = &sessionInfo{key: sessionKey}
}

func (sw *SharedFileWatcher) UnregisterSession(sessionKey string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	delete(sw.sessions, sessionKey)
	for path, key := range sw.pendingWrites {
		if key == sessionKey {
			delete(sw.pendingWrites, path)
		}
	}
}

func (sw *SharedFileWatcher) MarkSessionActive(_ string) {
}

func (sw *SharedFileWatcher) RecordPendingWrite(sessionKey, filePath string) {
	watchDir := ""
	sw.mu.Lock()
	if rel, err := sw.root.NormalizePath(filePath); err == nil {
		filePath = rel
	}
	filePath = filepath.ToSlash(filePath)
	sw.pendingWrites[filePath] = sessionKey
	watchDir = parentDir(filePath)
	sw.mu.Unlock()
	if watchDir != "" {
		sw.watchNearestExistingDir(watchDir)
	}
}

func (sw *SharedFileWatcher) RecordSessionFile(sessionKey, filePath string) {
	if sw.sessionStore == nil || sessionKey == "" || filePath == "" {
		return
	}
	sw.recordSessionWorktree(context.Background(), sessionKey, filePath)
	record, ok := sw.resolveRelatedFileRecord(context.Background(), filePath)
	if !ok {
		return
	}
	if err := sw.sessionStore.RecordOutputFileInRepo(
		context.Background(),
		sessionKey,
		record.rootID,
		record.repoKind,
		record.repoPath,
		record.repoName,
		record.path,
		record.head,
	); err != nil {
		return
	}
	if record.repoKind != "git" || sameCleanPath(record.repoPath, sw.root.RootPath) {
		sw.root.UpdateFileMeta(record.path, sessionKey, "agent")
	}
	sw.emitRelatedFile(RelatedFileEvent{
		RootID:     sw.root.ID,
		SessionKey: sessionKey,
		Path:       record.path,
	})
}

func (sw *SharedFileWatcher) resolveRelatedFileRecord(ctx context.Context, filePath string) (relatedFileRecord, bool) {
	absFilePath := absoluteRelatedFilePath(sw.root.RootPath, filePath)
	if sw.worktreeResolver != nil && filePath != "" {
		if match, ok := sw.worktreeResolver(ctx, sw.root, filePath); ok {
			relPath, ok := relativePathInside(match.Path, absFilePath)
			if ok && validRelatedPath(relPath) {
				repoPath := filepath.Clean(match.Path)
				return relatedFileRecord{
					rootID:   sw.root.ID,
					repoKind: "git",
					repoPath: repoPath,
					repoName: filepath.Base(repoPath),
					path:     relPath,
					head:     strings.TrimSpace(match.Head),
				}, true
			}
		}
	}

	relPath, err := sw.root.NormalizePath(filePath)
	if err != nil {
		return relatedFileRecord{}, false
	}
	relPath = filepath.ToSlash(relPath)
	if !validRelatedPath(relPath) {
		return relatedFileRecord{}, false
	}
	rootPath := filepath.Clean(sw.root.RootPath)
	return relatedFileRecord{
		rootID:   sw.root.ID,
		repoKind: "plain",
		repoPath: rootPath,
		repoName: sw.root.Name,
		path:     relPath,
	}, true
}

func absoluteRelatedFilePath(rootPath, filePath string) string {
	cleanPath := strings.TrimSpace(filePath)
	if cleanPath == "" {
		return ""
	}
	if filepath.IsAbs(cleanPath) {
		return filepath.Clean(cleanPath)
	}
	return filepath.Clean(filepath.Join(rootPath, cleanPath))
}

func validRelatedPath(path string) bool {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "." || path == ".." || path == "" {
		return false
	}
	return !(len(path) >= len(".curvature") && path[:len(".curvature")] == ".curvature")
}

func relativePathInside(rootPath, filePath string) (string, bool) {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	if rootPath == "" {
		return "", false
	}
	cleanPath := strings.TrimSpace(filePath)
	if cleanPath == "" {
		return "", false
	}
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(rootPath, cleanPath)
	}
	cleanPath = filepath.Clean(cleanPath)
	rel, err := filepath.Rel(rootPath, cleanPath)
	if err != nil || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func sameCleanPath(a, b string) bool {
	return filepath.Clean(strings.TrimSpace(a)) == filepath.Clean(strings.TrimSpace(b))
}

func (sw *SharedFileWatcher) recordSessionWorktree(ctx context.Context, sessionKey, filePath string) {
	if sw.sessionStore == nil || sw.worktreeResolver == nil || sessionKey == "" || filePath == "" {
		return
	}
	match, ok := sw.worktreeResolver(ctx, sw.root, filePath)
	if !ok {
		return
	}
	if added, err := sw.sessionStore.RecordRelatedWorktree(ctx, sessionKey, sw.root.ID, match.Path, match.Branch, match.Head); err == nil && added {
		sw.emitRelatedFile(RelatedFileEvent{
			RootID:          sw.root.ID,
			SessionKey:      sessionKey,
			RelatedWorktree: &match,
		})
	}
}

func (sw *SharedFileWatcher) SetOnFileChange(handler func(FileChangeEvent)) {
	sw.mu.Lock()
	sw.onFileChange = handler
	sw.mu.Unlock()
}

func (sw *SharedFileWatcher) SetOnFileChangeBatch(handler func(FileChangeBatchEvent)) {
	sw.mu.Lock()
	sw.onFileChangeBatch = handler
	sw.mu.Unlock()
}

func (sw *SharedFileWatcher) SetOnRelatedFile(handler func(RelatedFileEvent)) {
	sw.mu.Lock()
	sw.onRelatedFile = handler
	sw.mu.Unlock()
}

func (sw *SharedFileWatcher) SessionCount() int {
	sw.mu.RLock()
	defer sw.mu.RUnlock()
	return len(sw.sessions)
}

func (sw *SharedFileWatcher) Close() {
	sw.mu.Lock()
	select {
	case <-sw.done:
		sw.mu.Unlock()
		return
	default:
		close(sw.done)
	}
	sw.mu.Unlock()
	sw.flushFileChangeBatch(0)
	sw.watcher.Close()
}

func (sw *SharedFileWatcher) run() {
	for {
		select {
		case event, ok := <-sw.watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename|fsnotify.Remove) == 0 {
				continue
			}
			if sw.shouldIgnore(event.Name) {
				continue
			}
			rel, err := sw.root.NormalizePath(event.Name)
			if err != nil {
				continue
			}
			if event.Op&fsnotify.Remove != 0 {
				sw.emitFileChange(FileChangeEvent{
					RootID: sw.root.ID,
					Path:   rel,
					Op:     event.Op.String(),
					IsDir:  false,
				})
				continue
			}
			info, err := os.Stat(event.Name)
			if err != nil {
				// File might disappear quickly during rename/remove races.
				sw.emitFileChange(FileChangeEvent{
					RootID: sw.root.ID,
					Path:   rel,
					Op:     event.Op.String(),
					IsDir:  false,
				})
				continue
			}
			if info.IsDir() {
				sw.emitFileChange(FileChangeEvent{
					RootID: sw.root.ID,
					Path:   rel,
					Op:     event.Op.String(),
					IsDir:  true,
				})
				// Off the event loop: a stuck Add must not stop this loop from
				// draining fsnotify, which is the only way changes are reported.
				go func(dir string) {
					_ = sw.WatchDir(dir)
				}(rel)
				continue
			}
			sw.emitFileChange(FileChangeEvent{
				RootID: sw.root.ID,
				Path:   rel,
				Op:     event.Op.String(),
				IsDir:  false,
			})
			sessionKey := sw.resolveSessionKey(rel)
			if sessionKey == "" {
				continue
			}
			sw.RecordSessionFile(sessionKey, rel)
		case _, ok := <-sw.watcher.Errors:
			if !ok {
				return
			}
		case <-sw.done:
			return
		}
	}
}

func (sw *SharedFileWatcher) resolveSessionKey(relPath string) string {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sessionKey, ok := sw.pendingWrites[relPath]; ok {
		delete(sw.pendingWrites, relPath)
		return sessionKey
	}
	return ""
}

func (sw *SharedFileWatcher) emitFileChange(change FileChangeEvent) {
	sw.queueFileChangeBatch(change)
}

func (sw *SharedFileWatcher) queueFileChangeBatch(change FileChangeEvent) {
	change.Path = filepath.ToSlash(change.Path)
	if change.Path == "" {
		return
	}
	sw.mu.Lock()
	select {
	case <-sw.done:
		sw.mu.Unlock()
		return
	default:
	}
	if sw.pendingChanges == nil {
		sw.pendingChanges = make(map[string]FileChangeEvent)
	}
	if sw.pendingChangeDirs == nil {
		sw.pendingChangeDirs = make(map[string]struct{})
	}
	sw.pendingChanges[change.Path] = change
	sw.pendingChangeDirs[parentDir(change.Path)] = struct{}{}
	if change.IsDir || strings.Contains(change.Op, "REMOVE") || strings.Contains(change.Op, "RENAME") {
		sw.pendingChangeDirs[change.Path] = struct{}{}
	}
	sw.fileChangeVersion++
	version := sw.fileChangeVersion
	if sw.fileChangeTimer != nil {
		sw.fileChangeTimer.Stop()
	}
	sw.fileChangeTimer = time.AfterFunc(fileChangeBatchDelay, func() {
		sw.flushFileChangeBatch(version)
	})
	sw.mu.Unlock()
}

func (sw *SharedFileWatcher) flushFileChangeBatch(version uint64) {
	sw.mu.Lock()
	if version != 0 && version != sw.fileChangeVersion {
		sw.mu.Unlock()
		return
	}
	if sw.fileChangeTimer != nil {
		sw.fileChangeTimer.Stop()
		sw.fileChangeTimer = nil
	}
	if len(sw.pendingChanges) == 0 {
		sw.mu.Unlock()
		return
	}
	changesByPath := sw.pendingChanges
	dirsByPath := sw.pendingChangeDirs
	sw.pendingChanges = make(map[string]FileChangeEvent)
	sw.pendingChangeDirs = make(map[string]struct{})
	batchHandler := sw.onFileChangeBatch
	singleHandler := sw.onFileChange
	sw.mu.Unlock()

	paths := make([]string, 0, len(changesByPath))
	for path := range changesByPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	dirs := make([]string, 0, len(dirsByPath))
	for dir := range dirsByPath {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	events := make([]FileChangeEvent, 0, len(paths))
	for _, path := range paths {
		events = append(events, changesByPath[path])
	}

	if batchHandler != nil {
		batchHandler(FileChangeBatchEvent{
			RootID: sw.root.ID,
			Paths:  paths,
			Dirs:   dirs,
			Events: events,
		})
		return
	}
	if singleHandler != nil {
		for _, change := range events {
			singleHandler(change)
		}
	}
}

func parentDir(path string) string {
	clean := strings.Trim(filepath.ToSlash(path), "/")
	if clean == "" || clean == "." {
		return "."
	}
	idx := strings.LastIndex(clean, "/")
	if idx <= 0 {
		return "."
	}
	return clean[:idx]
}

func (sw *SharedFileWatcher) emitRelatedFile(change RelatedFileEvent) {
	sw.mu.RLock()
	handler := sw.onRelatedFile
	sw.mu.RUnlock()
	if handler != nil {
		handler(change)
	}
}

func (sw *SharedFileWatcher) WatchDir(dirRel string) error {
	dirAbs, err := sw.root.resolveRelativePath(dirRel)
	if err != nil {
		return err
	}
	if sw.shouldIgnore(dirAbs) {
		return nil
	}
	info, err := os.Stat(dirAbs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	clean := filepath.Clean(dirAbs)

	// Adding a watch is the one call in this file that can block indefinitely, so
	// it runs under its own mutex and never under sw.mu. Holding sw.mu across
	// fsnotify.Add meant a single stuck Add froze every later WatchDir call for
	// that root, and with it every directory listing and every file read 闂?the
	// project's file tree simply stopped loading until the process was restarted.
	sw.addMu.Lock()
	defer sw.addMu.Unlock()

	sw.mu.Lock()
	select {
	case <-sw.done:
		sw.mu.Unlock()
		return nil
	default:
	}
	if _, ok := sw.watchedDirs[clean]; ok {
		sw.mu.Unlock()
		return nil
	}
	if len(sw.watchedDirs) >= maxWatchDirs {
		sw.mu.Unlock()
		return nil
	}
	sw.mu.Unlock()

	if err := sw.watcher.Add(clean); err != nil {
		return err
	}

	sw.mu.Lock()
	if _, ok := sw.watchedDirs[clean]; !ok {
		sw.watchedDirs[clean] = struct{}{}
		sw.watchedDirList = append(sw.watchedDirList, clean)
	}
	sw.mu.Unlock()
	return nil
}

func (sw *SharedFileWatcher) watchNearestExistingDir(dirRel string) {
	dirRel = strings.TrimSpace(filepath.ToSlash(dirRel))
	if dirRel == "" {
		dirRel = "."
	}
	for {
		if err := sw.WatchDir(dirRel); err == nil {
			return
		}
		next := parentDir(dirRel)
		if next == dirRel {
			return
		}
		dirRel = next
	}
}

const (
	rescanMinInterval = 2 * time.Second
	rescanMaxEntries  = 200000
	rescanMaxReported = 2000
)

type dirEntrySignature struct {
	isDir bool
	size  int64
	mtime int64
	// Directories only: how many children the listing returned and the newest
	// child mtime. Together they move on create/delete/rename/modify inside the
	// directory even when the changed entry's own timestamp does not.
	children int64
	maxMTime int64
}

// invalidateDir lets the session store drop any per-directory state it keeps
// for dirAbs. Listings are read from disk on every request, so the store is the
// only place that could hold a stale snapshot.
func (sw *SharedFileWatcher) invalidateDir(dirAbs string) {
	if sw.sessionStore == nil {
		return
	}
	if invalidator, ok := sw.sessionStore.(interface{ InvalidateDir(absPath string) }); ok {
		invalidator.InvalidateDir(dirAbs)
	}
}

func (sw *SharedFileWatcher) isWatchedDir(dirAbs string) bool {
	sw.mu.RLock()
	defer sw.mu.RUnlock()
	_, ok := sw.watchedDirs[filepath.Clean(dirAbs)]
	return ok
}

func (sw *SharedFileWatcher) watchedDirsFull() bool {
	sw.mu.RLock()
	defer sw.mu.RUnlock()
	return len(sw.watchedDirs) >= maxWatchDirs
}

func (sw *SharedFileWatcher) forgetDir(dirAbs string) {
	clean := filepath.Clean(dirAbs)
	sw.mu.Lock()
	defer sw.mu.Unlock()
	delete(sw.watchedDirs, clean)
	for i := len(sw.watchedDirList) - 1; i >= 0; i-- {
		if sw.watchedDirList[i] == clean {
			sw.watchedDirList = append(sw.watchedDirList[:i], sw.watchedDirList[i+1:]...)
			break
		}
	}
}

type scanEntry struct {
	isDir    bool
	size     int64
	mtime    int64
	children int64
	maxMTime int64
	// Included for the directories Rescan walks: their non-ignored children.
	// Reported alongside the directory so a client re-lists it.
	childPaths []string
}

func signatureOf(entry scanEntry) dirEntrySignature {
	return dirEntrySignature{isDir: entry.isDir, size: entry.size, mtime: entry.mtime, children: entry.children, maxMTime: entry.maxMTime}
}

// RescanDiagnostics describes what the last Rescan pass saw. It is logged on
// reconnect so "the tree is stale" can be told apart from "the scan found
// nothing", which otherwise takes a debugger.
func (sw *SharedFileWatcher) RescanDiagnostics() string {
	sw.rescanMu.Lock()
	defer sw.rescanMu.Unlock()
	if sw.lastRescan.IsZero() {
		return "rescan=never"
	}
	total, files := 0, 0
	for _, entry := range sw.scanSnapshot {
		total++
		if !entry.isDir {
			files++
		}
	}
	return "rescan.last=" + sw.lastRescan.Format(time.RFC3339) + " entries=" + strconv.Itoa(total) + " files=" + strconv.Itoa(files)
}

// buildScanSnapshot returns every watched directory's listing (plus the watched
// directories themselves) keyed by absolute path. It runs under rescanMu, which
// Rescan also holds for the comparison pass, so a concurrent Rescan cannot
// observe or replace a half-built snapshot.
func (sw *SharedFileWatcher) buildScanSnapshot() map[string]scanEntry {
	snapshot := make(map[string]scanEntry)
	sw.mu.RLock()
	dirs := append([]string(nil), sw.watchedDirList...)
	sw.mu.RUnlock()
	seenEntries := 0
	var walk func(dirAbs string) bool
	walk = func(dirAbs string) bool {
		entries, err := os.ReadDir(dirAbs)
		if err != nil {
			if os.IsNotExist(err) {
				// The directory is gone and its watch went with it; drop the
				// bookkeeping so a recreated path gets watched again.
				sw.forgetDir(dirAbs)
			}
			return true
		}
		seenEntries += len(entries)
		if seenEntries > rescanMaxEntries {
			return false
		}
		dir := scanEntry{isDir: true, childPaths: make([]string, 0, len(entries))}
		subdirs := make([]string, 0, len(entries))
		for _, entry := range entries {
			abs := filepath.Join(dirAbs, entry.Name())
			if sw.shouldIgnore(abs) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			clean := filepath.Clean(abs)
			snapshot[clean] = scanEntry{
				isDir: info.IsDir(),
				size:  info.Size(),
				mtime: info.ModTime().UnixNano(),
			}
			dir.childPaths = append(dir.childPaths, clean)
			if mtime := info.ModTime().UnixNano(); mtime > dir.maxMTime {
				dir.maxMTime = mtime
			}
			if info.IsDir() {
				subdirs = append(subdirs, abs)
			}
		}
		dir.children = int64(len(dir.childPaths))
		snapshot[filepath.Clean(dirAbs)] = mergeScanEntry(snapshot[filepath.Clean(dirAbs)], dir)
		for _, sub := range subdirs {
			sw.mu.RLock()
			_, watched := sw.watchedDirs[sub]
			sw.mu.RUnlock()
			if !watched {
				continue
			}
			if !walk(sub) {
				return false
			}
		}
		return true
	}
	for _, dirAbs := range dirs {
		if sw.shouldIgnore(dirAbs) {
			continue
		}
		if !walk(dirAbs) {
			break
		}
	}
	return snapshot
}

// mergeScanEntry keeps the per-entry signature when a directory is also listed
// by its parent and replaces the listing fields with the deeper walk's.
func mergeScanEntry(existing, walked scanEntry) scanEntry {
	walked.size = existing.size
	walked.mtime = existing.mtime
	return walked
}

// Rescan compares the watched tree against the filesystem and emits a change
// event for everything that appeared, disappeared or was modified since the
// previous pass.
//
// fsnotify is not reliable as the only source of change events. A watch can
// silently stop delivering and the kernel/FileSystemWatcher buffers overflow
// under bursts (a build, a checkout, a formatter writing many files); on
// Windows fsnotify then drops events without telling the watcher. Nothing
// reached the UI, so the file tree and the open file kept showing stale
// content until the server was restarted.
//
// To survive timestamps that do not strictly increase (Windows rounds some
// mtimes to whole seconds, so a same-second rewrite can compare equal), files
// are not compared one by one: per directory, a change to any child's entry, to
// the child count, or to the newest child mtime reports every child of that
// directory. The client re-validates each file through its own mtime check.
func (sw *SharedFileWatcher) Rescan() []FileChangeEvent {
	sw.rescanMu.Lock()
	defer sw.rescanMu.Unlock()
	if time.Since(sw.lastRescan) < rescanMinInterval {
		return nil
	}
	defer func() { sw.lastRescan = time.Now() }()

	sw.mu.RLock()
	select {
	case <-sw.done:
		sw.mu.RUnlock()
		return nil
	default:
	}
	sw.mu.RUnlock()

	previous := sw.scanSnapshot
	current := sw.buildScanSnapshot()

	changes := make([]FileChangeEvent, 0)
	dirtyDirs := make(map[string]struct{})
	record := func(abs string, isDir bool) {
		rel, err := sw.root.relativeFromAbsolute(abs)
		if err != nil {
			return
		}
		if parent, err := sw.root.relativeFromAbsolute(filepath.Dir(abs)); err == nil {
			dirtyDirs[parent] = struct{}{}
		}
		if isDir {
			// The directory's own contents may have changed too.
			dirtyDirs[rel] = struct{}{}
		}
		if len(changes) < rescanMaxReported {
			changes = append(changes, FileChangeEvent{
				RootID: sw.root.ID,
				Path:   rel,
				Op:     "RESCAN",
				IsDir:  isDir,
			})
		}
	}
	for abs, entry := range current {
		prev, known := previous[abs]
		if known && signatureOf(prev) == signatureOf(entry) && prev.children == entry.children && prev.maxMTime == entry.maxMTime {
			continue
		}
		if !entry.isDir {
			record(abs, false)
			continue
		}
		// A changed parent means something inside it changed; report the whole
		// listing so nothing in it can hide behind an unchanged-looking stat.
		record(abs, true)
		for _, child := range entry.childPaths {
			record(child, current[child].isDir)
		}
		if !sw.isWatchedDir(abs) && !sw.watchedDirsFull() {
			// Pick up the new directory so later changes arrive as events.
			if rel, err := sw.root.relativeFromAbsolute(abs); err == nil {
				if err := sw.WatchDir(rel); err == nil {
					dirtyDirs[rel] = struct{}{}
				}
			}
		}
	}
	for abs, prev := range previous {
		if _, ok := current[abs]; !ok {
			record(abs, prev.isDir)
		}
	}
	sw.scanSnapshot = current
	for dir := range dirtyDirs {
		sw.invalidateDir(dir)
	}
	for _, change := range changes {
		sw.emitFileChange(change)
	}
	return changes
}

func (sw *SharedFileWatcher) shouldIgnore(path string) bool {
	metaDir := sw.root.MetaDir()
	if metaDir != "" && isPathWithin(path, metaDir) {
		return true
	}
	checkPath := path
	if filepath.IsAbs(path) {
		if rel, err := sw.root.relativeFromAbsolute(path); err == nil {
			checkPath = rel
		}
	}
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(checkPath)), "/") {
		if part == "" {
			continue
		}
		if _, ignored := ignoredWatchNames[part]; ignored {
			return true
		}
	}
	return false
}

func isPathWithin(path, dir string) bool {
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	if path == dir {
		return true
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
