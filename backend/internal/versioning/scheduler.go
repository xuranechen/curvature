package versioning

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// DefaultAutoCheckpointDebounce is the quiet period after which pending
// activity is recorded as a snapshot.
const DefaultAutoCheckpointDebounce = 30 * time.Second

// DefaultAutoCheckpointMaxDelay bounds how long activity can stay unrecorded
// while new activity keeps arriving.
const DefaultAutoCheckpointMaxDelay = 5 * time.Minute

// StoreProvider resolves the version store for a root.
type StoreProvider func(rootID string) (*Store, error)

// AutoSchedulerConfig configures an AutoScheduler.
type AutoSchedulerConfig struct {
	Provider StoreProvider
	// Debounce is the quiet period required before a snapshot is taken.
	// Zero uses DefaultAutoCheckpointDebounce.
	Debounce time.Duration
	// MaxDelay forces a snapshot even while activity keeps arriving. Zero uses
	// DefaultAutoCheckpointMaxDelay.
	MaxDelay time.Duration
	// Enabled reports whether automatic checkpoints are on. Nil means always on.
	Enabled func() bool
	// Now supplies the current time. Nil uses time.Now. Tests inject a clock to
	// exercise the debounce and maximum-delay windows without sleeping.
	Now func() time.Time
	// OnError observes failures. Nil logs instead.
	OnError func(rootID string, err error)
}

// AutoCheckpointResult reports what one scheduled checkpoint did.
type AutoCheckpointResult struct {
	RootID  string
	Created bool
	Hash    string
	Err     error
}

// AutoScheduler coalesces working-tree activity into automatic checkpoints.
//
// Callers only report that something happened; the scheduler decides when to
// snapshot. Notifying is deliberately cheap: an activity report that arrives
// while nothing changed costs one stat-cached scan that produces no commit.
type AutoScheduler struct {
	provider StoreProvider
	debounce time.Duration
	maxDelay time.Duration
	enabled  func() bool
	now      func() time.Time
	onError  func(rootID string, err error)

	mu      sync.Mutex
	first   map[string]time.Time
	last    map[string]time.Time
	reasons map[string]string
	wake    chan struct{}
	stopped bool
}

// NewAutoScheduler builds a scheduler. It does nothing until Start or Flush is
// called, so tests can drive it synchronously.
func NewAutoScheduler(cfg AutoSchedulerConfig) *AutoScheduler {
	if cfg.Provider == nil {
		return nil
	}
	debounce := cfg.Debounce
	if debounce <= 0 {
		debounce = DefaultAutoCheckpointDebounce
	}
	maxDelay := cfg.MaxDelay
	if maxDelay <= 0 {
		maxDelay = DefaultAutoCheckpointMaxDelay
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &AutoScheduler{
		provider: cfg.Provider,
		debounce: debounce,
		maxDelay: maxDelay,
		enabled:  cfg.Enabled,
		now:      now,
		onError:  cfg.OnError,
		first:    map[string]time.Time{},
		last:     map[string]time.Time{},
		reasons:  map[string]string{},
		wake:     make(chan struct{}, 1),
	}
}

// Debounce exposes the configured quiet period.
func (s *AutoScheduler) Debounce() time.Duration {
	if s == nil {
		return 0
	}
	return s.debounce
}

// currentTime reads the scheduler clock.
func (s *AutoScheduler) currentTime() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// Enabled reports whether checkpoints are currently permitted.
func (s *AutoScheduler) Enabled() bool {
	if s == nil {
		return false
	}
	if s.enabled == nil {
		return true
	}
	return s.enabled()
}

// Notify reports activity in a root. The reason is stored for the eventual
// commit's origin so the timeline can explain why a checkpoint exists.
func (s *AutoScheduler) Notify(rootID, reason string) {
	if s == nil {
		return
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" || !s.Enabled() {
		return
	}
	now := s.currentTime()
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if _, ok := s.first[rootID]; !ok {
		s.first[rootID] = now
	}
	s.last[rootID] = now
	if reason != "" {
		s.reasons[rootID] = reason
	}
	s.mu.Unlock()

	// Wake the loop without blocking: a pending signal is already enough.
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// due is one root whose activity is ready to be recorded.
type due struct {
	rootID string
	reason string
}

// dueRoots returns the roots whose quiet period has elapsed, or whose activity
// has been arriving for longer than the maximum delay. The pending entry is
// consumed so a root cannot be flushed twice.
func (s *AutoScheduler) dueRoots(now time.Time) []due {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := make([]due, 0, len(s.last))
	for rootID, last := range s.last {
		quiet := now.Sub(last) >= s.debounce
		overdue := now.Sub(s.first[rootID]) >= s.maxDelay
		if quiet || overdue {
			pending = append(pending, due{rootID: rootID, reason: s.reasons[rootID]})
			delete(s.first, rootID)
			delete(s.last, rootID)
			delete(s.reasons, rootID)
		}
	}
	return pending
}

// Flush records a checkpoint for every root that is due at now.
//
// It is exported so tests and the CLI can drive the scheduler deterministically
// instead of waiting on a timer.
func (s *AutoScheduler) Flush(now time.Time) []AutoCheckpointResult {
	if s == nil {
		return nil
	}
	pending := s.dueRoots(now)
	if len(pending) == 0 {
		return nil
	}
	results := make([]AutoCheckpointResult, 0, len(pending))
	for _, item := range pending {
		results = append(results, s.checkpoint(item))
	}
	return results
}

// checkpoint records one automatic snapshot.
func (s *AutoScheduler) checkpoint(item due) AutoCheckpointResult {
	result := AutoCheckpointResult{RootID: item.rootID}
	store, err := s.provider(item.rootID)
	if err != nil {
		result.Err = err
		s.reportError(item.rootID, err)
		return result
	}
	if store == nil {
		return result
	}
	snapshot, err := store.Snapshot(SnapshotOptions{
		Message:    "auto checkpoint",
		Origin:     &Origin{Kind: OriginAuto, Label: item.reason},
		Checkpoint: true,
	})
	if err != nil {
		result.Err = err
		s.reportError(item.rootID, err)
		return result
	}
	result.Created = snapshot.Created
	result.Hash = snapshot.Commit.Hash
	return result
}

func (s *AutoScheduler) reportError(rootID string, err error) {
	if s.onError != nil {
		s.onError(rootID, err)
		return
	}
	log.Printf("[versioning/auto] checkpoint.error root=%s err=%v", rootID, err)
}

// Start runs the scheduler until ctx is cancelled.
//
// A disabled scheduler still drains its queue, so re-enabling later does not
// replay a backlog of stale activity.
func (s *AutoScheduler) Start(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	interval := s.debounce / 2
	if interval > 5*time.Second {
		interval = 5 * time.Second
	}
	if interval < 250*time.Millisecond {
		interval = 250 * time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.mu.Lock()
				s.stopped = true
				s.mu.Unlock()
				return
			case <-s.wake:
				s.Flush(time.Now())
			case now := <-ticker.C:
				s.Flush(now)
			}
		}
	}()
}
