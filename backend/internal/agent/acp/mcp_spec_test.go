package acp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPServerSpecConvertsToTheStdioWireShape(t *testing.T) {
	spec := MCPServerSpec{
		Name:    "curvature-ask-user",
		Command: `C:\Program Files\Curvature\curvature-server.exe`,
		Args:    []string{"mcp-ask-user"},
		Env:     map[string]string{"B": "2", "A": "1"},
	}
	wire := spec.toSDK()
	if wire.Stdio == nil {
		t.Fatal("spec did not produce a stdio server")
	}
	if wire.Stdio.Command != spec.Command || len(wire.Stdio.Args) != 1 || wire.Stdio.Args[0] != "mcp-ask-user" {
		t.Fatalf("stdio = %+v", wire.Stdio)
	}
	if len(wire.Stdio.Env) != 2 {
		t.Fatalf("env = %+v", wire.Stdio.Env)
	}
	// Deterministic order keeps logs and golden payloads stable.
	if wire.Stdio.Env[0].Name != "A" || wire.Stdio.Env[1].Name != "B" {
		t.Fatalf("env order = %+v", wire.Stdio.Env)
	}
	if wire.Stdio.Env[0].Value != "1" {
		t.Fatalf("env values = %+v", wire.Stdio.Env)
	}
}

func TestMCPServerSpecMarshalsAsAStdioServer(t *testing.T) {
	spec := MCPServerSpec{Name: "curvature-ask-user", Command: "/usr/local/bin/curvature-server", Args: []string{"mcp-ask-user"}}
	raw, err := json.Marshal(spec.toSDK())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	encoded := string(raw)
	for _, want := range []string{`"name":"curvature-ask-user"`, `"command":"/usr/local/bin/curvature-server"`, `"args":["mcp-ask-user"]`} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("wire payload %s is missing %s", encoded, want)
		}
	}
	// stdio is the default variant, so it carries no `type` discriminant (unlike
	// http/sse/acp). dsh accepts this shape; verified live.
	if strings.Contains(encoded, `"type"`) {
		t.Fatalf("stdio payload unexpectedly carries a type discriminant: %s", encoded)
	}
}

func TestMCPServerListIsNeverNil(t *testing.T) {
	list := mcpServerList(nil)
	if list == nil || len(list) != 0 {
		t.Fatalf("list = %#v, want an empty non-nil slice", list)
	}
	list = mcpServerList([]MCPServerSpec{{Name: "a", Command: "/bin/true"}})
	if len(list) != 1 || list[0].Stdio == nil {
		t.Fatalf("list = %+v", list)
	}
}

// TestMCPServersArePerSession pins the bug where a process-wide list let the
// first session opened on a shared agent process decide what every later session
// advertised: an internal title-generation session (no bridge) opened first, and
// the interactive session then saw no ask_user_question tool at all.
func TestMCPServersArePerSession(t *testing.T) {
	internal := mcpServerList(nil)
	interactive := mcpServerList([]MCPServerSpec{{
		Name:    "curvature-ask-user",
		Command: "/usr/local/bin/curvature-server",
		Args:    []string{"mcp-ask-user"},
	}})
	if len(internal) != 0 {
		t.Fatalf("an internal session advertised servers: %+v", internal)
	}
	if len(interactive) != 1 {
		t.Fatalf("the interactive session lost its server: %+v", interactive)
	}
}
