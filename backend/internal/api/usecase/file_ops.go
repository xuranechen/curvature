package usecase

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type FileOperationInput struct {
	Root        string `json:"root"`
	Path        string `json:"path"`
	Action      string `json:"action"`
	Name        string `json:"name"`
	Destination string `json:"destination"`
}

var fileOperationMu sync.Mutex

// CreateBlankFile creates exactly the requested name without replacing an
// existing entry.
func (s *Service) CreateBlankFile(rootID, dir, name string) (string, error) {
	if strings.TrimSpace(name) == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return "", errors.New("invalid file name")
	}
	if err := s.ensureRegistry(); err != nil {
		return "", err
	}
	root, err := s.Registry.GetRoot(rootID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	dir, err = root.NormalizePath(dir)
	if err != nil {
		return "", err
	}
	return root.CreateFile(filepath.ToSlash(filepath.Join(dir, name)))
}

// OperateFile renames, moves, or deletes one project entry. Paths cover the
// literal entry name (not a #line navigation target), and only the parent is
// resolved so operating on a symlink affects the link itself.
func (s *Service) OperateFile(in FileOperationInput) error {
	fileOperationMu.Lock()
	defer fileOperationMu.Unlock()
	if err := s.ensureRegistry(); err != nil {
		return err
	}
	root, err := s.Registry.GetRoot(in.Root)
	if err != nil {
		return err
	}
	rel := filepath.Clean(in.Path)
	if filepath.IsAbs(rel) {
		rel, err = filepath.Rel(root.RootPath, rel)
		if err != nil {
			return err
		}
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("path outside root")
	}
	if rel == "." || rel == "" {
		return errors.New("cannot modify project root")
	}
	base, err := root.ResolvePath(".")
	if err != nil {
		return err
	}
	parent := filepath.Join(base, filepath.Dir(rel))
	if strings.HasPrefix(filepath.ToSlash(rel), ".curvature/") {
		parent = filepath.Join(root.MetaDir(), filepath.Dir(strings.TrimPrefix(filepath.ToSlash(rel), ".curvature/")))
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	source := filepath.Join(parent, filepath.Base(rel))
	for _, managed := range s.Registry.ListRoots() {
		managedPath, resolveErr := filepath.EvalSymlinks(managed.RootPath)
		if resolveErr == nil && (managedPath == source || strings.HasPrefix(managedPath, source+string(filepath.Separator))) {
			return errors.New("cannot modify a project root or its ancestor")
		}
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if in.Action == "delete" {
		return os.RemoveAll(source)
	}
	var destination string
	switch in.Action {
	case "rename":
		if strings.TrimSpace(in.Name) == "" || in.Name == "." || in.Name == ".." || strings.ContainsAny(in.Name, "/\\\x00") {
			return errors.New("invalid name")
		}
		destination = filepath.Join(parent, in.Name)
	case "move":
		if !filepath.IsAbs(in.Destination) {
			return errors.New("absolute destination directory required")
		}
		target, err := filepath.EvalSymlinks(in.Destination)
		if err != nil {
			return err
		}
		stat, err := os.Stat(target)
		if err != nil {
			return err
		}
		if !stat.IsDir() {
			return errors.New("destination is not a directory")
		}
		if info.IsDir() && (target == source || strings.HasPrefix(target, source+string(filepath.Separator))) {
			return errors.New("cannot move a directory into itself")
		}
		destination = filepath.Join(target, filepath.Base(source))
	default:
		return errors.New("unsupported file operation")
	}
	if source == destination {
		return errors.New("source and destination are identical")
	}
	if _, err := os.Lstat(destination); err == nil {
		return os.ErrExist
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(source, destination)
}
