package api

import (
	"context"
	"os"
	"strings"

	"curvature/backend/internal/versioning"
)

// autoCheckpointsEnv disables automatic checkpoints when set to a falsey value.
//
// The PRD defaults automatic checkpoints to on (D5) and the user-facing switch
// lives in the settings panel (POST/PUT /api/preferences/versioning-auto-checkpoints).
// This environment variable is the operator-level override: it can force the
// feature off, and can never force it on against the user's preference.
const autoCheckpointsEnv = "CURVATURE_AUTO_CHECKPOINTS"

// The application context is what usecase reaches through its optional
// notifier interface; keep the two in sync at compile time.
var _ interface {
	NotifyVersioningAutoCheckpoint(rootID, sessionID string)
} = (*AppContext)(nil)

// VersioningStore resolves the local version store backing a root.
//
// Store instances come from a package-level cache keyed by the store directory,
// so resolving on every notification is cheap. A missing or unregistered root
// reports (nil, nil): the scheduler treats that as "nothing to do" rather than
// an error, because roots come and go while activity is queued.
func (s *AppContext) VersioningStore(rootID string) (*versioning.Store, error) {
	if s == nil {
		return nil, nil
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return nil, nil
	}
	root, err := s.GetRoot(rootID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(root.RootPath) == "" {
		return nil, nil
	}
	metaDir, err := root.EnsureMetaDir()
	if err != nil {
		return nil, err
	}
	return versioning.Open(root.RootPath, metaDir), nil
}

// StartVersioningAutoCheckpoints wires and starts the automatic checkpoint
// scheduler. It is safe to call when versioning is unwanted: the scheduler
// simply never receives a notification.
func (s *AppContext) StartVersioningAutoCheckpoints(ctx context.Context) {
	if s == nil || s.AutoCheckpoints != nil {
		return
	}
	scheduler := versioning.NewAutoScheduler(versioning.AutoSchedulerConfig{
		Provider: s.VersioningStore,
		Enabled:  s.versioningAutoCheckpointsEnabled,
	})
	if scheduler == nil {
		return
	}
	s.AutoCheckpoints = scheduler
	scheduler.Start(ctx)
}

// NotifyVersioningAutoCheckpoint reports working-tree activity in a root.
//
// usecase reaches this through a type assertion rather than a Registry method,
// so the Registry interface stays as narrow as it is and the fakes in tests do
// not have to implement it.
func (s *AppContext) NotifyVersioningAutoCheckpoint(rootID, sessionID string) {
	reason := "auto"
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		reason = "session:" + sessionID
	}
	s.notifyAutoCheckpoint(rootID, reason)
}

// ObserveTaskTerminal records a checkpoint when a kanban task reaches a terminal
// state. A finished task is a natural boundary between units of work, so it
// complements the per-session-turn checkpoints rather than replacing them.
//
// The shape is asserted against kanban.TaskObserver at the wiring site in
// backend/app, which is where the two packages meet.
func (s *AppContext) ObserveTaskTerminal(rootID, taskID, status string) {
	parts := []string{"task"}
	if taskID = strings.TrimSpace(taskID); taskID != "" {
		parts = append(parts, taskID)
	}
	if status = strings.TrimSpace(status); status != "" {
		parts = append(parts, status)
	}
	s.notifyAutoCheckpoint(rootID, strings.Join(parts, ":"))
}

// notifyAutoCheckpoint is the single funnel into the scheduler.
func (s *AppContext) notifyAutoCheckpoint(rootID, reason string) {
	if s == nil || s.AutoCheckpoints == nil {
		return
	}
	s.AutoCheckpoints.Notify(rootID, reason)
}

// versioningAutoCheckpointsEnabled reports whether automatic checkpoints are on.
//
// The user preference decides. The environment variable is an operator override
// that can only force the feature off, never on.
//
// The scheduler consults this on every notification rather than once at startup,
// so toggling the preference takes effect immediately without a restart. It also
// means activity queued while the feature was off is dropped instead of
// replayed later, because a disabled scheduler never records it in the first
// place.
func (s *AppContext) versioningAutoCheckpointsEnabled() bool {
	if !autoCheckpointsEnvEnabled() {
		return false
	}
	prefs := s.GetPreferences()
	if prefs == nil {
		return true
	}
	return prefs.VersioningAutoCheckpointsEnabled()
}

// autoCheckpointsEnvEnabled reports whether the operator override permits the
// feature. An unset or unrecognised value permits it and leaves the decision to
// the user preference.
func autoCheckpointsEnvEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(autoCheckpointsEnv))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}
