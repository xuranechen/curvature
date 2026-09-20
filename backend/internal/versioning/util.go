package versioning

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrNotInitialized is returned when a store operation needs an existing store.
var ErrNotInitialized = errors.New("versioning: store not initialized")

// ErrBadRevision is returned when a revision expression cannot be resolved.
var ErrBadRevision = errors.New("versioning: unknown revision")

// ErrBadPath is returned when a path escapes the project root.
var ErrBadPath = errors.New("versioning: path outside project root")

const hashHexLen = 64

// validHash reports whether s looks like a full sha256 hex digest.
func validHash(s string) bool {
	if len(s) != hashHexLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// normalizeRelPath converts an arbitrary user supplied path into a clean
// slash-separated project relative path. It rejects absolute paths and any
// path that escapes the root via "..".
func normalizeRelPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "./")
	if p == "" || p == "." {
		return "", nil
	}
	if strings.HasPrefix(p, "/") {
		return "", ErrBadPath
	}
	if len(p) >= 2 && p[1] == ':' {
		return "", ErrBadPath
	}
	cleaned := path.Clean(p)
	if cleaned == "." {
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrBadPath
	}
	return cleaned, nil
}

// resolveInsideRoot joins rel onto root and guarantees the result stays inside
// root. It does not require the target to exist.
func resolveInsideRoot(root, rel string) (string, error) {
	cleanRel, err := normalizeRelPath(rel)
	if err != nil {
		return "", err
	}
	if cleanRel == "" {
		return "", ErrBadPath
	}
	full := filepath.Join(root, filepath.FromSlash(cleanRel))
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	relCheck, err := filepath.Rel(absRoot, absFull)
	if err != nil {
		return "", ErrBadPath
	}
	if relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", ErrBadPath
	}
	return absFull, nil
}

// atomicWriteFile writes data to a temporary file in the destination directory
// and renames it into place, so readers never observe a partial file.
func atomicWriteFile(dest string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// Best-effort cleanup when the rename never happened.
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		// Non-fatal on platforms without full mode support.
		_ = err
	}
	return os.Rename(tmpName, dest)
}

// atomicWriteString is atomicWriteFile for text payloads.
func atomicWriteString(dest, content string) error {
	return atomicWriteFile(dest, []byte(content), 0o644)
}

// readFileIfExists returns (nil, false, nil) when the file is absent.
func readFileIfExists(p string) ([]byte, bool, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

// copyToFile writes r into dest atomically.
func copyToFile(dest string, r io.Reader, perm os.FileMode) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return atomicWriteFile(dest, data, perm)
}

// IsBinaryPayload applies the same heuristic git uses: a NUL byte inside the
// first 8000 bytes means "not text".
func IsBinaryPayload(data []byte) bool {
	limit := len(data)
	if limit > 8000 {
		limit = 8000
	}
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// humanBytes is a small helper used by CLI/UI summaries.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
