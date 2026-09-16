package relay

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	configpkg "curvature/backend/internal/config"
)

func getOrCreateDeviceID() (string, error) {
	configDir, err := configpkg.CurvatureConfigDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(configDir, "device.json")
	var payload struct {
		DeviceID string `json:"device_id"`
	}

	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &payload); err != nil {
			return "", err
		}
		if deviceID := strings.TrimSpace(payload.DeviceID); deviceID != "" {
			return deviceID, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}

	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	payload.DeviceID = "md_" + base64.RawURLEncoding.EncodeToString(buf)
	data, err = json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return payload.DeviceID, nil
}
