package acp

import (
	"os/exec"
	"strings"
	"testing"
)

// The agent process is where the marker has to originate: it is the only process
// Curvature spawns on the agent's behalf, and every shell the agent runs inherits
// its environment. commandexec strips the marker for the user's integrated
// terminal, so the two sides have to stay complementary -- mark the agent here, or
// strip the user there, but never the other way round.
func TestConfigureProcessCommandMarksAgentProcess(t *testing.T) {
	cmd := exec.Command("echo")
	configureProcessCommand(cmd, map[string]string{"FOO": "bar"})

	if got := markerValue(cmd.Env); got != "1" {
		t.Fatalf("%s = %q, want %q", agentSessionEnvKey, got, "1")
	}
	if got := markerCount(cmd.Env); got != 1 {
		t.Fatalf("found %d assignments of %s, want exactly 1", got, agentSessionEnvKey)
	}
	if got := envEntry(cmd.Env, "FOO"); got != "bar" {
		t.Fatalf("FOO = %q, want bar (the marker must not displace the caller's env)", got)
	}
}

// A caller must not get to choose the marker's value, and an inherited marker must
// not survive as a duplicate: with a duplicate key, which value wins depends on the
// platform.
func TestConfigureProcessCommandReplacesSuppliedMarker(t *testing.T) {
	cmd := exec.Command("echo")
	configureProcessCommand(cmd, map[string]string{agentSessionEnvKey: "caller-supplied"})

	if got := markerCount(cmd.Env); got != 1 {
		t.Fatalf("found %d assignments of %s, want exactly 1", got, agentSessionEnvKey)
	}
	if got := markerValue(cmd.Env); got != "1" {
		t.Fatalf("%s = %q, want %q", agentSessionEnvKey, got, "1")
	}
}

func TestConfigureProcessCommandToleratesNilCommand(t *testing.T) {
	configureProcessCommand(nil, map[string]string{"FOO": "bar"})
}

func markerValue(env []string) string {
	return envEntry(env, agentSessionEnvKey)
}

func markerCount(env []string) int {
	prefix := agentSessionEnvKey + "="
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			count++
		}
	}
	return count
}

func envEntry(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
