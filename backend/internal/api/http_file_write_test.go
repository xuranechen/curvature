package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fileWriteResponse struct {
	File struct {
		Path  string `json:"path"`
		Name  string `json:"name"`
		Size  int64  `json:"size"`
		MTime string `json:"mtime"`
	} `json:"file"`
	Error string `json:"error"`
}

func doFileWrite(t *testing.T, env *versioningTestEnv, body map[string]any) (*fileWriteResponse, int) {
	t.Helper()
	rec := env.do(t, http.MethodPut, "/api/file", body)
	var payload fileWriteResponse
	decodeVersioningResponse(t, rec, &payload)
	return &payload, rec.Code
}

func TestFileWriteEndpointSavesContent(t *testing.T) {
	env := newVersioningTestEnv(t)
	env.writeFile(t, "note.txt", "before\n")

	payload, status := doFileWrite(t, env, map[string]any{
		"root":    env.rootID,
		"path":    "note.txt",
		"content": "after\n",
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, payload.Error)
	}
	if payload.File.Path != "note.txt" {
		t.Fatalf("path = %q, want note.txt", payload.File.Path)
	}
	if payload.File.Size != int64(len("after\n")) {
		t.Fatalf("size = %d, want %d", payload.File.Size, len("after\n"))
	}
	if _, err := time.Parse(time.RFC3339Nano, payload.File.MTime); err != nil {
		t.Fatalf("mtime %q is not RFC3339: %v", payload.File.MTime, err)
	}
	if got := env.readFile(t, "note.txt"); got != "after\n" {
		t.Fatalf("content = %q, want %q", got, "after\n")
	}
}

func TestFileWriteEndpointCreatesMissingFile(t *testing.T) {
	env := newVersioningTestEnv(t)

	_, status := doFileWrite(t, env, map[string]any{
		"root":    env.rootID,
		"path":    "docs/new.md",
		"content": "# new\n",
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got := env.readFile(t, "docs/new.md"); got != "# new\n" {
		t.Fatalf("content = %q, want %q", got, "# new\n")
	}
}

func TestFileWriteEndpointRejectsStaleMTime(t *testing.T) {
	env := newVersioningTestEnv(t)
	env.writeFile(t, "note.txt", "first\n")

	first, status := doFileWrite(t, env, map[string]any{
		"root":    env.rootID,
		"path":    "note.txt",
		"content": "second\n",
	})
	if status != http.StatusOK {
		t.Fatalf("seed status = %d, want %d", status, http.StatusOK)
	}

	// The editor read the file at first.MTime; a conditional write with that
	// mtime no longer matches, because the seed write above moved it forward.
	stale, status := doFileWrite(t, env, map[string]any{
		"root":           env.rootID,
		"path":           "note.txt",
		"content":        "third\n",
		"expected_mtime": "2001-01-01T00:00:00Z",
	})
	if status != http.StatusConflict {
		t.Fatalf("stale status = %d, want %d (body %s)", status, http.StatusConflict, stale.Error)
	}
	if got := env.readFile(t, "note.txt"); got != "second\n" {
		t.Fatalf("content after conflict = %q, want %q", got, "second\n")
	}

	// The current mtime still round-trips into a successful save.
	if _, status := doFileWrite(t, env, map[string]any{
		"root":           env.rootID,
		"path":           "note.txt",
		"content":        "third\n",
		"expected_mtime": first.File.MTime,
	}); status != http.StatusOK {
		t.Fatalf("fresh status = %d, want %d", status, http.StatusOK)
	}
	if got := env.readFile(t, "note.txt"); got != "third\n" {
		t.Fatalf("content = %q, want %q", got, "third\n")
	}
}

func TestFileWriteEndpointValidatesTarget(t *testing.T) {
	env := newVersioningTestEnv(t)
	env.writeFile(t, "dir/child.txt", "keep\n")

	if _, status := doFileWrite(t, env, map[string]any{
		"root":    env.rootID,
		"path":    "../outside.txt",
		"content": "nope",
	}); status != http.StatusBadRequest {
		t.Fatalf("escape status = %d, want %d", status, http.StatusBadRequest)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(env.rootPath), "outside.txt")); !os.IsNotExist(err) {
		t.Fatalf("escape wrote outside the root: err = %v", err)
	}

	if _, status := doFileWrite(t, env, map[string]any{
		"root":    env.rootID,
		"path":    "dir",
		"content": "nope",
	}); status != http.StatusBadRequest {
		t.Fatalf("directory status = %d, want %d", status, http.StatusBadRequest)
	}
	if got := env.readFile(t, "dir/child.txt"); got != "keep\n" {
		t.Fatalf("directory write mutated a child: %q", got)
	}

	if _, status := doFileWrite(t, env, map[string]any{
		"root":    "",
		"path":    "note.txt",
		"content": "nope",
	}); status != http.StatusBadRequest {
		t.Fatalf("missing root status = %d, want %d", status, http.StatusBadRequest)
	}

	if _, status := doFileWrite(t, env, map[string]any{
		"root":           env.rootID,
		"path":           "missing.txt",
		"content":        "nope",
		"expected_mtime": "2001-01-01T00:00:00Z",
	}); status != http.StatusConflict {
		t.Fatalf("conditional create status = %d, want %d", status, http.StatusConflict)
	}
	if _, err := os.Stat(filepath.Join(env.rootPath, "missing.txt")); !os.IsNotExist(err) {
		t.Fatalf("conditional write created a missing file: err = %v", err)
	}
}
