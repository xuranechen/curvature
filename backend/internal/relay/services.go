package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const serviceSlugHeader = "X-Curvature-Relay-Service-Slug"

var serviceSlugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{1,38}[a-z0-9])$`)

type LocalService struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	LocalURL string `json:"local_url"`
	Enabled  bool   `json:"enabled"`
}

func NormalizeServiceSlug(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func ValidServiceSlug(value string) bool {
	value = NormalizeServiceSlug(value)
	return serviceSlugPattern.MatchString(value) && !strings.Contains(value, "--")
}

func NormalizeLocalService(service LocalService) (LocalService, error) {
	service.Slug = NormalizeServiceSlug(service.Slug)
	if !ValidServiceSlug(service.Slug) {
		return LocalService{}, errors.New("invalid_service_slug")
	}
	service.Name = strings.TrimSpace(service.Name)
	if service.Name == "" {
		service.Name = service.Slug
	}
	localURL, err := normalizeLocalServiceURL(service.LocalURL)
	if err != nil {
		return LocalService{}, err
	}
	service.LocalURL = localURL
	return service, nil
}

func normalizeLocalServiceURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("invalid_local_service_url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("invalid_local_service_url")
	}
	host := strings.Trim(strings.ToLower(u.Hostname()), "[]")
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return "", errors.New("local_service_host_not_allowed")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func (m *Manager) ListServices() ([]LocalService, error) {
	return m.service.store.ListServices()
}

func (m *Manager) SaveService(ctx context.Context, service LocalService) (LocalService, error) {
	normalized, err := NormalizeLocalService(service)
	if err != nil {
		return LocalService{}, err
	}
	previous, hadPrevious, err := m.service.store.GetService(normalized.Slug)
	if err != nil {
		return LocalService{}, err
	}
	if normalized.Enabled {
		if err := m.registerService(ctx, normalized); err != nil {
			return LocalService{}, err
		}
	} else if hadPrevious && previous.Enabled {
		if err := m.updateRemoteServiceEnabled(ctx, normalized.Slug, false); err != nil {
			return LocalService{}, err
		}
	}
	if err := m.service.store.SaveService(normalized); err != nil {
		return LocalService{}, err
	}
	m.rememberService(normalized)
	return normalized, nil
}

func (m *Manager) DeleteService(ctx context.Context, slug string) error {
	slug = NormalizeServiceSlug(slug)
	if !ValidServiceSlug(slug) {
		return errors.New("invalid_service_slug")
	}
	if err := m.deleteRemoteService(ctx, slug); err != nil {
		return err
	}
	if err := m.service.store.DeleteService(slug); err != nil {
		return err
	}
	m.forgetService(slug)
	return nil
}

func (m *Manager) registerService(ctx context.Context, service LocalService) error {
	return m.saveRemoteService(ctx, service.Slug, service.Name, true)
}

func (m *Manager) updateRemoteServiceEnabled(ctx context.Context, slug string, enabled bool) error {
	return m.saveRemoteService(ctx, slug, slug, enabled)
}

func (m *Manager) saveRemoteService(ctx context.Context, slug, name string, enabled bool) error {
	creds, err := m.service.store.Load()
	if err != nil {
		return err
	}
	if creds.Relay.DeviceToken == "" || creds.Relay.NodeID == "" {
		return errors.New("relay_not_bound")
	}
	endpoint := strings.TrimSuffix(m.resolveRelayBase(), "/") + "/api/device/nodes/" + url.PathEscape(creds.Relay.NodeID) + "/services/" + url.PathEscape(slug)
	body, err := json.Marshal(map[string]any{"name": name, "enabled": enabled})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+creds.Relay.DeviceToken)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("relay_service_register_failed_%d", resp.StatusCode)
	}
	return nil
}

func (m *Manager) deleteRemoteService(ctx context.Context, slug string) error {
	creds, err := m.service.store.Load()
	if err != nil {
		return err
	}
	if creds.Relay.DeviceToken == "" || creds.Relay.NodeID == "" {
		return errors.New("relay_not_bound")
	}
	endpoint := strings.TrimSuffix(m.resolveRelayBase(), "/") + "/api/device/nodes/" + url.PathEscape(creds.Relay.NodeID) + "/services/" + url.PathEscape(slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+creds.Relay.DeviceToken)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("relay_service_delete_failed_%d", resp.StatusCode)
	}
	return nil
}
