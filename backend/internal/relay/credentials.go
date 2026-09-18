package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	configpkg "curvature/backend/internal/config"
)

type Credentials struct {
	Relay RelayCredentials `json:"relay"`
}

type RelayCredentials struct {
	DeviceToken    string `json:"device_token"`
	NodeID         string `json:"node_id"`
	NodeName       string `json:"node_name,omitempty"`
	Endpoint       string `json:"endpoint"`
	AccessPassword string `json:"access_password,omitempty"`
}

// onDiskRelayConfig is the unified schema persisted in relay.json. It merges the
// former relay-config.json (base URL + node name), credentials.json (device
// binding) and relay-services.json (exposed local services) into a single file.
// relay_base_url is the single source of truth for the relay base URL and
// node_name is the single source of truth for the node display name.
type onDiskRelayConfig struct {
	RelayBaseURL string                  `json:"relay_base_url,omitempty"`
	NodeName     string                  `json:"node_name,omitempty"`
	Credentials  *onDiskRelayCredentials `json:"credentials,omitempty"`
	Services     []LocalService          `json:"services,omitempty"`
}

type onDiskRelayCredentials struct {
	DeviceToken    string `json:"device_token"`
	NodeID         string `json:"node_id,omitempty"`
	Endpoint       string `json:"endpoint"`
	AccessPassword string `json:"access_password,omitempty"`
}

func (c onDiskRelayConfig) empty() bool {
	return strings.TrimSpace(c.RelayBaseURL) == "" &&
		strings.TrimSpace(c.NodeName) == "" &&
		c.Credentials == nil &&
		len(c.Services) == 0
}

// RelayStore owns the unified client-side relay.json. It replaces the previous
// split CredentialsStore (credentials.json) and ServiceStore
// (relay-services.json) files. All reads and writes go through this single
// store so the relay base URL, node name, binding credentials and exposed
// services stay consistent and can be watched as one file.
type RelayStore struct {
	mu       sync.Mutex
	filePath string
}

func NewRelayStore() (*RelayStore, error) {
	configDir, err := configpkg.CurvatureConfigDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return nil, err
	}
	return &RelayStore{filePath: filepath.Join(configDir, "relay.json")}, nil
}

// Path returns the absolute path of the unified relay config file.
func (s *RelayStore) Path() string {
	if s == nil {
		return ""
	}
	return s.filePath
}

func (s *RelayStore) loadLocked() (onDiskRelayConfig, error) {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return s.migrateLegacyLocked()
		}
		return onDiskRelayConfig{}, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return onDiskRelayConfig{}, nil
	}
	var cfg onDiskRelayConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return onDiskRelayConfig{}, err
	}
	return cfg, nil
}

// migrateLegacyLocked upgrades the pre-unification split files into relay.json
// the first time the unified file is missing. Legacy files are removed only
// after the unified file has been written successfully.
func (s *RelayStore) migrateLegacyLocked() (onDiskRelayConfig, error) {
	dir := filepath.Dir(s.filePath)
	var cfg onDiskRelayConfig

	if data, err := os.ReadFile(filepath.Join(dir, "relay-config.json")); err == nil {
		var legacy struct {
			RelayBaseURL string `json:"relay_base_url"`
			NodeName     string `json:"node_name"`
		}
		if json.Unmarshal(data, &legacy) == nil {
			cfg.RelayBaseURL = strings.TrimSuffix(strings.TrimSpace(legacy.RelayBaseURL), "/")
			cfg.NodeName = strings.TrimSpace(legacy.NodeName)
		}
	}

	if data, err := os.ReadFile(filepath.Join(dir, "credentials.json")); err == nil {
		var legacy Credentials
		if json.Unmarshal(data, &legacy) == nil {
			if legacy.Relay.DeviceToken != "" || legacy.Relay.Endpoint != "" {
				cfg.Credentials = &onDiskRelayCredentials{
					DeviceToken:    legacy.Relay.DeviceToken,
					NodeID:         legacy.Relay.NodeID,
					Endpoint:       legacy.Relay.Endpoint,
					AccessPassword: legacy.Relay.AccessPassword,
				}
			}
			if cfg.NodeName == "" {
				cfg.NodeName = strings.TrimSpace(legacy.Relay.NodeName)
			}
		}
	}

	if data, err := os.ReadFile(filepath.Join(dir, "relay-services.json")); err == nil {
		var legacy struct {
			Services []LocalService `json:"services"`
		}
		if json.Unmarshal(data, &legacy) == nil {
			cfg.Services = legacy.Services
		}
	}

	if cfg.empty() {
		return onDiskRelayConfig{}, nil
	}
	if err := s.writeLocked(cfg); err != nil {
		return onDiskRelayConfig{}, err
	}
	for _, name := range []string{"relay-config.json", "credentials.json", "relay-services.json"} {
		_ = os.Remove(filepath.Join(dir, name))
	}
	return cfg, nil
}

