package relay

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	configpkg "curvature/server/internal/config"
)

type Credentials struct {
	Relay        RelayCredentials        `json:"relay"`
	TokenStation TokenStationCredentials `json:"token_station,omitempty"`
}

type RelayCredentials struct {
	DeviceToken string `json:"device_token"`
	NodeID      string `json:"node_id"`
	NodeName    string `json:"node_name,omitempty"`
	Endpoint    string `json:"endpoint"`
}

type TokenStationCredentials struct {
	Token string `json:"token"`
}

type CredentialsStore struct {
	mu       sync.RWMutex
	filePath string
}

func NewCredentialsStore() (*CredentialsStore, error) {
	configDir, err := configpkg.CurvatureConfigDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return nil, err
	}
	return &CredentialsStore{filePath: filepath.Join(configDir, "credentials.json")}, nil
}

func (s *CredentialsStore) relayConfigPath() string {
	dir := filepath.Dir(s.filePath)
	return filepath.Join(dir, "relay-config.json")
}

func (s *CredentialsStore) LoadRelayBase() string {
	payload, err := os.ReadFile(s.relayConfigPath())
	if err != nil {
		return ""
	}
	var cfg struct {
		RelayBaseURL string `json:"relay_base_url"`
	}
	if err := json.Unmarshal(payload, &cfg); err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.RelayBaseURL)
}

// SaveRelayBase persists the configured relay base URL independently from
// binding credentials so that clearing invalid credentials does not wipe the
// user-chosen relay configuration.
func (s *CredentialsStore) SaveRelayBase(relayBaseURL string) error {
	relayBaseURL = strings.TrimSpace(relayBaseURL)
	cfg := struct {
		RelayBaseURL string `json:"relay_base_url"`
	}{RelayBaseURL: strings.TrimSuffix(relayBaseURL, "/")}
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if strings.TrimSpace(relayBaseURL) == "" {
		if err := os.Remove(s.relayConfigPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.WriteFile(s.relayConfigPath(), payload, 0o600); err != nil {
		return err
	}
	return os.Chmod(s.relayConfigPath(), 0o600)
}

func (s *CredentialsStore) Load() (Credentials, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var creds Credentials
	payload, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return creds, nil
		}
		return creds, err
	}
	if err := json.Unmarshal(payload, &creds); err != nil {
		return Credentials{}, err
	}
	return creds, nil
}

func (s *CredentialsStore) Save(creds Credentials) error {
	if strings.TrimSpace(creds.Relay.DeviceToken) == "" || strings.TrimSpace(creds.Relay.Endpoint) == "" {
		return errors.New("relay credentials require device_token and endpoint")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	payload, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.filePath, payload, 0o600); err != nil {
		return err
	}
	return os.Chmod(s.filePath, 0o600)
}

func (s *CredentialsStore) SaveTokenStation(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("token station credentials require token")
	}
	creds, err := s.Load()
	if err != nil {
		return err
	}
	creds.TokenStation.Token = token

	s.mu.Lock()
	defer s.mu.Unlock()

	payload, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.filePath, payload, 0o600); err != nil {
		return err
	}
	return os.Chmod(s.filePath, 0o600)
}

func (s *CredentialsStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.Remove(s.filePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
