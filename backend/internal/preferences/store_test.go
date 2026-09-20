package preferences

import (
	"path/filepath"
	"testing"
)

func TestSessionNamingDefaultsPersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), preferencesFileName)
	store := &Store{
		path: path,
		data: UserPreferences{Agents: map[string]AgentDefaults{}},
	}
	if err := store.UpdateSessionNamingDefaults(" codex ", " gpt-5.4 ", true); err != nil {
		t.Fatalf("UpdateSessionNamingDefaults: %v", err)
	}
	if got := store.SessionNamingDefaults(); got != (SessionNamingDefaults{Agent: "codex", Model: "gpt-5.4", Disabled: true}) {
		t.Fatalf("SessionNamingDefaults = %#v", got)
	}

	reloaded := &Store{
		path: path,
		data: UserPreferences{Agents: map[string]AgentDefaults{}},
	}
	if err := reloaded.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := reloaded.SessionNamingDefaults(); got != (SessionNamingDefaults{Agent: "codex", Model: "gpt-5.4", Disabled: true}) {
		t.Fatalf("reloaded SessionNamingDefaults = %#v", got)
	}
}

func TestSessionNamingDefaultsAllowDefaultModel(t *testing.T) {
	store := &Store{
		path: filepath.Join(t.TempDir(), preferencesFileName),
		data: UserPreferences{Agents: map[string]AgentDefaults{}},
	}
	if err := store.UpdateSessionNamingDefaults("codex", "", false); err != nil {
		t.Fatalf("UpdateSessionNamingDefaults with default model: %v", err)
	}
	if got := store.SessionNamingDefaults(); got != (SessionNamingDefaults{Agent: "codex"}) {
		t.Fatalf("SessionNamingDefaults = %#v", got)
	}
	if err := store.UpdateSessionNamingDefaults("", "gpt-5.4", false); err == nil {
		t.Fatal("UpdateSessionNamingDefaults without agent succeeded")
	}
	if err := store.UpdateSessionNamingDefaults("", "", true); err != nil {
		t.Fatalf("UpdateSessionNamingDefaults disabled without agent: %v", err)
	}
	if got := store.SessionNamingDefaults(); got != (SessionNamingDefaults{Disabled: true}) {
		t.Fatalf("disabled SessionNamingDefaults = %#v", got)
	}
}

func TestIdleSessionResourceReleaseHoursDefaultAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), preferencesFileName)
	store := &Store{path: path, data: UserPreferences{Agents: map[string]AgentDefaults{}}}
	if got := store.IdleSessionResourceReleaseHours(); got != 72 {
		t.Fatalf("default hours = %d, want 72", got)
	}
	if err := store.UpdateIdleSessionResourceReleaseHours(24); err != nil {
		t.Fatalf("UpdateIdleSessionResourceReleaseHours: %v", err)
	}
	reloaded := &Store{path: path, data: UserPreferences{Agents: map[string]AgentDefaults{}}}
	if err := reloaded.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := reloaded.IdleSessionResourceReleaseHours(); got != 24 {
		t.Fatalf("reloaded hours = %d, want 24", got)
	}
	if err := store.UpdateIdleSessionResourceReleaseHours(0); err == nil {
		t.Fatal("zero hours unexpectedly accepted")
	}
}

func TestNewProjectMetaLocationDefaultAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), preferencesFileName)
	store := &Store{path: path, data: UserPreferences{Agents: map[string]AgentDefaults{}}}
	if got := store.NewProjectMetaLocation(); got != "project" {
		t.Fatalf("default location = %q", got)
	}
	if err := store.UpdateNewProjectMetaLocation("home"); err != nil {
		t.Fatal(err)
	}
	reloaded := &Store{path: path, data: UserPreferences{Agents: map[string]AgentDefaults{}}}
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.NewProjectMetaLocation(); got != "home" {
		t.Fatalf("reloaded location = %q", got)
	}
	if err := store.UpdateNewProjectMetaLocation("other"); err == nil {
		t.Fatal("invalid location unexpectedly accepted")
	}
}

