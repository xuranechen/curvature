package config

import (
	"os"
	"path/filepath"
)

// CurvatureConfigDir returns the user-level config directory for Curvature.
// Example: ~/.config/curvature (Linux/macOS), %AppData%/curvature (Windows).
func CurvatureConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "curvature"), nil
}

// CurvatureInstallDir returns the installed shared-data directory for Curvature
// when the executable lives under PREFIX/bin.
func CurvatureInstallDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	prefix := filepath.Dir(filepath.Dir(exe))
	return filepath.Join(prefix, "share", "curvature"), nil
}
