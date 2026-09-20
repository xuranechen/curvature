package usecase

import (
	"context"
	"errors"
	"strings"
	"time"

	"curvature/backend/internal/fs"
	"curvature/backend/internal/versioning"
)

// maxVersioningPreviewBytes caps the historical file preview. protectedEndpoint
// buffers the response and round-trips it through JSON, so an unbounded read
// would be able to exhaust memory on a large file.
const maxVersioningPreviewBytes = 1 << 20

// defaultVersioningLogLimit is the page size for history listings.
const defaultVersioningLogLimit = 50

// versioningStore resolves a root and opens its version store.
//
// Store instances come from a package-level cache keyed by the store directory,
// so opening per request is cheap and no extra Registry method is needed.
func (s *Service) versioningStore(rootID string) (*versioning.Store, fs.RootInfo, error) {
	if err := s.ensureRegistry(); err != nil {
		return nil, fs.RootInfo{}, err
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return nil, fs.RootInfo{}, errors.New("root required")
	}
	root, err := s.Registry.GetRoot(rootID)
	if err != nil {
		return nil, fs.RootInfo{}, err
	}
	if strings.TrimSpace(root.RootPath) == "" {
		return nil, fs.RootInfo{}, errors.New("root required")
	}
	metaDir, err := root.EnsureMetaDir()
	if err != nil {
		return nil, fs.RootInfo{}, err
	}
	return versioning.Open(root.RootPath, metaDir), root, nil
}

// versioningAutoCheckpointNotifier is implemented by the application context.
//
// This is deliberately an optional interface rather than a Registry method:
// Registry is implemented by several test fakes, and widening it would force
// every one of them to grow a method they do not care about.
type versioningAutoCheckpointNotifier interface {
	NotifyVersioningAutoCheckpoint(rootID, sessionID string)
}

// notifyVersioningAutoCheckpoint reports that a session turn touched a root.
//
// The scheduler decides whether and when that becomes a snapshot, so this is
// cheap: repeated calls inside the debounce window collapse into one commit, and
// a call that finds no new content produces no commit at all.
func notifyVersioningAutoCheckpoint(registry Registry, rootID, sessionID string) {
	if registry == nil {
		return
	}
	notifier, ok := registry.(versioningAutoCheckpointNotifier)
	if !ok {
		return
	}
	notifier.NotifyVersioningAutoCheckpoint(rootID, sessionID)
}

// ---------------------------------------------------------------------------
// info / status
// ---------------------------------------------------------------------------

type VersioningInfoInput struct {
	RootID string
}

type VersioningInfoOutput struct {
	Info  versioning.RepoInfo `json:"info"`
	Root  string              `json:"root"`
	Label string              `json:"label"`
}

// GetVersioningInfo reports whether versioning is active for a root.
func (s *Service) GetVersioningInfo(_ context.Context, in VersioningInfoInput) (VersioningInfoOutput, error) {
	store, root, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningInfoOutput{}, err
	}
	info, err := store.Info()
	if err != nil {
		return VersioningInfoOutput{}, err
	}
	return VersioningInfoOutput{Info: info, Root: root.RootPath, Label: root.Name}, nil
}

type VersioningStatusInput struct {
	RootID string
	Limit  int
}

type VersioningStatusOutput struct {
	Status versioning.StatusResult `json:"status"`
}

// GetVersioningStatus compares the working tree with the latest snapshot.
func (s *Service) GetVersioningStatus(_ context.Context, in VersioningStatusInput) (VersioningStatusOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningStatusOutput{}, err
	}
	status, err := store.Status(in.Limit)
	if err != nil {
		return VersioningStatusOutput{}, err
	}
	return VersioningStatusOutput{Status: status}, nil
}

// ---------------------------------------------------------------------------
// snapshots
// ---------------------------------------------------------------------------

type VersioningSnapshotInput struct {
	RootID  string
	Message string
	Author  string
	Tag     string
	// OriginKind labels why the snapshot exists (manual, auto, session, task).
	OriginKind string
	SessionID  string
	TaskID     string
	// Force records a snapshot even when the content is unchanged.
	Force bool
}

type VersioningSnapshotOutput struct {
	Commit  versioning.Commit `json:"commit"`
	Created bool              `json:"created"`
	Reason  string            `json:"reason,omitempty"`
}

