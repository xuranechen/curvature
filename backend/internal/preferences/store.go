package preferences

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"curvature/backend/internal/agent"
	"curvature/backend/internal/config"
)

const preferencesFileName = "preferences.json"

const DefaultIdleSessionResourceReleaseHours = 72
const MaxIdleSessionResourceReleaseHours = 2_562_047

// DefaultVersioningCheckpointRetention is how many automatic checkpoints are
// kept before the oldest are pruned.
const DefaultVersioningCheckpointRetention = 200

// MaxVersioningCheckpointRetention bounds the preference so a typo cannot pin an
// unbounded number of checkpoints.
const MaxVersioningCheckpointRetention = 100_000

type Store struct {
	mu   sync.RWMutex
	path string
	data UserPreferences
}

type UserPreferences struct {
	Agents                          map[string]AgentDefaults `json:"agents,omitempty"`
	SessionNaming                   SessionNamingDefaults    `json:"session_naming,omitempty"`
	IdleSessionResourceReleaseHours int                      `json:"idle_session_resource_release_hours,omitempty"`
	NewProjectMetaLocation          string                   `json:"new_project_meta_location,omitempty"`
	// VersioningAutoCheckpointsDisabled is stored as a negative flag so that an
	// absent value means enabled: automatic checkpoints default to on.
	VersioningAutoCheckpointsDisabled bool `json:"versioning_auto_checkpoints_disabled,omitempty"`
	// VersioningCheckpointRetention is how many automatic checkpoints to keep.
	// Zero means the default.
	VersioningCheckpointRetention int `json:"versioning_checkpoint_retention,omitempty"`
	// VersioningAgentAutonomyEnabled lets an agent rewrite the working tree
	// through restore and checkout. Unlike the two fields above this one is a
	// plain positive flag, because the default is *off*: agents may read history
	// and take snapshots, but rolling the tree back stays a human decision until
	// the user opts in.
	VersioningAgentAutonomyEnabled bool `json:"versioning_agent_autonomy_enabled,omitempty"`
}

// VersioningAgentAutonomyEnabled reports whether agents may run restore and
// checkout. The default is off, so an unset preference must read as disabled —
// that is why this field is not negated.
func (s *Store) VersioningAgentAutonomyEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.VersioningAgentAutonomyEnabled
}

// UpdateVersioningAgentAutonomy grants or revokes an agent's authority to
// rewrite the working tree.
func (s *Store) UpdateVersioningAgentAutonomy(enabled bool) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.VersioningAgentAutonomyEnabled == enabled {
		return nil
	}
	s.data.VersioningAgentAutonomyEnabled = enabled
	return s.saveLocked()
}

// VersioningCheckpointRetentionValue reports how many automatic checkpoints to
// retain, falling back to the default for an unset or out-of-range value.
func (s *Store) VersioningCheckpointRetentionValue() int {
	if s == nil {
		return DefaultVersioningCheckpointRetention
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value := s.data.VersioningCheckpointRetention
	if value <= 0 || value > MaxVersioningCheckpointRetention {
		return DefaultVersioningCheckpointRetention
	}
	return value
}

// UpdateVersioningCheckpointRetention sets how many automatic checkpoints to
// keep. Retention is what bounds the store, so the value may not be zero: use
// the prune action to drop every checkpoint explicitly.
func (s *Store) UpdateVersioningCheckpointRetention(value int) error {
	if s == nil {
		return nil
	}
	if value <= 0 || value > MaxVersioningCheckpointRetention {
		return errors.New("versioning checkpoint retention is out of range")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.VersioningCheckpointRetention == value {
		return nil
	}
	s.data.VersioningCheckpointRetention = value
	return s.saveLocked()
}

// VersioningAutoCheckpointsEnabled reports whether automatic checkpoints are on.
//
// The default is on, so an unset preference must read as enabled. That is why
// the stored field is negated rather than a plain "enabled" boolean.
func (s *Store) VersioningAutoCheckpointsEnabled() bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.data.VersioningAutoCheckpointsDisabled
}

// UpdateVersioningAutoCheckpointsEnabled turns automatic checkpoints on or off.
func (s *Store) UpdateVersioningAutoCheckpointsEnabled(enabled bool) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	disabled := !enabled
	if s.data.VersioningAutoCheckpointsDisabled == disabled {
		return nil
	}
	s.data.VersioningAutoCheckpointsDisabled = disabled
	return s.saveLocked()
}

