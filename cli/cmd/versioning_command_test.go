package main

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// Go's flag package stops at the first positional argument, so a root id written
// before -versioning has to be moved to the end or the flag is never parsed.
func TestNormalizeRootFirstArgsHandlesVersioning(t *testing.T) {
	got := normalizeTaskRootFirstArgs([]string{"root-1", "-versioning", "status"})
	want := []string{"-versioning", "status", "root-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeTaskRootFirstArgs = %v, want %v", got, want)
	}

	// The pre-existing -task behaviour must keep working.
	got = normalizeTaskRootFirstArgs([]string{"root-1", "-task", "12", "-next"})
	want = []string{"-task", "12", "-next", "root-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeTaskRootFirstArgs for -task = %v, want %v", got, want)
	}

	// A leading flag is already parseable and must be left untouched.
	args := []string{"-versioning", "status", "root-1"}
	if got := normalizeTaskRootFirstArgs(args); !reflect.DeepEqual(got, args) {
		t.Fatalf("normalizeTaskRootFirstArgs rewrote an already-valid argv: %v", got)
	}

	// Unrelated arguments must pass through unchanged.
	args = []string{"root-1", "-foreground"}
	if got := normalizeTaskRootFirstArgs(args); !reflect.DeepEqual(got, args) {
		t.Fatalf("normalizeTaskRootFirstArgs rewrote unrelated argv: %v", got)
	}
}

func TestVersioningRequestReadsUseQueryRoot(t *testing.T) {
	for _, action := range []string{"info", "status", "verify"} {
		method, path, body, err := versioningRequest("root-1", versioningCommandRequest{Action: action})
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if method != http.MethodGet {
			t.Fatalf("%s method = %s, want GET", action, method)
		}
		if want := "/api/versioning/" + action + "?root=root-1"; path != want {
			t.Fatalf("%s path = %q, want %q", action, path, want)
		}
		if body != nil {
			t.Fatalf("%s unexpectedly carried a body: %v", action, body)
		}
	}
}

func TestVersioningRequestWritesCarryRootInBody(t *testing.T) {
	method, path, body, err := versioningRequest("root-1", versioningCommandRequest{Action: "snapshot", Message: "before refactor"})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if method != http.MethodPost {
		t.Fatalf("snapshot method = %s, want POST", method)
	}
	if path != "/api/versioning/snapshots" {
		t.Fatalf("snapshot path = %q", path)
	}
	if body["root"] != "root-1" || body["message"] != "before refactor" {
		t.Fatalf("snapshot body = %v", body)
	}

	// An empty message must be omitted so the server applies its own default.
	_, _, body, err = versioningRequest("root-1", versioningCommandRequest{Action: "snapshot", Message: "   "})
	if err != nil {
		t.Fatalf("snapshot without message: %v", err)
	}
	if _, ok := body["message"]; ok {
		t.Fatalf("blank message was forwarded: %v", body)
	}
}

func TestVersioningRequestLogClampsLimit(t *testing.T) {
	_, path, _, err := versioningRequest("root-1", versioningCommandRequest{Action: "log", Limit: 5})
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if !strings.Contains(path, "limit=5") {
		t.Fatalf("log path = %q, want limit=5", path)
	}

	// A zero or negative limit must not reach the server as-is.
	for _, limit := range []int{0, -3} {
		_, path, _, err := versioningRequest("root-1", versioningCommandRequest{Action: "log", Limit: limit})
		if err != nil {
			t.Fatalf("log limit=%d: %v", limit, err)
		}
		if !strings.Contains(path, "limit=20") {
			t.Fatalf("log limit=%d produced %q, want the default of 20", limit, path)
		}
	}
}

func TestVersioningRequestRestoreRequiresRevision(t *testing.T) {
	_, _, _, err := versioningRequest("root-1", versioningCommandRequest{Action: "restore"})
	if err == nil {
		t.Fatal("restore without -rev was accepted")
	}
	if !strings.Contains(err.Error(), "-rev") {
		t.Fatalf("restore error = %q, want it to mention -rev", err)
	}

	_, path, body, err := versioningRequest("root-1", versioningCommandRequest{Action: "restore", Revision: "abc123", Mode: "overwrite"})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if path != "/api/versioning/restore" {
		t.Fatalf("restore path = %q", path)
	}
	if body["revision"] != "abc123" || body["mode"] != "overwrite" {
		t.Fatalf("restore body = %v", body)
	}
	// An omitted mode must be left out so the server keeps its documented
	// default of revert.
	_, _, body, err = versioningRequest("root-1", versioningCommandRequest{Action: "restore", Revision: "abc123"})
	if err != nil {
		t.Fatalf("restore without mode: %v", err)
	}
	if _, ok := body["mode"]; ok {
		t.Fatalf("blank mode was forwarded: %v", body)
	}
}

func TestVersioningRequestRejectsUnknownAction(t *testing.T) {
	_, _, _, err := versioningRequest("root-1", versioningCommandRequest{Action: "rollback"})
	if err == nil {
		t.Fatal("unknown action was accepted")
	}
	// The message must name the accepted actions so the CLI is self-teaching.
	for _, action := range []string{"info", "status", "log", "snapshot", "restore", "gc", "prune", "verify"} {
		if !strings.Contains(err.Error(), action) {
			t.Fatalf("error %q does not mention %q", err, action)
		}
	}
}

