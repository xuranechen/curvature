package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"curvature/backend/internal/apperr"
	rootfs "curvature/backend/internal/fs"
)

func TestRespondErrorIncludesAppErrorFields(t *testing.T) {
	rec := httptest.NewRecorder()
	respondError(rec, http.StatusBadRequest, apperr.Wrap("open", "/private/session.jsonl", os.ErrPermission))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["code"] != apperr.CodePermissionDenied {
		t.Fatalf("code = %v", payload["code"])
	}
	if payload["operation"] != "open" {
		t.Fatalf("operation = %v", payload["operation"])
	}
	if payload["path"] != "/private/session.jsonl" {
		t.Fatalf("path = %v", payload["path"])
	}
	if payload["message"] == "" || payload["detail"] == "" || payload["error"] == "" {
		t.Fatalf("missing expected message fields: %#v", payload)
	}
}

func TestEnsureTaskWorktreeExcluded(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll .git: %v", err)
	}
	if err := ensureTaskWorktreeExcluded(root); err != nil {
		t.Fatalf("ensureTaskWorktreeExcluded: %v", err)
	}
	if err := ensureTaskWorktreeExcluded(root); err != nil {
		t.Fatalf("ensureTaskWorktreeExcluded second call: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("ReadFile exclude: %v", err)
	}
	if got := strings.Count(string(data), "/.worktree/"); got != 1 {
		t.Fatalf("exclude entry count=%d, want 1; content=%q", got, string(data))
	}
}

func TestResolveRelatedWorktreePrefersNestedTaskWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	root := t.TempDir()
	runAPITestGit(t, root, "init")
	runAPITestGit(t, root, "config", "user.email", "test@example.com")
	runAPITestGit(t, root, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("WriteFile README.md: %v", err)
	}
	runAPITestGit(t, root, "add", "README.md")
	runAPITestGit(t, root, "commit", "-m", "initial")
	runAPITestGit(t, root, "checkout", "-b", "task-1")
	runAPITestGit(t, root, "checkout", "-")

	worktreeRoot := filepath.Join(root, ".worktree", "task-1")
	runAPITestGit(t, root, "worktree", "add", worktreeRoot, "task-1")
	if err := os.WriteFile(filepath.Join(worktreeRoot, "test.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile test.json: %v", err)
	}

	match, ok := resolveRelatedWorktree(
		context.Background(),
		rootfs.NewRootInfo("root", "root", root),
		filepath.Join(".worktree", "task-1", "test.json"),
	)
	if !ok {
		t.Fatal("resolveRelatedWorktree returned false")
	}
	if !sameAPITestPath(match.Path, worktreeRoot) {
		t.Fatalf("match.Path = %q, want %q", match.Path, worktreeRoot)
	}
}

func runAPITestGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s returned error: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out)
}

func sameAPITestPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if resolved, err := filepath.EvalSymlinks(left); err == nil {
		left = filepath.Clean(resolved)
	}
	if resolved, err := filepath.EvalSymlinks(right); err == nil {
		right = filepath.Clean(resolved)
	}
	return left == right
}
