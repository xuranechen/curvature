package preferences

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"curvature/backend/internal/agent"
)

func TestModelPinsIncludedInAgentStatusesWithoutOtherDefaults(t *testing.T) {
	store := &Store{path: filepath.Join(t.TempDir(), preferencesFileName)}
	if _, err := store.SetModelPinned("codex", "model-a", true); err != nil {
		t.Fatal(err)
	}
	statuses := []agent.Status{{Name: "codex"}, {Name: "claude"}}
	result := store.ApplyAgentDefaults(statuses)
	if !reflect.DeepEqual(result[0].PinnedModels, []string{"model-a"}) || len(result[1].PinnedModels) != 0 {
		t.Fatalf("statuses = %+v", result)
	}
	result[0].PinnedModels[0] = "mutated"
	if store.PinnedModels("codex")[0] != "model-a" || len(statuses[0].PinnedModels) != 0 {
		t.Fatal("status projection mutated source data")
	}
}

func TestModelPinsPersistenceIsolationAndDefaults(t *testing.T) {
	store := &Store{path: filepath.Join(t.TempDir(), preferencesFileName)}
	if _, err := store.UpdateAgentDefaultsIfChanged("codex", "original", "high", ""); err != nil {
		t.Fatal(err)
	}
	for _, agentName := range []string{"codex", "claude"} {
		for range 2 {
			if _, err := store.SetModelPinned(agentName, "model-a", true); err != nil {
				t.Fatal(err)
			}
		}
	}
	result, err := store.SetModelPinned("codex", "model-b", true)
	if err != nil {
		t.Fatal(err)
	}
	result[0] = "must-not-mutate-store"
	if _, err := store.SetModelPinned("codex", "model-a", false); err != nil {
		t.Fatal(err)
	}
	reloaded := &Store{path: store.path}
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.PinnedModels("codex"), []string{"model-b"}) || !reflect.DeepEqual(reloaded.PinnedModels("claude"), []string{"model-a"}) {
		t.Fatalf("pins = %+v", reloaded.data.AgentPinnedModels)
	}
	if defaults := reloaded.data.Agents["codex"]; defaults.Model != "original" || defaults.Effort != "high" {
		t.Fatalf("defaults changed: %+v", defaults)
	}
	copy := reloaded.PinnedModels("codex")
	copy[0] = "mutated"
	if reloaded.PinnedModels("codex")[0] != "model-b" {
		t.Fatal("getter returned mutable store data")
	}
}

func TestModelPinsConcurrentUpdates(t *testing.T) {
	store := &Store{path: filepath.Join(t.TempDir(), preferencesFileName)}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.SetModelPinned("codex", fmt.Sprintf("model-%d", i), true); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(store.PinnedModels("codex")) != 20 {
		t.Fatal("concurrent pins lost")
	}
}

func TestModelPinsSaveFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	store := &Store{path: filepath.Join(dir, preferencesFileName)}
	if _, err := store.SetModelPinned("codex", "original", true); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store.path = filepath.Join(blocked, preferencesFileName)
	if _, err := store.SetModelPinned("codex", "extra", true); err == nil {
		t.Fatal("expected save failure")
	}
	if !reflect.DeepEqual(store.PinnedModels("codex"), []string{"original"}) {
		t.Fatal("failed save changed pins")
	}
}
func TestLastAgentPersistsWithAgentDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), preferencesFileName)
	store := &Store{path: path}
	for _, name := range []string{"claude", "codex", " claude "} {
		// Switching back must persist even when this agent's model is unchanged.
		changed, err := store.UpdateAgentDefaultsIfChanged(name, " model ", " high ", " on ")
		if err != nil || !changed {
			t.Fatalf("update %q: changed=%v, err=%v", name, changed, err)
		}
	}
	if changed, err := store.UpdateAgentDefaultsIfChanged("claude", "model", "high", "on"); err != nil || changed {
		t.Fatalf("unchanged update: changed=%v, err=%v", changed, err)
	}
	if changed, err := store.UpdateAgentDefaultsIfChanged(" ", "", "", ""); err != nil || changed {
		t.Fatalf("empty agent update: changed=%v, err=%v", changed, err)
	}
	reloaded := &Store{path: path}
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	statuses := []agent.Status{{Name: "codex", Available: true}, {Name: "claude", Available: true}}
	got := reloaded.ApplyAgentDefaults(statuses)
	if got[0].LastUsed || !got[1].LastUsed {
		t.Fatalf("last agent not restored: %#v", got)
	}
	if got[1].DefaultModelID != "model" || got[1].DefaultEffort != "high" || got[1].DefaultFastService != "on" {
		t.Fatalf("agent defaults not restored: %#v", got[1])
	}
	if statuses[1].LastUsed {
		t.Fatal("ApplyAgentDefaults mutated the probe statuses")
	}
}

func TestLastAgentLegacyPreferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), preferencesFileName)
	if err := os.WriteFile(path, []byte(`{"agents":{"claude":{"model":"sonnet"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &Store{path: path}
	if err := store.load(); err != nil {
		t.Fatal(err)
	}
	got := store.ApplyAgentDefaults([]agent.Status{{Name: "claude"}})
	if got[0].LastUsed || got[0].DefaultModelID != "sonnet" {
		t.Fatalf("legacy preferences changed: %#v", got)
	}
	if changed, err := store.UpdateAgentDefaultsIfChanged("claude", "sonnet", "", ""); err != nil || !changed {
		t.Fatalf("first use after upgrade: changed=%v, err=%v", changed, err)
	}
}

func TestLastAgentSaveFailureCanBeRetried(t *testing.T) {
	dir := t.TempDir()
	blockedDir := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blockedDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store := &Store{path: filepath.Join(blockedDir, preferencesFileName)}
	if changed, err := store.UpdateAgentDefaultsIfChanged("claude", "sonnet", "", ""); err == nil || changed {
		t.Fatalf("failed save: changed=%v, err=%v", changed, err)
	}
	store.path = filepath.Join(dir, preferencesFileName)
	if changed, err := store.UpdateAgentDefaultsIfChanged("claude", "sonnet", "", ""); err != nil || !changed {
		t.Fatalf("retry: changed=%v, err=%v", changed, err)
	}
}
