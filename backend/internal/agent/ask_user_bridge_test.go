package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	agenttypes "curvature/backend/internal/agent/types"
	"curvature/backend/mcpaskuser"
)

func TestAskUserSpecIsAttachedOnlyWhenTheAgentOptsIn(t *testing.T) {
	allowBridgeVerification(t)
	pool := NewPool(Config{})
	pool.ConfigureMCPAskUserBridge(MCPAskUserBridgeOptions{Addr: "127.0.0.1:7331", Token: "bridge-token"})

	input := agenttypes.OpenSessionInput{
		SessionKey: "session-1",
		AgentName:  "dsh",
		RootPath:   t.TempDir(),
	}

	specs := pool.askUserSpec(Definition{Name: "dsh", Protocol: ProtocolACP, MCPAskUser: true}, input)
	if len(specs) != 1 {
		t.Fatalf("specs = %+v, want exactly one server", specs)
	}
	spec := specs[0]
	if spec.Name != "curvature-ask-user" {
		t.Fatalf("name = %q", spec.Name)
	}
	if !filepath.IsAbs(spec.Command) {
		t.Fatalf("command %q must be absolute: agents reject relative MCP commands", spec.Command)
	}
	if len(spec.Args) != 1 || spec.Args[0] != "mcp-ask-user" {
		t.Fatalf("args = %+v, want [mcp-ask-user]", spec.Args)
	}
	want := map[string]string{
		mcpaskuser.EnvEndpoint: "http://127.0.0.1:7331",
		mcpaskuser.EnvToken:    "bridge-token",
		mcpaskuser.EnvSession:  "session-1",
		mcpaskuser.EnvAgent:    "dsh",
	}
	for key, value := range want {
		if spec.Env[key] != value {
			t.Fatalf("env[%s] = %q, want %q (%+v)", key, spec.Env[key], value, spec.Env)
		}
	}

	if specs := pool.askUserSpec(Definition{Name: "dsh", Protocol: ProtocolACP}, input); specs != nil {
		t.Fatalf("an agent that did not opt in got a server: %+v", specs)
	}

	unconfigured := NewPool(Config{})
	if specs := unconfigured.askUserSpec(Definition{Name: "dsh", MCPAskUser: true}, input); specs != nil {
		t.Fatalf("a pool without a bridge endpoint got a server: %+v", specs)
	}

	// An explicit command wins over this process, which is how the live test
	// points the agent at a real server binary instead of the test binary.
	explicit := NewPool(Config{})
	explicit.ConfigureMCPAskUserBridge(MCPAskUserBridgeOptions{
		Addr:    "127.0.0.1:7331",
		Token:   "bridge-token",
		Command: filepath.Join(t.TempDir(), "curvature-server"),
	})
	specs = explicit.askUserSpec(Definition{Name: "dsh", MCPAskUser: true}, input)
	if len(specs) != 1 || !strings.HasSuffix(specs[0].Command, "curvature-server") {
		t.Fatalf("explicit command not honored: %+v", specs)
	}
}

// allowBridgeVerification replaces the command self test, which spawns a real
// process, with a stub for tests that only care about what gets attached.
func allowBridgeVerification(t *testing.T) {
	t.Helper()
	previous := verifyAskUserCommand
	verifyAskUserCommand = func(context.Context, string) error { return nil }
	t.Cleanup(func() { verifyAskUserCommand = previous })
}

func TestBridgeIsNeverAttachedWhenTheCommandFailsItsSelfTest(t *testing.T) {
	previous := verifyAskUserCommand
	verifyAskUserCommand = func(context.Context, string) error { return errors.New("not an MCP server") }
	t.Cleanup(func() { verifyAskUserCommand = previous })

	pool := NewPool(Config{})
	pool.ConfigureMCPAskUserBridge(MCPAskUserBridgeOptions{Addr: "127.0.0.1:7331", Token: "bridge-token"})

	// Handing an agent an MCP server it cannot start makes session/new fail, so
	// an unverified command must leave the bridge off instead of breaking agent
	// startup.
	specs := pool.askUserSpec(Definition{Name: "dsh", MCPAskUser: true}, agenttypes.OpenSessionInput{
		SessionKey: "dsh-1",
		AgentName:  "dsh",
	})
	if specs != nil {
		t.Fatalf("an unverified command was attached: %+v", specs)
	}
}