func (s *RelayStore) writeLocked(cfg onDiskRelayConfig) error {
	if cfg.empty() {
		if err := os.Remove(s.filePath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.filePath); err != nil {
		return err
	}
	return os.Chmod(s.filePath, 0o600)
}

// Snapshot returns the parsed unified config together with a stable hash of its
// canonical JSON, used by the config watcher to tell external edits apart from
// the file's own writes.
func (s *RelayStore) Snapshot() (onDiskRelayConfig, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.loadLocked()
	if err != nil {
		return onDiskRelayConfig{}, "", err
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		return onDiskRelayConfig{}, "", err
	}
	sum := sha256.Sum256(payload)
	return cfg, hex.EncodeToString(sum[:]), nil
}

func (s *RelayStore) LoadRelayBase() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.loadLocked()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.RelayBaseURL)
}

// SaveRelayBase persists the configured relay base URL independently from the
// binding credentials so clearing invalid credentials does not wipe the
// user-chosen relay configuration.
func (s *RelayStore) SaveRelayBase(relayBaseURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.loadLocked()
	if err != nil {
		return err
	}
	if strings.TrimSpace(relayBaseURL) == "" {
		cfg.RelayBaseURL = ""
	} else {
		cfg.RelayBaseURL = strings.TrimSuffix(strings.TrimSpace(relayBaseURL), "/")
	}
	return s.writeLocked(cfg)
}

// LoadNodeName returns the locally persisted node display name (set before or
// after binding). An empty string means the default hostname should be used.
func (s *RelayStore) LoadNodeName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.loadLocked()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.NodeName)
}

// SaveNodeName persists the node display name independently from binding
// credentials so it survives unbinding and is used for the next bind poll.
func (s *RelayStore) SaveNodeName(nodeName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.loadLocked()
	if err != nil {
		return err
	}
	cfg.NodeName = strings.TrimSpace(nodeName)
	return s.writeLocked(cfg)
}

func (s *RelayStore) Load() (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.loadLocked()
	if err != nil {
		return Credentials{}, err
	}
	var creds Credentials
	if cfg.Credentials != nil {
		creds.Relay = RelayCredentials{
			DeviceToken:    cfg.Credentials.DeviceToken,
			NodeID:         cfg.Credentials.NodeID,
			Endpoint:       cfg.Credentials.Endpoint,
			AccessPassword: cfg.Credentials.AccessPassword,
		}
	}
	creds.Relay.NodeName = strings.TrimSpace(cfg.NodeName)
	return creds, nil
}

func (s *RelayStore) Save(creds Credentials) error {
	if strings.TrimSpace(creds.Relay.DeviceToken) == "" || strings.TrimSpace(creds.Relay.Endpoint) == "" {
		return errors.New("relay credentials require device_token and endpoint")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.loadLocked()
	if err != nil {
		return err
	}
	if name := strings.TrimSpace(creds.Relay.NodeName); name != "" {
		cfg.NodeName = name
	}
	cfg.Credentials = &onDiskRelayCredentials{
		DeviceToken:    strings.TrimSpace(creds.Relay.DeviceToken),
		NodeID:         strings.TrimSpace(creds.Relay.NodeID),
		Endpoint:       strings.TrimSpace(creds.Relay.Endpoint),
		AccessPassword: strings.TrimSpace(creds.Relay.AccessPassword),
	}
	return s.writeLocked(cfg)
}

// Clear removes only the binding credentials. The relay base URL, node name and
// exposed services survive so a rebind does not require reconfiguration.
func (s *RelayStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.loadLocked()
	if err != nil {
		return err
	}
	cfg.Credentials = nil
	return s.writeLocked(cfg)
}

func (s *RelayStore) ListServices() ([]LocalService, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	out := make([]LocalService, 0, len(cfg.Services))
	for _, service := range cfg.Services {
		normalized, err := NormalizeLocalService(service)
		if err == nil {
			out = append(out, normalized)
		}
	}
	return out, nil
}

func (s *RelayStore) GetService(slug string) (LocalService, bool, error) {
	slug = NormalizeServiceSlug(slug)
	services, err := s.ListServices()
	if err != nil {
		return LocalService{}, false, err
	}
	for _, service := range services {
		if service.Slug == slug {
			return service, true, nil
		}
	}
	return LocalService{}, false, nil
}

func (s *RelayStore) SaveService(service LocalService) error {
	normalized, err := NormalizeLocalService(service)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.loadLocked()
	if err != nil {
		return err
	}
	replaced := false
	for i := range cfg.Services {
		if NormalizeServiceSlug(cfg.Services[i].Slug) == normalized.Slug {
			cfg.Services[i] = normalized
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.Services = append(cfg.Services, normalized)
	}
	return s.writeLocked(cfg)
}

func (s *RelayStore) DeleteService(slug string) error {
	slug = NormalizeServiceSlug(slug)
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.loadLocked()
	if err != nil {
		return err
	}
	next := cfg.Services[:0]
	for _, service := range cfg.Services {
		if NormalizeServiceSlug(service.Slug) != slug {
			next = append(next, service)
		}
	}
	cfg.Services = next
	return s.writeLocked(cfg)
}