func TestVersioningRequestPruneOnlySendsKeepWhenGiven(t *testing.T) {
	// A negative keep leaves the decision to the server default, so the field
	// must be omitted rather than sent as a sentinel value.
	_, path, body, err := versioningRequest("root-1", versioningCommandRequest{Action: "prune", Keep: -1})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if path != "/api/versioning/checkpoints/prune" {
		t.Fatalf("prune path = %q", path)
	}
	if body["root"] != "root-1" {
		t.Fatalf("prune body = %v", body)
	}
	if _, ok := body["keep"]; ok {
		t.Fatalf("an unset keep was forwarded: %v", body)
	}

	// An explicit zero is meaningful: it drops every automatic checkpoint.
	_, _, body, err = versioningRequest("root-1", versioningCommandRequest{Action: "prune", Keep: 0})
	if err != nil {
		t.Fatalf("prune keep=0: %v", err)
	}
	if keep, ok := body["keep"]; !ok || keep != 0 {
		t.Fatalf("explicit keep=0 was not forwarded: %v", body)
	}

	_, _, body, err = versioningRequest("root-1", versioningCommandRequest{Action: "prune", Keep: 50})
	if err != nil {
		t.Fatalf("prune keep=50: %v", err)
	}
	if keep, ok := body["keep"]; !ok || keep != 50 {
		t.Fatalf("keep=50 was not forwarded: %v", body)
	}
}

func TestVersioningCommandRequiresRootID(t *testing.T) {
	err := handleVersioningCommand("127.0.0.1:7331", false, versioningCommandRequest{Action: "status"})
	if err == nil {
		t.Fatal("a blank root id was accepted")
	}
	if !strings.Contains(err.Error(), "root id") {
		t.Fatalf("error = %q, want it to mention the root id", err)
	}
}

// Branch-style version switching has to be reachable from the CLI, not only from
// the HTTP API the web UI talks to.
func TestVersioningRequestSupportsTimelineSwitching(t *testing.T) {
	method, path, body, err := versioningRequest("root-1", versioningCommandRequest{Action: "branches"})
	if err != nil {
		t.Fatalf("branches: %v", err)
	}
	if method != http.MethodGet {
		t.Fatalf("branches method = %s, want GET", method)
	}
	if want := "/api/versioning/branches?root=root-1"; path != want {
		t.Fatalf("branches path = %q, want %q", path, want)
	}
	if body != nil {
		t.Fatalf("branches unexpectedly carried a body: %v", body)
	}

	// Creating a timeline names it and may pin the base revision.
	_, path, body, err = versioningRequest("root-1", versioningCommandRequest{
		Action: "branch", Name: "experiment", Revision: "abc123",
	})
	if err != nil {
		t.Fatalf("branch: %v", err)
	}
	if path != "/api/versioning/branches" {
		t.Fatalf("branch path = %q", path)
	}
	if body["root"] != "root-1" || body["name"] != "experiment" || body["from"] != "abc123" {
		t.Fatalf("branch body = %v", body)
	}

	// Omitting the base lets the server branch from the current head, so the
	// field must be absent rather than empty.
	_, _, body, err = versioningRequest("root-1", versioningCommandRequest{Action: "branch", Name: "experiment"})
	if err != nil {
		t.Fatalf("branch without rev: %v", err)
	}
	if _, present := body["from"]; present {
		t.Fatalf("branch without -rev sent a from field: %v", body)
	}

	// Switching timelines carries the mode through.
	_, path, body, err = versioningRequest("root-1", versioningCommandRequest{
		Action: "checkout", Name: "experiment", Mode: "overwrite",
	})
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if path != "/api/versioning/checkout" {
		t.Fatalf("checkout path = %q", path)
	}
	if body["root"] != "root-1" || body["name"] != "experiment" || body["mode"] != "overwrite" {
		t.Fatalf("checkout body = %v", body)
	}

	// A nameless request is a usage error, not something to send to the server.
	for _, action := range []string{"branch", "checkout"} {
		if _, _, _, err := versioningRequest("root-1", versioningCommandRequest{Action: action}); err == nil {
			t.Fatalf("%s without -name should be rejected", action)
		}
	}
}

// The branch listing decides how much to show per row, so it must not split a
// multi-byte timeline name.
func TestTruncateRunesKeepsMultiByteTextIntact(t *testing.T) {
	if got := truncateRunes("时间线", 8); got != "时间线" {
		t.Fatalf("short text was rewritten: %q", got)
	}
	got := truncateRunes("时间线时间线时间线", 4)
	if got != "时间线时…" {
		t.Fatalf("truncateRunes = %q, want %q", got, "时间线时…")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncateRunes produced invalid UTF-8: %q", got)
	}
}

// The env key and header are literals duplicated from the server side: Go's
// internal rule stops cli/cmd from importing either commandexec or the api
// package, so the spellings are pinned here instead of shared.
func TestAgentMarkerNamesArePinned(t *testing.T) {
	if agentSessionEnvKey != "CURVATURE_AGENT_SESSION" {
		t.Fatalf("agentSessionEnvKey = %q, want %q", agentSessionEnvKey, "CURVATURE_AGENT_SESSION")
	}
	if agentSessionHeader != "X-Curvature-Agent-Session" {
		t.Fatalf("agentSessionHeader = %q, want %q", agentSessionHeader, "X-Curvature-Agent-Session")
	}
}

// The CLI declares itself only when commandexec marked the shell. A blank marker
// has to read as the user's own terminal, not as a session named "".
func TestAgentSessionFromEnv(t *testing.T) {
	t.Setenv(agentSessionEnvKey, "  sess-9  ")
	if got := agentSessionFromEnv(); got != "sess-9" {
		t.Fatalf("agentSessionFromEnv = %q, want %q", got, "sess-9")
	}

	t.Setenv(agentSessionEnvKey, "   ")
	if got := agentSessionFromEnv(); got != "" {
		t.Fatalf("a blank marker must read as a human caller, got %q", got)
	}
}