func TestInternalAndProbeSessionsNeverGetTheBridge(t *testing.T) {
	allowBridgeVerification(t)
	pool := NewPool(Config{})
	pool.ConfigureMCPAskUserBridge(MCPAskUserBridgeOptions{Addr: "127.0.0.1:7331", Token: "bridge-token"})
	def := Definition{Name: "dsh", MCPAskUser: true}

	cases := map[string]agenttypes.OpenSessionInput{
		"internal": {SessionKey: "dsh-name-1", AgentName: "dsh", Internal: true},
		"probe":    {SessionKey: "dsh-probe-1", AgentName: "dsh", Probe: true},
	}
	for name, input := range cases {
		if specs := pool.askUserSpec(def, input); specs != nil {
			t.Fatalf("%s session got the bridge: %+v", name, specs)
		}
	}
	interactive := agenttypes.OpenSessionInput{SessionKey: "dsh-1", AgentName: "dsh"}
	if specs := pool.askUserSpec(def, interactive); len(specs) != 1 {
		t.Fatalf("an interactive session did not get the bridge: %+v", specs)
	}
}

func TestBridgeKillSwitchDisablesAttachment(t *testing.T) {
	allowBridgeVerification(t)
	t.Setenv(EnvMCPAskUser, "off")
	pool := NewPool(Config{})
	pool.ConfigureMCPAskUserBridge(MCPAskUserBridgeOptions{Addr: "127.0.0.1:7331", Token: "bridge-token"})
	if specs := pool.askUserSpec(Definition{Name: "dsh", MCPAskUser: true}, agenttypes.OpenSessionInput{
		SessionKey: "dsh-1",
		AgentName:  "dsh",
	}); specs != nil {
		t.Fatalf("the kill switch did not disable the bridge: %+v", specs)
	}
}

func TestResolveAskUserCommandHonorsTheEnvironment(t *testing.T) {
	t.Setenv(EnvMCPAskUser, filepath.Join("C:", "custom", "curvature.exe"))
	if got := resolveAskUserCommand(""); !strings.HasSuffix(got, "curvature.exe") || !strings.Contains(got, "custom") {
		t.Fatalf("resolveAskUserCommand = %q, want the environment command", got)
	}
	if got := resolveAskUserCommand("C:\\explicit.exe"); got != "C:\\explicit.exe" {
		t.Fatalf("an explicit command must win: %q", got)
	}
	for _, disabled := range []string{"0", "false", "off", "no", "disabled", "OFF"} {
		t.Setenv(EnvMCPAskUser, disabled)
		if !disabledByEnv() {
			t.Fatalf("%q should disable the bridge", disabled)
		}
	}
}

// TestBridgeSessionKeyIsResolvableByThePool pins the cross-layer invariant the
// bridge depends on: the session key handed to the MCP server process must be
// one the pool can look up verbatim. Callers open sessions with the prefixed
// pool key (<agent>-<session>), so a backend that re-prefixed it would look for
// an agent-prefixed pool key and never find the session.
func TestBridgeSessionKeyIsResolvableByThePool(t *testing.T) {
	allowBridgeVerification(t)
	pool := NewPool(Config{})
	pool.ConfigureMCPAskUserBridge(MCPAskUserBridgeOptions{Addr: "127.0.0.1:7331", Token: "bridge-token"})

	poolSessionKey := "dsh-2f1c" // what usecase.agentPoolSessionKey("2f1c", "dsh") produces
	input := agenttypes.OpenSessionInput{SessionKey: poolSessionKey, AgentName: "dsh"}
	pool.mu.Lock()
	pool.sessions[poolSessionKey] = &sessionEntry{agentName: "dsh", sessionKey: poolSessionKey, session: stubSession{}}
	pool.mu.Unlock()

	specs := pool.askUserSpec(Definition{Name: "dsh", MCPAskUser: true}, input)
	if len(specs) != 1 {
		t.Fatalf("specs = %+v", specs)
	}
	fromEnv := specs[0].Env[mcpaskuser.EnvSession]
	if fromEnv != poolSessionKey {
		t.Fatalf("env session key = %q, want the pool key %q", fromEnv, poolSessionKey)
	}
	if _, ok := pool.Get(fromEnv); !ok {
		t.Fatalf("the pool cannot resolve the session key the bridge was given: %q", fromEnv)
	}
	if _, ok := pool.Get("dsh-" + fromEnv); ok {
		t.Fatal("re-prefixing the bridge key resolved a session; the key contract is ambiguous")
	}
}

// stubSession satisfies agenttypes.Session without an agent process; the pool
// only has to hold it for these assertions.
type stubSession struct{ agenttypes.Session }

func TestLoopbackEndpointNormalizesWildcardHosts(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:7331": "http://127.0.0.1:7331",
		"0.0.0.0:7331":   "http://127.0.0.1:7331",
		"[::]:7331":      "http://127.0.0.1:7331",
		":7331":          "http://127.0.0.1:7331",
		"localhost:9000": "http://localhost:9000",
		"":               "",
		// No readable port: fail closed rather than hand the agent a broken URL.
		"127.0.0.1": "",
		"garbage":   "",
		"host:abc":  "",
	}
	for addr, want := range cases {
		if got := loopbackEndpoint(addr); got != want {
			t.Fatalf("loopbackEndpoint(%q) = %q, want %q", addr, got, want)
		}
	}
}
