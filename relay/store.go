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

type Store struct {
	mu       sync.Mutex
	dir      string
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
	if err := s.loadJSON("binds.json", &s.binds); err != nil {
		return err
	}
	if err := s.loadJSON("devices.json", &s.devices); err != nil {
		return err
	}
	if err := s.loadJSON("services.json", &s.services); err != nil {
		return err
	}
	return nil
}

func (s *Store) loadJSON(name string, target any) error {
	path := filepath.Join(s.dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, target)
}

func (s *Store) saveJSON(name string, value any) error {
	path := filepath.Join(s.dir, name)
	tmp := path + ".tmp"
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
	return s.saveJSON("binds.json", s.binds)
}

func (s *Store) updateBind(b *Bind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.binds[b.Code] = b
	return s.saveJSON("binds.json", s.binds)
}

func (s *Store) saveDevice(d *Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices[d.NodeID] = d
	return s.saveJSON("devices.json", s.devices)
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
	return s.saveJSON("devices.json", s.devices)
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

func (s *Store) saveService(svc *Service) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.services[serviceKey(svc.Slug, svc.NodeID)] = svc
	return s.saveJSON("services.json", s.services)
}

func (s *Store) deleteService(slug, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.services, serviceKey(slug, nodeID))
	return s.saveJSON("services.json", s.services)
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
