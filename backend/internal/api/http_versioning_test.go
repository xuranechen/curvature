package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"curvature/backend/internal/e2ee"
	"curvature/backend/internal/fs"
	"curvature/backend/internal/preferences"
	"curvature/backend/internal/versioning"

	"github.com/go-chi/chi/v5"
)

// versioningTestEnv wires the real router to a real root backed by a temporary
// project, so the versioning endpoints are exercised end to end.
type versioningTestEnv struct {
	handler  http.Handler
	rootID   string
	rootPath string
	// prefs is the store the handlers consult for user preferences, exposed so a
	// test can flip a switch mid-flight.
	prefs *preferences.Store
}

func newVersioningTestEnv(t *testing.T) *versioningTestEnv {
	t.Helper()
	base := t.TempDir()
	rootPath := filepath.Join(base, "project")
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	registry := fs.NewRegistry(filepath.Join(base, "registry.json"))
	// Pin the metadata to the project so the test never writes to the real home
	// directory.
	root, err := registry.UpsertWithMetaLocation(rootPath, fs.MetaLocationProject)
	if err != nil {
		t.Fatalf("register root: %v", err)
	}
	prefs, err := preferences.NewStoreAt(filepath.Join(base, "preferences.json"))
	if err != nil {
		t.Fatalf("preferences.NewStoreAt: %v", err)
	}
	handler := &HTTPHandler{
		AppContext: &AppContext{
			Dirs:  registry,
			E2EE:  e2ee.NewManager(e2ee.Config{Enabled: false, NodeID: "test-node", PairingSecret: "secret"}),
			Prefs: prefs,
		},
		Version: "test",
	}
	return &versioningTestEnv{handler: handler.Routes(), rootID: root.ID, rootPath: rootPath, prefs: prefs}
}

func (e *versioningTestEnv) do(t *testing.T, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return e.doWithHeaders(t, method, target, body, nil)
}

// doWithHeaders issues a request with extra headers, which is how a test plays
// the part of an agent-invoked CLI.
func (e *versioningTestEnv) doWithHeaders(t *testing.T, method, target string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(payload)
	}
	req := httptest.NewRequest(method, target, reader)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func decodeVersioningResponse(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
}

