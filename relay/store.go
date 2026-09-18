package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Device struct {
	NodeID         string    `json:"node_id"`
	NodeName       string    `json:"node_name"`
	DeviceID       string    `json:"device_id"`
	DeviceToken    string    `json:"device_token"`
	Endpoint       string    `json:"endpoint"`
	AccessPassword string    `json:"access_password,omitempty"`
	BoundAt        time.Time `json:"bound_at"`
}

// EndpointBaseIsWSS reports whether the device connects over TLS (wss), which
// drives whether the access-password cookie should be flagged Secure.
func (d *Device) EndpointBaseIsWSS() bool {
	return strings.HasPrefix(strings.TrimSpace(d.Endpoint), "wss://")
}

type Bind struct {
	Code      string    `json:"code"`
	DeviceID  string    `json:"device_id"`
	Purpose   string    `json:"purpose,omitempty"`
	NodeName  string    `json:"node_name,omitempty"`
	NodeID    string    `json:"node_id,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type Service struct {
	Slug    string `json:"slug"`
	NodeID  string `json:"node_id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// storeState is the unified on-disk schema for the relay's persistent data. It
// merges the former binds.json, devices.json and services.json into a single
// JSON file so the relay owns exactly one state file. The legacy split files
// are read once and removed after the unified file is written.
type storeState struct {
	Binds    map[string]*Bind    `json:"binds,omitempty"`
	Devices  map[string]*Device  `json:"devices,omitempty"`
	Services map[string]*Service `json:"services,omitempty"`
}

const stateFileName = "state.json"

type Store struct {
	mu       sync.Mutex
	dir      string
	filePath string
	binds    map[string]*Bind
	devices  map[string]*Device
	services map[string]*Service
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir:      dir,
		filePath: filepath.Join(dir, stateFileName),
		binds:    map[string]*Bind{},
		devices:  map[string]*Device{},
		services: map[string]*Service{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return s.migrateLegacy()
		}
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var state storeState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	s.applyState(state)
	return nil
}

func (s *Store) applyState(state storeState) {
	if state.Binds != nil {
		s.binds = state.Binds
	}
	if state.Devices != nil {
		s.devices = state.Devices
	}
	if state.Services != nil {
		s.services = state.Services
	}
}

// migrateLegacy upgrades the pre-unification split files into state.json the
// first time the unified file is missing. Legacy files are removed only after
// the unified file has been written successfully.
func (s *Store) migrateLegacy() error {
	migrated := false
	if s.loadLegacy("binds.json", &s.binds) {
		migrated = true
	}
	if s.loadLegacy("devices.json", &s.devices) {
		migrated = true
	}
	if s.loadLegacy("services.json", &s.services) {
		migrated = true
	}
	if !migrated {
		return nil
	}
	if err := s.saveLocked(); err != nil {
		return err
	}
	for _, name := range []string{"binds.json", "devices.json", "services.json"} {
		_ = os.Remove(filepath.Join(s.dir, name))
	}
	return nil
}

func (s *Store) loadLegacy(name string, target any) bool {
	data, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil || len(data) == 0 {
		return false
	}
	return json.Unmarshal(data, target) == nil
}

// saveLocked writes the entire relay state atomically. Callers must hold s.mu.
func (s *Store) saveLocked() error {
	state := storeState{
		Binds:    s.binds,
		Devices:  s.devices,
		Services: s.services,
	}
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.filePath)
}

func (s *Store) getBind(code string) *Bind {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.binds[code]
}

func (s *Store) createBind(b *Bind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.binds[b.Code] = b
	return s.saveLocked()
}

func (s *Store) updateBind(b *Bind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.binds[b.Code] = b
	return s.saveLocked()
}

func (s *Store) saveDevice(d *Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices[d.NodeID] = d
	return s.saveLocked()
}

func (s *Store) getDeviceByNode(nodeID string) *Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.devices[nodeID]
}

func (s *Store) deleteDevice(nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.devices, nodeID)
	return s.saveLocked()
}

func (s *Store) getDeviceByToken(token string) *Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.DeviceToken == token {
			return d
		}
	}
	return nil
}

func (s *Store) listDevices() []*Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Device, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, d)
	}
	return out
}

// isNodeNameTaken reports whether name is already used by a different device.
// The excludeNodeID parameter allows the current device to keep its own name.
func (s *Store) isNodeNameTaken(name, excludeNodeID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return false
	}
	for _, d := range s.devices {
		if d.NodeID == excludeNodeID {
			continue
		}
		if strings.TrimSpace(strings.ToLower(d.NodeName)) == name {
			return true
		}
	}
	return false
}

func (s *Store) saveService(svc *Service) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.services[serviceKey(svc.Slug, svc.NodeID)] = svc
	return s.saveLocked()
}

func (s *Store) deleteService(slug, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.services, serviceKey(slug, nodeID))
	return s.saveLocked()
}

func (s *Store) getService(slug, nodeID string) *Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.services[serviceKey(slug, nodeID)]
}

// getServiceFold looks up a service with case-insensitive matching, which is
// required because subdomain-style Host headers are normalized to lowercase.
func (s *Store) getServiceFold(slug, nodeID string) *Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	slug = strings.ToLower(slug)
	for _, svc := range s.services {
		if strings.EqualFold(svc.Slug, slug) && strings.EqualFold(svc.NodeID, nodeID) {
			return svc
		}
	}
	return nil
}

func (s *Store) listServices(nodeID string) []*Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Service, 0)
	for _, svc := range s.services {
		if nodeID == "" || svc.NodeID == nodeID {
			out = append(out, svc)
		}
	}
	return out
}

func serviceKey(slug, nodeID string) string {
	return slug + "@" + nodeID
}
