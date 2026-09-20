package versioning

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// CheckpointsDir is the ref namespace holding automatic checkpoints.
//
// It sits outside refs/heads so checkpoints never appear as user timelines and
// can never be switched to by accident.
const CheckpointsDir = "checkpoints"

// CheckpointStampLayout names a checkpoint ref so that lexicographic order
// matches chronological order.
const CheckpointStampLayout = "20060102T150405.000000000"

// PruneCheckpointsResult reports what a retention pass did.
type PruneCheckpointsResult struct {
	Timeline string `json:"timeline"`
	Kept     int    `json:"kept"`
	Dropped  int    `json:"dropped"`
}

func (s *Store) checkpointsRoot() string {
	return filepath.Join(s.storeDir, "refs", CheckpointsDir)
}

func (s *Store) checkpointRefPath(stamp string) string {
	return filepath.Join(s.checkpointsRoot(), stamp)
}

// checkpointStamps lists checkpoint ref names oldest first.
func (s *Store) checkpointStamps() ([]string, error) {
	entries, err := os.ReadDir(s.checkpointsRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	stamps := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		stamps = append(stamps, entry.Name())
	}
	sort.Strings(stamps)
	return stamps, nil
}

// newestCheckpoint returns the hash of the most recent checkpoint, or "".
func (s *Store) newestCheckpoint() string {
	stamps, err := s.checkpointStamps()
	if err != nil || len(stamps) == 0 {
		return ""
	}
	hash, err := s.readRef(s.checkpointRefPath(stamps[len(stamps)-1]))
	if err != nil {
		return ""
	}
	return hash
}

// CheckpointCount reports how many automatic checkpoints are retained.
func (s *Store) CheckpointCount() int {
	stamps, err := s.checkpointStamps()
	if err != nil {
		return 0
	}
	return len(stamps)
}

// Checkpoints lists automatic checkpoints newest first.
//
// Checkpoints are deliberately absent from Log, which walks the user's timeline:
// they are safety nets rather than part of the project's narrative history, so
// callers ask for them explicitly.
func (s *Store) Checkpoints(limit int) ([]Commit, error) {
	stamps, err := s.checkpointStamps()
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > len(stamps) {
		limit = len(stamps)
	}
	commits := make([]Commit, 0, limit)
	// Stamps sort oldest first, so walk backwards for newest first.
	for i := len(stamps) - 1; i >= 0 && len(commits) < limit; i-- {
		hash, err := s.readRef(s.checkpointRefPath(stamps[i]))
		if err != nil || hash == "" {
			continue
		}
		commit, err := readCommit(s.storeDir, hash)
		if err != nil {
			continue
		}
		commit.Tags = s.tagsFor(hash)
		commits = append(commits, commit)
	}
	return commits, nil
}

// PruneCheckpoints keeps only the newest `keep` automatic checkpoints.
//
// Retention is a ref deletion, not a history rewrite. Each checkpoint is stored
// as a parentless commit, so dropping one simply makes it unreachable and a
// later GC reclaims its objects. The user's timelines — and the safety commits
// restores create on them — are never touched.
//
// Pruning only removes refs; call GC to reclaim the space.
func (s *Store) PruneCheckpoints(keep int) (PruneCheckpointsResult, error) {
	result := PruneCheckpointsResult{Timeline: CheckpointsDir}
	err := s.withLock(func() error {
		if !s.Initialized() {
			return ErrNotInitialized
		}
		stamps, err := s.checkpointStamps()
		if err != nil {
			return err
		}
		if keep < 0 {
			keep = 0
		}
		if len(stamps) <= keep {
			result.Kept = len(stamps)
			return nil
		}
		for _, stamp := range stamps[:len(stamps)-keep] {
			if err := os.Remove(s.checkpointRefPath(stamp)); err != nil {
				return fmt.Errorf("versioning: prune checkpoint %s: %w", stamp, err)
			}
			result.Dropped++
		}
		result.Kept = keep
		return nil
	})
	return result, err
}