// CreateVersioningSnapshot records the working tree as a point in time.
func (s *Service) CreateVersioningSnapshot(_ context.Context, in VersioningSnapshotInput) (VersioningSnapshotOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningSnapshotOutput{}, err
	}
	origin := buildVersioningOrigin(in.OriginKind, in.SessionID, in.TaskID, in.Message)
	result, err := store.Snapshot(versioning.SnapshotOptions{
		Message: strings.TrimSpace(in.Message),
		Author:  strings.TrimSpace(in.Author),
		Tag:     strings.TrimSpace(in.Tag),
		Force:   in.Force,
		Origin:  origin,
	})
	if err != nil {
		return VersioningSnapshotOutput{}, err
	}
	return VersioningSnapshotOutput{Commit: result.Commit, Created: result.Created, Reason: result.Reason}, nil
}

// buildVersioningOrigin normalises an origin kind and drops an empty origin so
// manual snapshots stay free of meaningless metadata.
func buildVersioningOrigin(kind, sessionID, taskID, label string) *versioning.Origin {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = versioning.OriginManual
	}
	sessionID = strings.TrimSpace(sessionID)
	taskID = strings.TrimSpace(taskID)
	if kind == versioning.OriginManual && sessionID == "" && taskID == "" {
		return nil
	}
	return &versioning.Origin{
		Kind:      kind,
		SessionID: sessionID,
		TaskID:    taskID,
		Label:     strings.TrimSpace(label),
	}
}

// ---------------------------------------------------------------------------
// history
// ---------------------------------------------------------------------------

type VersioningLogInput struct {
	RootID string
	Limit  int
	Before string
	Branch string
	Path   string
}

type VersioningLogOutput struct {
	Commits []versioning.Commit `json:"commits"`
	Limit   int                 `json:"limit"`
}

// GetVersioningLog walks the snapshot history newest first.
func (s *Service) GetVersioningLog(_ context.Context, in VersioningLogInput) (VersioningLogOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningLogOutput{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultVersioningLogLimit
	}
	if limit > 500 {
		limit = 500
	}
	commits, err := store.Log(versioning.LogOptions{
		Limit:  limit,
		Branch: strings.TrimSpace(in.Branch),
		Before: strings.TrimSpace(in.Before),
		Path:   strings.TrimSpace(in.Path),
	})
	if err != nil {
		return VersioningLogOutput{}, err
	}
	if commits == nil {
		commits = []versioning.Commit{}
	}
	return VersioningLogOutput{Commits: commits, Limit: limit}, nil
}

type VersioningChangesInput struct {
	RootID string
	From   string
	To     string
}

type VersioningChangesOutput struct {
	Changes []versioning.ChangeItem `json:"changes"`
	From    string                  `json:"from"`
	To      string                  `json:"to"`
}

// GetVersioningChanges lists path-level differences between two snapshots.
func (s *Service) GetVersioningChanges(_ context.Context, in VersioningChangesInput) (VersioningChangesOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningChangesOutput{}, err
	}
	changes, err := store.Changes(strings.TrimSpace(in.From), strings.TrimSpace(in.To))
	if err != nil {
		return VersioningChangesOutput{}, err
	}
	if changes == nil {
		changes = []versioning.ChangeItem{}
	}
	return VersioningChangesOutput{
		Changes: changes,
		From:    strings.TrimSpace(in.From),
		To:      strings.TrimSpace(in.To),
	}, nil
}

type VersioningTimelineInput struct {
	RootID string
	Path   string
	Limit  int
}

type VersioningTimelineOutput struct {
	Path      string                    `json:"path"`
	Revisions []versioning.RevisionInfo `json:"revisions"`
}

// GetVersioningTimeline lists the snapshots that changed one path.
func (s *Service) GetVersioningTimeline(_ context.Context, in VersioningTimelineInput) (VersioningTimelineOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningTimelineOutput{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultVersioningLogLimit
	}
	revisions, err := store.Timeline(strings.TrimSpace(in.Path), limit)
	if err != nil {
		return VersioningTimelineOutput{}, err
	}
	if revisions == nil {
		revisions = []versioning.RevisionInfo{}
	}
	return VersioningTimelineOutput{Path: strings.TrimSpace(in.Path), Revisions: revisions}, nil
}

// ---------------------------------------------------------------------------
// content access
// ---------------------------------------------------------------------------