// Automatic checkpoints default to on, so an absent preference must read as
// enabled. The field is stored negated precisely to make that work; a plain
// "enabled" boolean would silently default the feature off.
func TestVersioningAutoCheckpointsDefaultOnAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), preferencesFileName)
	store := &Store{path: path, data: UserPreferences{Agents: map[string]AgentDefaults{}}}
	if !store.VersioningAutoCheckpointsEnabled() {
		t.Fatal("automatic checkpoints should default to on")
	}

	if err := store.UpdateVersioningAutoCheckpointsEnabled(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if store.VersioningAutoCheckpointsEnabled() {
		t.Fatal("checkpoints still report enabled after being disabled")
	}

	reloaded := &Store{path: path, data: UserPreferences{Agents: map[string]AgentDefaults{}}}
	if err := reloaded.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if reloaded.VersioningAutoCheckpointsEnabled() {
		t.Fatal("disabled state did not survive a reload")
	}

	if err := reloaded.UpdateVersioningAutoCheckpointsEnabled(true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if !reloaded.VersioningAutoCheckpointsEnabled() {
		t.Fatal("checkpoints not re-enabled")
	}
}

// Agent autonomy is the mirror image of the checkpoint switch: it defaults to
// off, so its field is a plain positive flag. Keeping the two conventions
// straight matters, because negating this one would silently hand agents the
// right to rewrite the working tree.
func TestVersioningAgentAutonomyDefaultsOffAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), preferencesFileName)
	store := &Store{path: path, data: UserPreferences{Agents: map[string]AgentDefaults{}}}
	if store.VersioningAgentAutonomyEnabled() {
		t.Fatal("agent autonomy should default to off")
	}

	if err := store.UpdateVersioningAgentAutonomy(true); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if !store.VersioningAgentAutonomyEnabled() {
		t.Fatal("autonomy not granted")
	}

	reloaded := &Store{path: path, data: UserPreferences{Agents: map[string]AgentDefaults{}}}
	if err := reloaded.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reloaded.VersioningAgentAutonomyEnabled() {
		t.Fatal("granted state did not survive a reload")
	}

	if err := reloaded.UpdateVersioningAgentAutonomy(false); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if reloaded.VersioningAgentAutonomyEnabled() {
		t.Fatal("autonomy not revoked")
	}
}

// An absent store resolves to the zero value, which has to mean "denied".
func TestVersioningAgentAutonomyNilStoreDenies(t *testing.T) {
	var store *Store
	if store.VersioningAgentAutonomyEnabled() {
		t.Fatal("a nil store must deny agent autonomy")
	}
	if err := store.UpdateVersioningAgentAutonomy(true); err != nil {
		t.Fatalf("nil store update should be a no-op, got %v", err)
	}
}

// Retention is what bounds the store, so the value must never resolve to
// something that would disable it, and a corrupt stored value must fall back
// rather than be returned as-is.
func TestVersioningCheckpointRetentionDefaultAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), preferencesFileName)
	store := &Store{path: path, data: UserPreferences{Agents: map[string]AgentDefaults{}}}
	if got := store.VersioningCheckpointRetentionValue(); got != DefaultVersioningCheckpointRetention {
		t.Fatalf("default retention = %d, want %d", got, DefaultVersioningCheckpointRetention)
	}
	if err := store.UpdateVersioningCheckpointRetention(25); err != nil {
		t.Fatalf("update: %v", err)
	}

	reloaded := &Store{path: path, data: UserPreferences{Agents: map[string]AgentDefaults{}}}
	if err := reloaded.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := reloaded.VersioningCheckpointRetentionValue(); got != 25 {
		t.Fatalf("reloaded retention = %d, want 25", got)
	}

	for _, value := range []int{0, -1, MaxVersioningCheckpointRetention + 1} {
		if err := reloaded.UpdateVersioningCheckpointRetention(value); err == nil {
			t.Fatalf("retention %d was accepted", value)
		}
	}

	// A stored value outside the accepted range must not be handed back as the
	// effective retention.
	corrupt := &Store{
		path: path,
		data: UserPreferences{Agents: map[string]AgentDefaults{}, VersioningCheckpointRetention: -5},
	}
	if got := corrupt.VersioningCheckpointRetentionValue(); got != DefaultVersioningCheckpointRetention {
		t.Fatalf("corrupt retention resolved to %d, want the default", got)
	}

	var nilStore *Store
	if got := nilStore.VersioningCheckpointRetentionValue(); got != DefaultVersioningCheckpointRetention {
		t.Fatalf("nil store retention = %d, want the default", got)
	}
}

// Callers that never configured preferences hold a nil store; it must report the
// documented default instead of panicking.
func TestVersioningAutoCheckpointsNilStoreDefaultsOn(t *testing.T) {
	var store *Store
	if !store.VersioningAutoCheckpointsEnabled() {
		t.Fatal("nil store should report automatic checkpoints as on")
	}
	if err := store.UpdateVersioningAutoCheckpointsEnabled(false); err != nil {
		t.Fatalf("nil store update: %v", err)
	}
}