func (e *versioningTestEnv) writeFile(t *testing.T, rel, content string) {
	t.Helper()
	full := filepath.Join(e.rootPath, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func (e *versioningTestEnv) readFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.rootPath, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// versioningRouteList is the contract between the router and the frontend.
var versioningRouteList = []string{
	"GET /api/versioning/info",
	"GET /api/versioning/status",
	"POST /api/versioning/snapshots",
	"GET /api/versioning/log",
	"GET /api/versioning/changes",
	"GET /api/versioning/timeline",
	"GET /api/versioning/file/diff",
	"GET /api/versioning/file",
	"GET /api/versioning/tree",
	"POST /api/versioning/restore",
	"GET /api/versioning/branches",
	"POST /api/versioning/branches",
	"POST /api/versioning/checkout",
	"GET /api/versioning/tags",
	"POST /api/versioning/tags",
	"DELETE /api/versioning/tags",
	"POST /api/versioning/gc",
	"POST /api/versioning/checkpoints/prune",
	"GET /api/versioning/checkpoints",
	"GET /api/versioning/verify",
}

func TestVersioningRoutesAreRegistered(t *testing.T) {
	handler := &HTTPHandler{Version: "test"}
	router, ok := handler.Routes().(chi.Router)
	if !ok {
		t.Fatalf("Routes() did not return a chi router")
	}

	registered := map[string]bool{}
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		registered[method+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}

	for _, want := range versioningRouteList {
		if !registered[want] {
			t.Fatalf("route %q is not registered", want)
		}
	}
	// Every registered versioning route must appear in the contract list, so a
	// new endpoint cannot be added without a frontend-visible name.
	for route := range registered {
		if !strings.Contains(route, "/api/versioning/") {
			continue
		}
		found := false
		for _, want := range versioningRouteList {
			if want == route {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("route %q is registered but missing from versioningRouteList", route)
		}
	}
}

func TestVersioningEndpointsRequireRoot(t *testing.T) {
	env := newVersioningTestEnv(t)

	gets := []string{
		"/api/versioning/info",
		"/api/versioning/status",
		"/api/versioning/log",
		"/api/versioning/changes",
		"/api/versioning/timeline?path=a.txt",
		"/api/versioning/file/diff?path=a.txt",
		"/api/versioning/file?path=a.txt",
		"/api/versioning/tree",
		"/api/versioning/branches",
		"/api/versioning/tags",
		"/api/versioning/verify",
	}
	for _, target := range gets {
		rec := env.do(t, http.MethodGet, target, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("GET %s without root = %d, want 400 (body %s)", target, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "root required") {
			t.Fatalf("GET %s error = %s, want root required", target, rec.Body.String())
		}
	}

	posts := []string{
		"/api/versioning/snapshots",
		"/api/versioning/restore",
		"/api/versioning/branches",
		"/api/versioning/checkout",
		"/api/versioning/tags",
		"/api/versioning/gc",
	}
	for _, target := range posts {
		rec := env.do(t, http.MethodPost, target, map[string]any{})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST %s without root = %d, want 400 (body %s)", target, rec.Code, rec.Body.String())
		}
	}

	rec := env.do(t, http.MethodDelete, "/api/versioning/tags", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("DELETE /api/versioning/tags without root = %d, want 400", rec.Code)
	}
}

func TestVersioningSnapshotStatusDiffAndRestoreOverHTTP(t *testing.T) {
	env := newVersioningTestEnv(t)
	env.writeFile(t, "src/app.go", "package main\n\nvar version = 1\n")
	env.writeFile(t, "README.md", "hello\n")

	// Take the baseline snapshot.
	rec := env.do(t, http.MethodPost, "/api/versioning/snapshots", map[string]any{
		"root":    env.rootID,
		"message": "baseline",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var snapshot struct {
		Commit  versioning.Commit `json:"commit"`
		Created bool              `json:"created"`
	}
	decodeVersioningResponse(t, rec, &snapshot)
	if !snapshot.Created {
		t.Fatalf("first snapshot was skipped: %+v", snapshot)
	}
	if snapshot.Commit.Hash == "" || snapshot.Commit.Manifest == "" {
		t.Fatalf("snapshot commit is incomplete: %+v", snapshot.Commit)
	}
	if snapshot.Commit.Stats.Files != 2 {
		t.Fatalf("Stats.Files = %d, want 2", snapshot.Commit.Stats.Files)
	}
	baseline := snapshot.Commit.Hash

	// A second snapshot with no changes must be skipped.
	rec = env.do(t, http.MethodPost, "/api/versioning/snapshots", map[string]any{"root": env.rootID, "message": "no-op"})
	if rec.Code != http.StatusOK {
		t.Fatalf("repeat snapshot status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var repeat struct {
		Created bool   `json:"created"`
		Reason  string `json:"reason"`
	}
	decodeVersioningResponse(t, rec, &repeat)
	if repeat.Created {
		t.Fatalf("repeat snapshot created a commit")
	}
	if repeat.Reason == "" {
		t.Fatalf("skipped snapshot did not explain why")
	}

	// Clean tree.
	rec = env.do(t, http.MethodGet, "/api/versioning/status?root="+env.rootID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var cleanStatus struct {
		Status versioning.StatusResult `json:"status"`
	}
	decodeVersioningResponse(t, rec, &cleanStatus)
	if !cleanStatus.Status.Initialized || !cleanStatus.Status.Clean {
		t.Fatalf("fresh snapshot left a dirty tree: %+v", cleanStatus.Status)
	}

	// Modify a file and confirm status reports exactly one change.
	env.writeFile(t, "src/app.go", "package main\n\nvar version = 2\n")
	rec = env.do(t, http.MethodGet, "/api/versioning/status?root="+env.rootID, nil)
	var dirtyStatus struct {
		Status versioning.StatusResult `json:"status"`
	}
	decodeVersioningResponse(t, rec, &dirtyStatus)
	if dirtyStatus.Status.Dirty != 1 || len(dirtyStatus.Status.Items) != 1 {
		t.Fatalf("Dirty = %d items = %d, want 1/1", dirtyStatus.Status.Dirty, len(dirtyStatus.Status.Items))
	}
	if dirtyStatus.Status.Items[0].Path != "src/app.go" || dirtyStatus.Status.Items[0].Status != versioning.StatusModified {
		t.Fatalf("status item = %+v, want src/app.go modified", dirtyStatus.Status.Items[0])
	}

	// Diff the working tree against the snapshot.
	rec = env.do(t, http.MethodGet,
		"/api/versioning/file/diff?root="+env.rootID+"&from="+baseline+"&to="+versioning.WorktreeRev+"&path=src/app.go", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("file diff = %d, body = %s", rec.Code, rec.Body.String())
	}
	var diffOut struct {
		Diff versioning.DiffResult `json:"diff"`
	}
	decodeVersioningResponse(t, rec, &diffOut)
	if diffOut.Diff.Additions != 1 || diffOut.Diff.Deletions != 1 {
		t.Fatalf("diff = +%d -%d, want +1 -1", diffOut.Diff.Additions, diffOut.Diff.Deletions)
	}
	for _, want := range []string{"diff --git a/src/app.go b/src/app.go", "@@ -1,3 +1,3 @@", "+var version = 2"} {
		if !strings.Contains(diffOut.Diff.Content, want) {
			t.Fatalf("diff missing %q:\n%s", want, diffOut.Diff.Content)
		}
	}

	// Read a historical revision of the file.
	rec = env.do(t, http.MethodGet, "/api/versioning/file?root="+env.rootID+"&revision="+baseline+"&path=src/app.go", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("file at revision = %d, body = %s", rec.Code, rec.Body.String())
	}
	var fileOut struct {
		Present bool   `json:"present"`
		Content string `json:"content"`
	}
	decodeVersioningResponse(t, rec, &fileOut)
	if !fileOut.Present || !strings.Contains(fileOut.Content, "var version = 1") {
		t.Fatalf("historical content = %+v, want version 1", fileOut)
	}

	// Restore the snapshot and confirm the working tree is rewound.
	rec = env.do(t, http.MethodPost, "/api/versioning/restore", map[string]any{
		"root":     env.rootID,
		"revision": baseline,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("restore = %d, body = %s", rec.Code, rec.Body.String())
	}
	var restoreOut struct {
		Result versioning.RestoreResult `json:"result"`
	}
	decodeVersioningResponse(t, rec, &restoreOut)
	if restoreOut.Result.SafetyCommit == "" {
		t.Fatalf("restore did not record a safety snapshot")
	}
	if got := env.readFile(t, "src/app.go"); !strings.Contains(got, "var version = 1") {
		t.Fatalf("after restore src/app.go = %q, want version 1", got)
	}

	// History still contains the baseline, plus the safety snapshot.
	rec = env.do(t, http.MethodGet, "/api/versioning/log?root="+env.rootID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("log = %d, body = %s", rec.Code, rec.Body.String())
	}
	var logOut struct {
		Commits []versioning.Commit `json:"commits"`
	}
	decodeVersioningResponse(t, rec, &logOut)
	if len(logOut.Commits) < 2 {
		t.Fatalf("log has %d commits, want at least 2 (baseline + safety)", len(logOut.Commits))
	}

	// Info reports an initialised store.
	rec = env.do(t, http.MethodGet, "/api/versioning/info?root="+env.rootID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("info = %d, body = %s", rec.Code, rec.Body.String())
	}
	var infoOut struct {
		Info versioning.RepoInfo `json:"info"`
	}
	decodeVersioningResponse(t, rec, &infoOut)
	if !infoOut.Info.Initialized {
		t.Fatalf("info reports an uninitialised store")
	}
	if infoOut.Info.Commits < 2 || infoOut.Info.Branch == "" {
		t.Fatalf("info = %+v, want >=2 commits and a timeline name", infoOut.Info)
	}
}

func TestVersioningRejectsTraversalAndUnknownRevision(t *testing.T) {
	env := newVersioningTestEnv(t)
	env.writeFile(t, "a.txt", "a\n")
	rec := env.do(t, http.MethodPost, "/api/versioning/snapshots", map[string]any{"root": env.rootID, "message": "baseline"})
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodGet, "/api/versioning/file?root="+env.rootID+"&path="+urlEscape("../../etc/passwd"), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("traversal read = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodPost, "/api/versioning/restore", map[string]any{
		"root":     env.rootID,
		"revision": "does-not-exist",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("restore of an unknown revision = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodPost, "/api/versioning/checkout", map[string]any{
		"root": env.rootID,
		"name": "no-such-timeline",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("checkout of an unknown timeline = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func urlEscape(value string) string {
	replacer := strings.NewReplacer("/", "%2F", ".", "%2E")
	return replacer.Replace(value)
}

// The UI and the CLI switch timelines through these two endpoints, so the
// success path needs coverage: the sibling test only asserts the 400 for an
// unknown name, which would still pass if creation or checkout were broken.
func TestVersioningTimelineSwitchingOverHTTP(t *testing.T) {
	env := newVersioningTestEnv(t)
	store := versioning.Open(env.rootPath, filepath.Join(env.rootPath, ".curvature"))

	if _, err := store.Snapshot(versioning.SnapshotOptions{Message: "baseline"}); err != nil {
		t.Fatalf("baseline snapshot: %v", err)
	}

	rec := env.do(t, http.MethodPost, "/api/versioning/branches", map[string]any{
		"root": env.rootID,
		"name": "experiment",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create timeline = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodPost, "/api/versioning/checkout", map[string]any{
		"root": env.rootID,
		"name": "experiment",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("checkout timeline = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodGet, "/api/versioning/branches?root="+env.rootID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list timelines = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var listed struct {
		Branches []struct {
			Name    string `json:"name"`
			Current bool   `json:"current"`
		} `json:"branches"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode branches: %v (body %s)", err, rec.Body.String())
	}
	current := ""
	for _, branch := range listed.Branches {
		if branch.Current {
			current = branch.Name
		}
	}
	if current != "experiment" {
		t.Fatalf("current timeline = %q, want %q (body %s)", current, "experiment", rec.Body.String())
	}

	// Timeline names become path components, so the server restricts them to
	// ASCII. The UI has to say so up front rather than let the request fail.
	rec = env.do(t, http.MethodPost, "/api/versioning/branches", map[string]any{
		"root": env.rootID,
		"name": "时间线",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-ASCII timeline name = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

// D11: an agent may read history and take snapshots, but rewriting the working
// tree stays a human decision until the user opts in. The marker is what the CLI
// sends once commandexec has put CURVATURE_AGENT_SESSION in its environment.
func TestAgentWorkingTreeRewritesAreGatedByPreference(t *testing.T) {
	env := newVersioningTestEnv(t)
	store := versioning.Open(env.rootPath, filepath.Join(env.rootPath, ".curvature"))

	baseline, err := store.Snapshot(versioning.SnapshotOptions{Message: "baseline"})
	if err != nil {
		t.Fatalf("baseline snapshot: %v", err)
	}
	env.writeFile(t, "a.txt", "changed\n")

	target := map[string]any{
		"root":     env.rootID,
		"revision": baseline.Commit.Hash,
		"mode":     versioning.RestoreModeOverwrite,
	}
	asAgent := map[string]string{agentSessionHeader: "sess-test"}

	// The default is off, so an agent is refused before anything is touched.
	rec := env.doWithHeaders(t, http.MethodPost, "/api/versioning/restore", target, asAgent)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("agent restore with autonomy off = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}

	// A human caller is never gated: the header is absent for the user's own
	// terminal, which after all shares the same CLI token.
	rec = env.do(t, http.MethodPost, "/api/versioning/restore", target)
	if rec.Code != http.StatusOK {
		t.Fatalf("human restore = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// Opting in lets the agent through.
	if err := env.prefs.UpdateVersioningAgentAutonomy(true); err != nil {
		t.Fatalf("UpdateVersioningAgentAutonomy(true): %v", err)
	}
	env.writeFile(t, "a.txt", "changed again\n")
	rec = env.doWithHeaders(t, http.MethodPost, "/api/versioning/restore", target, asAgent)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent restore with autonomy on = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// Revoking it again closes the door, and checkout is gated the same way
	// because it rewrites the tree too.
	if err := env.prefs.UpdateVersioningAgentAutonomy(false); err != nil {
		t.Fatalf("UpdateVersioningAgentAutonomy(false): %v", err)
	}
	rec = env.doWithHeaders(t, http.MethodPost, "/api/versioning/checkout", map[string]any{
		"root": env.rootID,
		"name": "main",
	}, asAgent)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("agent checkout with autonomy off = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}

	// Reading history and taking snapshots stay open, which is the whole point of
	// granting an agent partial access.
	rec = env.doWithHeaders(t, http.MethodGet, "/api/versioning/log?root="+env.rootID, nil, asAgent)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent log = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	rec = env.doWithHeaders(t, http.MethodPost, "/api/versioning/snapshots", map[string]any{
		"root":    env.rootID,
		"message": "agent checkpoint",
	}, asAgent)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent snapshot = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// The prune endpoint has to tell "keep the default" apart from an explicit zero,
// because dropping every automatic checkpoint is a legitimate request. That
// distinction is carried by a pointer, so it is worth covering over HTTP.
func TestVersioningPruneCheckpointsOverHTTP(t *testing.T) {
	env := newVersioningTestEnv(t)
	// Metadata is pinned to the project, so this resolves to the same cached
	// store the handler uses.
	store := versioning.Open(env.rootPath, filepath.Join(env.rootPath, ".curvature"))

	if _, err := store.Snapshot(versioning.SnapshotOptions{Message: "baseline"}); err != nil {
		t.Fatalf("baseline snapshot: %v", err)
	}
	for _, content := range []string{"c1", "c2", "c3"} {
		if err := os.WriteFile(filepath.Join(env.rootPath, "agent.txt"), []byte(content), 0o644); err != nil {
			t.Fatalf("write agent.txt: %v", err)
		}
		result, err := store.Snapshot(versioning.SnapshotOptions{Message: "auto checkpoint", Checkpoint: true})
		if err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		if !result.Created {
			t.Fatalf("checkpoint was skipped: %s", result.Reason)
		}
	}
	if store.CheckpointCount() != 3 {
		t.Fatalf("CheckpointCount = %d, want 3", store.CheckpointCount())
	}

	// Checkpoints must be listable on their own: they are kept out of Log.
	rec := env.do(t, http.MethodGet, "/api/versioning/checkpoints?root="+env.rootID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list checkpoints = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var listed struct {
		Commits []struct {
			Hash    string   `json:"hash"`
			Parents []string `json:"parents"`
			Origin  *struct {
				Kind string `json:"kind"`
			} `json:"origin"`
		} `json:"commits"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode checkpoints: %v (body %s)", err, rec.Body.String())
	}
	if len(listed.Commits) != 3 || listed.Total != 3 {
		t.Fatalf("listed %d checkpoints (total %d), want 3 and 3", len(listed.Commits), listed.Total)
	}
	for _, commit := range listed.Commits {
		if len(commit.Parents) != 0 {
			t.Fatalf("checkpoint %s has parents %v, want none", commit.Hash, commit.Parents)
		}
		if commit.Origin == nil || commit.Origin.Kind != "auto" {
			t.Fatalf("checkpoint %s origin = %+v, want kind auto", commit.Hash, commit.Origin)
		}
	}

	// And the user's own timeline must not be polluted by them.
	logRec := env.do(t, http.MethodGet, "/api/versioning/log?root="+env.rootID, nil)
	if logRec.Code != http.StatusOK {
		t.Fatalf("log = %d, want 200", logRec.Code)
	}
	var logged struct {
		Commits []struct {
			Hash string `json:"hash"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(logRec.Body.Bytes(), &logged); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if len(logged.Commits) != 1 {
		t.Fatalf("user log has %d commits, want just the baseline", len(logged.Commits))
	}

	rec = env.do(t, http.MethodPost, "/api/versioning/checkpoints/prune", map[string]any{"root": env.rootID, "keep": 2})
	if rec.Code != http.StatusOK {
		t.Fatalf("prune keep=2 = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if store.CheckpointCount() != 2 {
		t.Fatalf("CheckpointCount = %d after prune, want 2", store.CheckpointCount())
	}

	var pruned struct {
		Result struct {
			Kept    int `json:"kept"`
			Dropped int `json:"dropped"`
		} `json:"result"`
		GC struct {
			Removed    int   `json:"removed"`
			BytesFreed int64 `json:"bytes_freed"`
		} `json:"gc"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pruned); err != nil {
		t.Fatalf("decode prune response: %v (body %s)", err, rec.Body.String())
	}
	if pruned.Result.Dropped != 1 || pruned.Result.Kept != 2 {
		t.Fatalf("prune reported %+v, want 1 dropped and 2 kept", pruned.Result)
	}
	// An explicit prune must actually reclaim the orphaned objects. Under the
	// default one-hour grace period it would free nothing and only drop refs.
	if pruned.GC.Removed == 0 {
		t.Fatalf("prune reclaimed nothing; the grace period was not bypassed (body %s)", rec.Body.String())
	}
	if pruned.GC.BytesFreed <= 0 {
		t.Fatalf("prune reported %d bytes freed, want a positive amount", pruned.GC.BytesFreed)
	}

	// Omitting keep falls back to the server default, which must retain more
	// than the two present.
	rec = env.do(t, http.MethodPost, "/api/versioning/checkpoints/prune", map[string]any{"root": env.rootID})
	if rec.Code != http.StatusOK {
		t.Fatalf("default prune = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if store.CheckpointCount() != 2 {
		t.Fatalf("the default retention dropped checkpoints, leaving %d", store.CheckpointCount())
	}

	// An explicit zero must drop everything, not select the default.
	rec = env.do(t, http.MethodPost, "/api/versioning/checkpoints/prune", map[string]any{"root": env.rootID, "keep": 0})
	if rec.Code != http.StatusOK {
		t.Fatalf("prune keep=0 = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if store.CheckpointCount() != 0 {
		t.Fatalf("prune keep=0 left %d checkpoints", store.CheckpointCount())
	}

	rec = env.do(t, http.MethodPost, "/api/versioning/checkpoints/prune", map[string]any{"root": env.rootID, "keep": -1})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative keep = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}