type VersioningFileDiffInput struct {
	RootID  string
	From    string
	To      string
	Path    string
	Context int
}

type VersioningFileDiffOutput struct {
	Diff versioning.DiffResult `json:"diff"`
}

// GetVersioningFileDiff renders a git-shaped unified diff for one path.
func (s *Service) GetVersioningFileDiff(_ context.Context, in VersioningFileDiffInput) (VersioningFileDiffOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningFileDiffOutput{}, err
	}
	path := strings.TrimSpace(in.Path)
	if path == "" {
		return VersioningFileDiffOutput{}, errors.New("path required")
	}
	diff, err := store.FileDiff(strings.TrimSpace(in.From), strings.TrimSpace(in.To), path, in.Context)
	if err != nil {
		return VersioningFileDiffOutput{}, err
	}
	return VersioningFileDiffOutput{Diff: diff}, nil
}

type VersioningFileInput struct {
	RootID string
	Path   string
	// Revision defaults to HEAD. The pseudo-revision WORKTREE reads from disk.
	Revision string
	MaxBytes int
}

type VersioningFileOutput struct {
	Path      string `json:"path"`
	Revision  string `json:"revision"`
	Present   bool   `json:"present"`
	Hash      string `json:"hash"`
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content,omitempty"`
}

// GetVersioningFile returns a bounded preview of a file at a snapshot.
func (s *Service) GetVersioningFile(_ context.Context, in VersioningFileInput) (VersioningFileOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningFileOutput{}, err
	}
	path := strings.TrimSpace(in.Path)
	if path == "" {
		return VersioningFileOutput{}, errors.New("path required")
	}
	maxBytes := in.MaxBytes
	if maxBytes <= 0 || maxBytes > maxVersioningPreviewBytes {
		maxBytes = maxVersioningPreviewBytes
	}

	data, present, hash, err := store.FileAt(strings.TrimSpace(in.Revision), path)
	if err != nil {
		return VersioningFileOutput{}, err
	}
	output := VersioningFileOutput{
		Path:     path,
		Revision: strings.TrimSpace(in.Revision),
		Present:  present,
		Hash:     hash,
		Size:     int64(len(data)),
	}
	if !present {
		return output, nil
	}
	if int64(len(data)) > int64(maxBytes) {
		output.Truncated = true
		data = data[:maxBytes]
	}
	binary := versioning.IsBinaryPayload(data)
	output.Binary = binary
	if !binary {
		output.Content = string(data)
	}
	return output, nil
}

type VersioningTreeInput struct {
	RootID   string
	Revision string
	Dir      string
}

type VersioningTreeOutput struct {
	Entries []versioning.TreeEntry `json:"entries"`
}

// GetVersioningTree lists the files a snapshot contains below a directory.
func (s *Service) GetVersioningTree(_ context.Context, in VersioningTreeInput) (VersioningTreeOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningTreeOutput{}, err
	}
	entries, err := store.Tree(strings.TrimSpace(in.Revision), strings.TrimSpace(in.Dir))
	if err != nil {
		return VersioningTreeOutput{}, err
	}
	if entries == nil {
		entries = []versioning.TreeEntry{}
	}
	return VersioningTreeOutput{Entries: entries}, nil
}

// ---------------------------------------------------------------------------
// restore
// ---------------------------------------------------------------------------

type VersioningRestoreInput struct {
	RootID   string
	Revision string
	// Paths limits the restore; empty means the whole project.
	Paths []string
	// Mode is "overwrite" (default, history untouched) or "revert" (records a
	// new forward-only snapshot).
	Mode       string
	Message    string
	SkipSafety bool
}

type VersioningRestoreOutput struct {
	Result versioning.RestoreResult `json:"result"`
}

// RestoreVersioning rewinds the working tree to a snapshot.
func (s *Service) RestoreVersioning(_ context.Context, in VersioningRestoreInput) (VersioningRestoreOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningRestoreOutput{}, err
	}
	revision := strings.TrimSpace(in.Revision)
	if revision == "" {
		return VersioningRestoreOutput{}, errors.New("revision required")
	}
	result, err := store.Restore(versioning.RestoreOptions{
		Revision:   revision,
		Paths:      in.Paths,
		Mode:       strings.TrimSpace(in.Mode),
		Message:    strings.TrimSpace(in.Message),
		SkipSafety: in.SkipSafety,
	})
	if err != nil {
		return VersioningRestoreOutput{}, err
	}
	return VersioningRestoreOutput{Result: result}, nil
}

