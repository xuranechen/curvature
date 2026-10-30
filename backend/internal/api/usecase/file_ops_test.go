package usecase

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	rootfs "curvature/backend/internal/fs"
)

func TestCreateBlankFileRejectsExistingAndTraversal(t *testing.T) {
	rootDir := t.TempDir()
	root := rootfs.RootInfo{ID: "curvature", Name: "curvature", RootPath: rootDir}
	service := Service{Registry: uploadTestRegistry{root: root}}
	if err := os.MkdirAll(filepath.Join(rootDir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	path, err := service.CreateBlankFile(root.ID, "docs", "note.md")
	if err != nil {
		t.Fatalf("CreateBlankFile returned error: %v", err)
	}
	if path != "docs/note.md" {
		t.Fatalf("path = %q, want docs/note.md", path)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "docs", "note.md")); err != nil {
		t.Fatalf("created file missing: %v", err)
	}
	if _, err := service.CreateBlankFile(root.ID, "docs", "note.md"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("duplicate create error = %v, want os.ErrExist", err)
	}
	if _, err := service.CreateBlankFile(root.ID, "docs", "../escape.md"); err == nil {
		t.Fatal("expected invalid name error")
	}
}

func TestOperateFileRenamesMovesAndDeletes(t *testing.T) {
	rootDir := t.TempDir()
	root := rootfs.RootInfo{ID: "curvature", Name: "curvature", RootPath: rootDir}
	service := Service{Registry: uploadTestRegistry{root: root}}
	if err := os.MkdirAll(filepath.Join(rootDir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "docs", "old.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := service.OperateFile(FileOperationInput{Root: root.ID, Path: "docs/old.md", Action: "rename", Name: "new.md"}); err != nil {
		t.Fatalf("rename returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "docs", "new.md")); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}

	moveDir := t.TempDir()
	if err := service.OperateFile(FileOperationInput{Root: root.ID, Path: "docs/new.md", Action: "move", Destination: moveDir}); err != nil {
		t.Fatalf("move returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moveDir, "new.md")); err != nil {
		t.Fatalf("moved file missing: %v", err)
	}

	if err := service.OperateFile(FileOperationInput{Root: root.ID, Path: "docs", Action: "delete"}); err != nil {
		t.Fatalf("delete returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "docs")); !os.IsNotExist(err) {
		t.Fatalf("deleted directory still exists: %v", err)
	}
}

func TestOperateFileRefusesProjectRoot(t *testing.T) {
	rootDir := t.TempDir()
	root := rootfs.RootInfo{ID: "curvature", Name: "curvature", RootPath: rootDir}
	service := Service{Registry: uploadTestRegistry{root: root}}
	if err := service.OperateFile(FileOperationInput{Root: root.ID, Path: ".", Action: "delete"}); err == nil {
		t.Fatal("expected error when deleting project root")
	}
}