func (s *Store) NewProjectMetaLocation() string {
	if s == nil {
		return "project"
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data.NewProjectMetaLocation == "home" {
		return "home"
	}
	return "project"
}

func (s *Store) UpdateNewProjectMetaLocation(location string) error {
	if s == nil {
		return nil
	}
	location = strings.TrimSpace(location)
	if location != "project" && location != "home" {
		return errors.New("invalid new project metadata location")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.NewProjectMetaLocation == location {
		return nil
	}
	s.data.NewProjectMetaLocation = location
	return s.saveLocked()
}

func (s *Store) IdleSessionResourceReleaseHours() int {
	if s == nil {
		return DefaultIdleSessionResourceReleaseHours
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data.IdleSessionResourceReleaseHours <= 0 {
		return DefaultIdleSessionResourceReleaseHours
	}
	return s.data.IdleSessionResourceReleaseHours
}

func (s *Store) UpdateIdleSessionResourceReleaseHours(hours int) error {
	if s == nil {
		return nil
	}
	if hours <= 0 || hours > MaxIdleSessionResourceReleaseHours {
		return errors.New("idle session resource release hours are out of range")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.IdleSessionResourceReleaseHours == hours {
		return nil
	}
	s.data.IdleSessionResourceReleaseHours = hours
	return s.saveLocked()
}

type SessionNamingDefaults struct {
	Agent    string `json:"agent,omitempty"`
	Model    string `json:"model,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

type AgentDefaults struct {
	Model               string               `json:"model,omitempty"`
	Effort              string               `json:"effort,omitempty"`
	FastService         string               `json:"fast_service,omitempty"`
	LastConfigSelection *LastConfigSelection `json:"last_config_selection,omitempty"`
}

type LastConfigSelection struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

func NewStore() (*Store, error) {
	configDir, err := config.CurvatureConfigDir()
	if err != nil {
		return nil, err
	}
	return NewStoreAt(filepath.Join(configDir, preferencesFileName))
}

// NewStoreAt opens a preference store backed by an explicit file.
//
// It exists so callers outside this package (notably tests) can exercise
// preference-driven behaviour without touching the real user configuration
// directory.
func NewStoreAt(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("preferences path is required")
	}
	store := &Store{
		path: path,
		data: UserPreferences{Agents: map[string]AgentDefaults{}},
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	var data UserPreferences
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	if data.Agents == nil {
		data.Agents = map[string]AgentDefaults{}
	}
	s.data = data
	return nil
}

func (s *Store) UpdateAgentDefaultsIfChanged(agentName, model, effort, fastService string) (bool, error) {
	if s == nil {
		return false, nil
	}
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return false, nil
	}
	model = strings.TrimSpace(model)
	effort = strings.TrimSpace(effort)
	fastService = strings.TrimSpace(fastService)
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data.Agents == nil {
		s.data.Agents = map[string]AgentDefaults{}
	}
	next := s.data.Agents[agentName]
	if model != "" {
		next.Model = model
	}
	if effort != "" {
		next.Effort = effort
	}
	if fastService != "" {
		next.FastService = fastService
	}
	if s.data.Agents[agentName] == next {
		return false, nil
	}
	s.data.Agents[agentName] = next
	if err := s.saveLocked(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) UpdateAgentLastConfigSelection(agentName string, selection LastConfigSelection) error {
	if s == nil {
		return nil
	}
	agentName = strings.TrimSpace(agentName)
	selection.Type = strings.TrimSpace(selection.Type)
	selection.ID = strings.TrimSpace(selection.ID)
	selection.Name = strings.TrimSpace(selection.Name)
	if agentName == "" || selection.Type == "" || selection.ID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data.Agents == nil {
		s.data.Agents = map[string]AgentDefaults{}
	}
	next := s.data.Agents[agentName]
	if next.LastConfigSelection != nil && *next.LastConfigSelection == selection {
		return nil
	}
	next.LastConfigSelection = &selection
	s.data.Agents[agentName] = next
	return s.saveLocked()
}

func (s *Store) SessionNamingDefaults() SessionNamingDefaults {
	if s == nil {
		return SessionNamingDefaults{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.SessionNaming
}

func (s *Store) UpdateSessionNamingDefaults(agentName, model string, disabled bool) error {
	if s == nil {
		return nil
	}
	next := SessionNamingDefaults{
		Agent:    strings.TrimSpace(agentName),
		Model:    strings.TrimSpace(model),
		Disabled: disabled,
	}
	if next.Agent == "" && !next.Disabled {
		return errors.New("session naming agent is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.SessionNaming == next {
		return nil
	}
	s.data.SessionNaming = next
	return s.saveLocked()
}

func (s *Store) ApplyAgentDefaults(statuses []agent.Status) []agent.Status {
	if s == nil || len(statuses) == 0 {
		return statuses
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.data.Agents) == 0 {
		return statuses
	}
	out := append([]agent.Status(nil), statuses...)
	for i := range out {
		defaults := s.data.Agents[strings.TrimSpace(out[i].Name)]
		if defaults.Model != "" {
			out[i].DefaultModelID = defaults.Model
		}
		if defaults.Effort != "" {
			out[i].DefaultEffort = defaults.Effort
		}
		if defaults.FastService != "" {
			out[i].DefaultFastService = defaults.FastService
		}
		if defaults.LastConfigSelection != nil {
			out[i].LastConfigSelection = *defaults.LastConfigSelection
		}
	}
	return out
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(s.path)
		if retryErr := os.Rename(tmp, s.path); retryErr != nil {
			return err
		}
	}
	return nil
}