// ---------------------------------------------------------------------------
// timelines and tags
// ---------------------------------------------------------------------------

type VersioningBranchesInput struct {
	RootID string
}

type VersioningBranchesOutput struct {
	Branches []versioning.BranchItem `json:"branches"`
}

// GetVersioningBranches lists timelines.
func (s *Service) GetVersioningBranches(_ context.Context, in VersioningBranchesInput) (VersioningBranchesOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningBranchesOutput{}, err
	}
	branches, err := store.Branches()
	if err != nil {
		return VersioningBranchesOutput{}, err
	}
	if branches == nil {
		branches = []versioning.BranchItem{}
	}
	return VersioningBranchesOutput{Branches: branches}, nil
}

type VersioningBranchCreateInput struct {
	RootID string
	Name   string
	From   string
}

type VersioningBranchCreateOutput struct {
	Name string `json:"name"`
	From string `json:"from"`
}

// CreateVersioningBranch starts a new timeline at a snapshot.
func (s *Service) CreateVersioningBranch(_ context.Context, in VersioningBranchCreateInput) (VersioningBranchCreateOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningBranchCreateOutput{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return VersioningBranchCreateOutput{}, errors.New("name required")
	}
	from := strings.TrimSpace(in.From)
	if err := store.CreateBranch(name, from); err != nil {
		return VersioningBranchCreateOutput{}, err
	}
	if from == "" {
		from = versioning.HeadRev
	}
	return VersioningBranchCreateOutput{Name: name, From: from}, nil
}

type VersioningCheckoutInput struct {
	RootID string
	Name   string
	Mode   string
}

type VersioningCheckoutOutput struct {
	Result versioning.RestoreResult `json:"result"`
}

// CheckoutVersioningTimeline switches to a timeline and restores its content.
func (s *Service) CheckoutVersioningTimeline(_ context.Context, in VersioningCheckoutInput) (VersioningCheckoutOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningCheckoutOutput{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return VersioningCheckoutOutput{}, errors.New("name required")
	}
	result, err := store.Checkout(name, strings.TrimSpace(in.Mode))
	if err != nil {
		return VersioningCheckoutOutput{}, err
	}
	return VersioningCheckoutOutput{Result: result}, nil
}

type VersioningTagsInput struct {
	RootID string
}

type VersioningTagsOutput struct {
	Tags []versioning.TagItem `json:"tags"`
}

// GetVersioningTags lists bookmarks.
func (s *Service) GetVersioningTags(_ context.Context, in VersioningTagsInput) (VersioningTagsOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningTagsOutput{}, err
	}
	tags, err := store.Tags()
	if err != nil {
		return VersioningTagsOutput{}, err
	}
	if tags == nil {
		tags = []versioning.TagItem{}
	}
	return VersioningTagsOutput{Tags: tags}, nil
}

type VersioningTagCreateInput struct {
	RootID string
	Name   string
	// Revision defaults to HEAD.
	Revision string
}

type VersioningTagCreateOutput struct {
	Name string `json:"name"`
}

// CreateVersioningTag bookmarks a snapshot.
func (s *Service) CreateVersioningTag(_ context.Context, in VersioningTagCreateInput) (VersioningTagCreateOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningTagCreateOutput{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return VersioningTagCreateOutput{}, errors.New("name required")
	}
	if err := store.CreateTag(name, strings.TrimSpace(in.Revision)); err != nil {
		return VersioningTagCreateOutput{}, err
	}
	return VersioningTagCreateOutput{Name: name}, nil
}

type VersioningTagDeleteInput struct {
	RootID string
	Name   string
}

type VersioningTagDeleteOutput struct {
	Name string `json:"name"`
}

// DeleteVersioningTag removes a bookmark.
func (s *Service) DeleteVersioningTag(_ context.Context, in VersioningTagDeleteInput) (VersioningTagDeleteOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningTagDeleteOutput{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return VersioningTagDeleteOutput{}, errors.New("name required")
	}
	if err := store.DeleteTag(name); err != nil {
		return VersioningTagDeleteOutput{}, err
	}
	return VersioningTagDeleteOutput{Name: name}, nil
}

