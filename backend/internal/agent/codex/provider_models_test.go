package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"curvature/backend/internal/agent/types"
	"curvature/backend/internal/config"
)

func providerModelFixture(t *testing.T) func(string, any) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("APPDATA", base)
	t.Setenv("XDG_CONFIG_HOME", base)
	dir, err := config.CurvatureConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return func(name string, value any) {
		t.Helper()
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func providerSelection(id, kind string) map[string]any {
	return map[string]any{"last_config_selection": map[string]string{"type": kind, "id": id}}
}

func TestSupplementSavedProviderModelsPreservesNativeMetadata(t *testing.T) {
	write := providerModelFixture(t)
	write("preferences.json", map[string]any{"agents": map[string]any{
		"codex":       providerSelection("api-one", "api_provider"),
		"codex-other": providerSelection("api-two", "api_provider"),
	}})
	write("api-providers.json", []any{
		map[string]any{"id": "api-two", "models": []string{"other-account-model"}},
		map[string]any{"id": "api-one", "models": []string{" native ", " glm-test ", "", "glm-test", "qwen-test"}},
	})
	native := []types.ModelInfo{{ID: "native", Name: "Original name", Description: "Original metadata", Hidden: true, SupportEffort: true, Efforts: []string{"high"}}}
	got := supplementSavedProviderModels("codex", native)
	want := append(append([]types.ModelInfo{}, native...), types.ModelInfo{ID: "glm-test", Name: "glm-test"}, types.ModelInfo{ID: "qwen-test", Name: "qwen-test"})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %+v, want %+v", got, want)
	}
	other := supplementSavedProviderModels("codex-other", nil)
	if len(other) != 1 || other[0].ID != "other-account-model" {
		t.Fatalf("wrong agent binding: %+v", other)
	}
	if len(supplementSavedProviderModels("unbound", nil)) != 0 {
		t.Fatal("unbound agent received provider models")
	}
}

func TestSavedProviderModelsReflectSyncAndConfigSwitch(t *testing.T) {
	write := providerModelFixture(t)
	bind := func(id, kind string) {
		write("preferences.json", map[string]any{"agents": map[string]any{"codex": providerSelection(id, kind)}})
	}
	providers := func(models []string) {
		write("api-providers.json", []any{map[string]any{"id": "api-one", "models": models}, map[string]any{"id": "api-two", "models": []string{"second-model"}}})
	}
	check := func(want []types.ModelInfo) {
		t.Helper()
		if got := supplementSavedProviderModels("codex", nil); !reflect.DeepEqual(got, want) {
			t.Fatalf("models = %+v, want %+v", got, want)
		}
	}
	bind("api-one", "api_provider")
	providers([]string{"before-sync"})
	check([]types.ModelInfo{{ID: "before-sync", Name: "before-sync"}})
	providers([]string{"after-sync"})
	check([]types.ModelInfo{{ID: "after-sync", Name: "after-sync"}})
	bind("api-two", "api_provider")
	check([]types.ModelInfo{{ID: "second-model", Name: "second-model"}})
	bind("backup-id", "backup")
	check(nil)
	bind("deleted", "api_provider")
	check(nil)
}

func TestSavedProviderModelsMissingOrCorruptFilesKeepNativeList(t *testing.T) {
	for _, broken := range []string{"preferences.json", "api-providers.json"} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/missing=%t", broken, missing), func(t *testing.T) {
				write := providerModelFixture(t)
				write("preferences.json", map[string]any{"agents": map[string]any{"codex": providerSelection("api-one", "api_provider")}})
				write("api-providers.json", []any{map[string]any{"id": "api-one", "models": []string{"extra"}}})
				dir, _ := config.CurvatureConfigDir()
				path := filepath.Join(dir, broken)
				var err error
				if missing {
					err = os.Remove(path)
				} else {
					err = os.WriteFile(path, []byte("{"), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
				native := []types.ModelInfo{{ID: "native", Name: "Original"}}
				if got := supplementSavedProviderModels("codex", native); !reflect.DeepEqual(got, native) {
					t.Fatalf("native list changed: %+v", got)
				}
			})
		}
	}
}
