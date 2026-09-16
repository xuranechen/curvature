package gitview

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestDecodeGitDiffOutputDecodesGB18030Text(t *testing.T) {
	source := "diff --git a/main.go b/main.go\n@@ -1 +1 @@\n-旧内容\n+新内容\n"
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(source))
	if err != nil {
		t.Fatalf("encode GB18030: %v", err)
	}

	got := decodeGitDiffOutput(encoded, ".go")
	if got != source {
		t.Fatalf("decoded diff = %q, want %q", got, source)
	}
}

func TestReadRelatedFileDiffUsesNextCommitAfterBase(t *testing.T) {
	root := initTestRepo(t)
	writeTestFile(t, root, "note.txt", "before\n")
	runTestGit(t, root, "add", "note.txt")
	runTestGit(t, root, "commit", "-m", "initial")
	base := strings.TrimSpace(runTestGit(t, root, "rev-parse", "HEAD"))

	writeTestFile(t, root, "note.txt", "after\n")
	runTestGit(t, root, "add", "note.txt")
	runTestGit(t, root, "commit", "-m", "update")

	diff, err := ReadRelatedFileDiff(context.Background(), root, base, "note.txt")
	if err != nil {
		t.Fatalf("ReadRelatedFileDiff: %v", err)
	}
	if diff.BaseHead != base {
		t.Fatalf("BaseHead = %q, want %q", diff.BaseHead, base)
	}
	if diff.TargetHead == "" {
		t.Fatal("TargetHead is empty")
	}
	if diff.Source != "commit_range" {
		t.Fatalf("Source = %q, want commit_range", diff.Source)
	}
	if !strings.Contains(diff.Content, "-before") || !strings.Contains(diff.Content, "+after") {
		t.Fatalf("diff content does not contain expected change:\n%s", diff.Content)
	}
}

func TestReadRelatedFileDiffUsesWorktreeRoot(t *testing.T) {
	root := initTestRepo(t)
	writeTestFile(t, root, "note.txt", "base\n")
	runTestGit(t, root, "add", "note.txt")
	runTestGit(t, root, "commit", "-m", "initial")
	runTestGit(t, root, "checkout", "-b", "task-1")
	base := strings.TrimSpace(runTestGit(t, root, "rev-parse", "HEAD"))
	runTestGit(t, root, "checkout", "-")

	worktreeRoot := filepath.Join(root, ".worktree", "task-1")
	runTestGit(t, root, "worktree", "add", worktreeRoot, "task-1")
	writeTestFile(t, root, "note.txt", "main-only\n")
	writeTestFile(t, worktreeRoot, "note.txt", "worktree-only\n")

	diff, err := ReadRelatedFileDiff(context.Background(), worktreeRoot, base, "note.txt")
	if err != nil {
		t.Fatalf("ReadRelatedFileDiff: %v", err)
	}
	if diff.Source != "worktree" {
		t.Fatalf("Source = %q, want worktree", diff.Source)
	}
	if !strings.Contains(diff.Content, "+worktree-only") {
		t.Fatalf("diff content does not contain worktree change:\n%s", diff.Content)
	}
	if strings.Contains(diff.Content, "main-only") {
		t.Fatalf("diff content used main worktree instead of task worktree:\n%s", diff.Content)
	}
}

func TestIsInsideWorktreeDetectsSubdirectories(t *testing.T) {
	root := initTestRepo(t)
	writeTestFile(t, root, "note.txt", "base\n")
	runTestGit(t, root, "add", "note.txt")
	runTestGit(t, root, "commit", "-m", "initial")
	runTestGit(t, root, "checkout", "-b", "task-1")
	runTestGit(t, root, "checkout", "-")

	worktreeRoot := filepath.Join(root, ".worktree", "task-1")
	runTestGit(t, root, "worktree", "add", worktreeRoot, "task-1")
	subdir := filepath.Join(worktreeRoot, "src")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	inside, err := IsInsideWorktree(context.Background(), subdir)
	if err != nil {
		t.Fatalf("IsInsideWorktree: %v", err)
	}
	if !inside {
		t.Fatal("IsInsideWorktree returned false for worktree subdir")
	}

	inside, err = IsInsideWorktree(context.Background(), root)
	if err != nil {
		t.Fatalf("IsInsideWorktree main root: %v", err)
	}
	if inside {
		t.Fatal("IsInsideWorktree returned true for main root")
	}
}

func TestReadRelatedFileDiffRejectsHeadOutsideCurrentHistory(t *testing.T) {
	root := initTestRepo(t)
	writeTestFile(t, root, "note.txt", "main\n")
	runTestGit(t, root, "add", "note.txt")
	runTestGit(t, root, "commit", "-m", "initial")
	mainBranch := strings.TrimSpace(runTestGit(t, root, "branch", "--show-current"))
	runTestGit(t, root, "checkout", "-b", "other")
	writeTestFile(t, root, "note.txt", "other\n")
	runTestGit(t, root, "commit", "-am", "other update")
	otherHead := strings.TrimSpace(runTestGit(t, root, "rev-parse", "HEAD"))
	runTestGit(t, root, "checkout", mainBranch)

	_, err := ReadRelatedFileDiff(context.Background(), root, otherHead, "note.txt")
	if err == nil {
		t.Fatal("ReadRelatedFileDiff succeeded for head outside current history")
	}
	if !strings.Contains(err.Error(), "记录的提交不存在或不在当前分支历史中") {
		t.Fatalf("error = %v", err)
	}
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	root := t.TempDir()
	runTestGit(t, root, "init")
	runTestGit(t, root, "config", "user.email", "test@example.com")
	runTestGit(t, root, "config", "user.name", "Test User")
	return root
}

func writeTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func runTestGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out)
}