// ---------------------------------------------------------------------------
// maintenance
// ---------------------------------------------------------------------------

type VersioningGCInput struct {
	RootID string
	// GracePeriodSeconds protects freshly written objects. Zero uses one hour.
	GracePeriodSeconds int
}

type VersioningGCOutput struct {
	Result versioning.GCResult `json:"result"`
}

// RunVersioningGC drops objects that no snapshot or bookmark references.
func (s *Service) RunVersioningGC(_ context.Context, in VersioningGCInput) (VersioningGCOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningGCOutput{}, err
	}
	var grace time.Duration
	if in.GracePeriodSeconds > 0 {
		grace = time.Duration(in.GracePeriodSeconds) * time.Second
	}
	result, err := store.GC(versioning.GCOptions{GracePeriod: grace})
	if err != nil {
		return VersioningGCOutput{}, err
	}
	return VersioningGCOutput{Result: result}, nil
}

type VersioningVerifyInput struct {
	RootID string
}

type VersioningVerifyOutput struct {
	OK bool `json:"ok"`
}

// VerifyVersioning checks every reachable object in the store.
func (s *Service) VerifyVersioning(_ context.Context, in VersioningVerifyInput) (VersioningVerifyOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningVerifyOutput{}, err
	}
	if err := store.Verify(); err != nil {
		return VersioningVerifyOutput{}, err
	}
	return VersioningVerifyOutput{OK: true}, nil
}

// defaultCheckpointRetention is how many automatic checkpoints are kept when the
// caller does not say, and no preference store is configured.
const defaultCheckpointRetention = 200

type VersioningCheckpointsInput struct {
	RootID string
	Limit  int
}

type VersioningCheckpointsOutput struct {
	Commits []versioning.Commit `json:"commits"`
	Total   int                 `json:"total"`
}

// ListVersioningCheckpoints lists automatic checkpoints newest first.
//
// They are kept out of Log on purpose: they are safety nets, not part of the
// project's narrative history, so the UI shows them separately.
func (s *Service) ListVersioningCheckpoints(_ context.Context, in VersioningCheckpointsInput) (VersioningCheckpointsOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningCheckpointsOutput{}, err
	}
	commits, err := store.Checkpoints(in.Limit)
	if err != nil {
		return VersioningCheckpointsOutput{}, err
	}
	return VersioningCheckpointsOutput{Commits: commits, Total: store.CheckpointCount()}, nil
}

type VersioningCheckpointPruneInput struct {
	RootID string
	// Keep is how many of the newest checkpoints to retain. Zero means the
	// default; a negative value means keep none.
	Keep int
}

type VersioningCheckpointPruneOutput struct {
	Result versioning.PruneCheckpointsResult `json:"result"`
	GC     versioning.GCResult               `json:"gc"`
}

// PruneVersioningCheckpoints applies the retention policy to automatic
// checkpoints and then reclaims the space they held.
//
// Retention drops the oldest checkpoints; the user's timelines and the safety
// commits restores create on them are never touched.
func (s *Service) PruneVersioningCheckpoints(_ context.Context, in VersioningCheckpointPruneInput) (VersioningCheckpointPruneOutput, error) {
	store, _, err := s.versioningStore(in.RootID)
	if err != nil {
		return VersioningCheckpointPruneOutput{}, err
	}
	keep := in.Keep
	if keep == 0 {
		// The user preference decides; the constant is only the last resort when
		// no preference store is configured.
		keep = defaultCheckpointRetention
		if prefs := s.Registry.GetPreferences(); prefs != nil {
			keep = prefs.VersioningCheckpointRetentionValue()
		}
	}
	result, err := store.PruneCheckpoints(keep)
	if err != nil {
		return VersioningCheckpointPruneOutput{}, err
	}
	// Pruning only removes refs, so sweep immediately: under the default grace
	// period the reclaimed space would sit there for another hour and the prune
	// would look like a no-op. A failed GC still leaves a correct store that is
	// merely larger than needed, so the prune result is reported either way.
	gc, err := store.GC(versioning.GCOptions{Immediate: true})
	if err != nil {
		return VersioningCheckpointPruneOutput{Result: result}, err
	}
	return VersioningCheckpointPruneOutput{Result: result, GC: gc}, nil
}
