package relay

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type Status struct {
	Bound        bool   `json:"relay_bound"`
	NoRelayer    bool   `json:"no_relayer"`
	PendingCode  string `json:"pending_code"`
	NodeName     string `json:"node_name"`
	NodeID       string `json:"node_id"`
	E2EENodeID   string `json:"e2ee_node_id,omitempty"`
	RelayBaseURL string `json:"relay_base_url"`
	NodeURL      string `json:"node_url"`
	LastError    string `json:"last_error,omitempty"`
	E2EERequired bool   `json:"e2ee_required"`
}

type Manager struct {
	service   *Service
	noRelayer bool
	relayBase string

	mu           sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
	started      bool
	polling      bool
	pendingCode  string
	pendingSince time.Time
	nodeName     string
	lastError    string
}

func NewManager(localAddr string, noRelayer bool, relayBaseURL string, useTLS bool) (*Manager, error) {
	service, err := NewService(localAddr, useTLS)
	if err != nil {
		return nil, err
	}
	resolvedRelayBase := strings.TrimSpace(os.Getenv("CURVATURE_RELAY_BASE_URL"))
	if resolvedRelayBase == "" {
		resolvedRelayBase = service.store.LoadRelayBase()
	}
	if resolvedRelayBase == "" {
		resolvedRelayBase = strings.TrimSpace(relayBaseURL)
	}
	if resolvedRelayBase == "" {
		noRelayer = true
	}
	return &Manager{
		service:   service,
		noRelayer: noRelayer,
		relayBase: strings.TrimSuffix(resolvedRelayBase, "/"),
		nodeName:  defaultNodeName(),
	}, nil
}

func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return nil
	}
	m.started = true
	m.ctx = ctx

	creds, err := m.service.store.Load()
	if err != nil {
		m.started = false
		m.ctx = nil
		return err
	}
	if relayBaseMismatch(m.relayBase, creds.Relay.Endpoint) {
		if clearErr := m.service.store.Clear(); clearErr != nil {
			m.started = false
			m.ctx = nil
			return clearErr
		}
		log.Printf("[relay] configured relay base changed, clearing stored credentials and requiring rebind")
		m.lastError = "relay base changed, rebinding required"
		creds = Credentials{}
	}
	if m.noRelayer {
		return nil
	}
	if creds.Relay.DeviceToken != "" && creds.Relay.Endpoint != "" {
		m.startLocked(ctx)
		return nil
	}
	return nil
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.statusLocked()
}

func (m *Manager) NoRelayer() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.noRelayer
}

// SetRelayBaseURL reconfigures the relay base URL at runtime, persists the
// new value, and re-establishes the relay connection. Switching to a different
// relay invalidates existing credentials and requires a fresh binding.
func (m *Manager) SetRelayBaseURL(relayBaseURL string) (Status, error) {
	relayBaseURL = strings.TrimSuffix(strings.TrimSpace(relayBaseURL), "/")

	m.mu.Lock()
	defer m.mu.Unlock()

	if relayBaseURL == m.relayBase {
		return m.statusLocked(), nil
	}

	if err := m.service.store.SaveRelayBase(relayBaseURL); err != nil {
		return m.statusLocked(), err
	}

	m.relayBase = relayBaseURL
	m.noRelayer = relayBaseURL == ""

	creds, err := m.service.store.Load()
	if err == nil && creds.Relay.DeviceToken != "" && creds.Relay.Endpoint != "" {
		if relayBaseMismatch(relayBaseURL, creds.Relay.Endpoint) {
			if clearErr := m.service.store.Clear(); clearErr != nil {
				return m.statusLocked(), clearErr
			}
			m.lastError = "relay base changed, rebinding required"
		}
	}

	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.pendingCode = ""

	if !m.noRelayer && m.ctx != nil {
		if reloaded, err := m.service.store.Load(); err == nil &&
			reloaded.Relay.DeviceToken != "" && reloaded.Relay.Endpoint != "" {
			m.startLocked(m.ctx)
		}
	}
	return m.statusLocked(), nil
}

func (m *Manager) StartBinding() (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.noRelayer {
		return m.statusLocked(), nil
	}
	creds, err := m.service.store.Load()
	if err != nil {
		m.lastError = err.Error()
		return m.statusLocked(), err
	}
	if creds.Relay.DeviceToken != "" && creds.Relay.Endpoint != "" {
		return m.statusLocked(), nil
	}
	if m.ctx == nil {
		return m.statusLocked(), errors.New("relay manager not started")
	}
	m.ensurePendingLocked()
	m.startPollingLocked(m.ctx, m.pendingCode)
	return m.statusLocked(), nil
}

func (m *Manager) statusLocked() Status {
	status := Status{
		NoRelayer:    m.noRelayer,
		PendingCode:  m.pendingCode,
		NodeName:     m.nodeName,
		RelayBaseURL: m.resolveRelayBaseLocked(),
		LastError:    m.lastError,
	}
	if m.noRelayer {
		status.PendingCode = ""
		return status
	}
	creds, err := m.service.store.Load()
	if err == nil && creds.Relay.DeviceToken != "" && creds.Relay.Endpoint != "" {
		status.Bound = true
		status.NodeID = creds.Relay.NodeID
		if nodeName := strings.TrimSpace(creds.Relay.NodeName); nodeName != "" {
			status.NodeName = nodeName
		}
		if status.RelayBaseURL == "" {
			status.RelayBaseURL = endpointBaseURL(creds.Relay.Endpoint)
		}
		if status.RelayBaseURL != "" && status.NodeID != "" {
			status.NodeURL = strings.TrimSuffix(status.RelayBaseURL, "/") + "/n/" + status.NodeID + "/"
		}
		status.PendingCode = ""
	}
	return status
}

func (m *Manager) startLocked(parent context.Context) {
	runCtx, cancel := context.WithCancel(parent)
	m.ctx = parent
	m.cancel = cancel
	go func() {
		if err := m.service.Run(runCtx); err != nil && runCtx.Err() == nil {
			if isPermanentRelayError(err) {
				m.handlePermanentRelayError(err)
				return
			}
			log.Printf("[relay] stopped: %v", err)
		}
	}()
}

func (m *Manager) startPollingLocked(parent context.Context, pendingCode string) {
	if strings.TrimSpace(pendingCode) == "" || m.polling {
		return
	}
	m.polling = true
	go m.pollLoop(parent, pendingCode)
}

func (m *Manager) pollLoop(parent context.Context, pendingCode string) {
	defer m.finishPolling(pendingCode)

	m.runBindPollLoop(parent, pendingCode, "",
		func(result BindPollResult) error {
			return m.service.store.Save(Credentials{Relay: result.Credentials})
		},
		func(status string) {
			m.mu.Lock()
			m.pendingCode = ""
			m.lastError = status
			alreadyStarted := status == "" && m.cancel != nil
			m.mu.Unlock()
			if status != "" {
				return
			}
			if alreadyStarted {
				m.restart()
				return
			}
			m.mu.Lock()
			if m.ctx != nil {
				m.startLocked(m.ctx)
			}
			m.mu.Unlock()
		},
		func(message string) {
			m.mu.Lock()
			m.lastError = message
			m.mu.Unlock()
		},
	)
}

func (m *Manager) runBindPollLoop(
	parent context.Context,
	pendingCode string,
	purpose string,
	onConfirmed func(BindPollResult) error,
	onFinished func(status string),
	onError func(message string),
) {
	delay := time.Duration(0)
	for {
		if delay > 0 {
			select {
			case <-parent.Done():
				return
			case <-time.After(delay):
			}
		} else if parent.Err() != nil {
			return
		}

		result, err := m.service.PollBindPurposeWithNodeName(parent, m.resolveRelayBase(), pendingCode, purpose, m.nodeName)
		if err != nil {
			delay = nextDelay(delay)
			onError(err.Error())
			continue
		}

		switch result.Status {
		case "pending":
			delay = result.NextPollAfter
			if delay <= 0 {
				delay = 3 * time.Second
			}
		case "confirmed":
			if err := onConfirmed(result); err != nil {
				delay = nextDelay(delay)
				onError(err.Error())
				continue
			}
			onFinished("")
			return
		case "claimed", "expired", "revoked":
			onFinished(result.Status)
			return
		default:
			delay = nextDelay(delay)
		}
	}
}

func (m *Manager) finishPolling(pendingCode string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.polling = false
}

func (m *Manager) restart() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	if m.ctx != nil {
		m.startLocked(m.ctx)
	}
}

func (m *Manager) handlePermanentRelayError(err error) {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	if clearErr := m.service.store.Clear(); clearErr != nil {
		log.Printf("[relay] clear credentials failed after permanent error: %v", clearErr)
	}
	m.lastError = err.Error()
	m.mu.Unlock()

	log.Printf("[relay] credentials invalidated, rebinding required: %v", err)
}

func (m *Manager) ensurePendingLocked() {
	if strings.TrimSpace(m.pendingCode) != "" {
		return
	}
	m.pendingCode = generatePendingCode()
	m.pendingSince = time.Now().UTC()
}

func (m *Manager) resolveRelayBase() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resolveRelayBaseLocked()
}

func (m *Manager) resolveRelayBaseLocked() string {
	if strings.TrimSpace(m.relayBase) != "" {
		return strings.TrimSuffix(m.relayBase, "/")
	}
	creds, err := m.service.store.Load()
	if err != nil {
		return ""
	}
	return endpointBaseURL(creds.Relay.Endpoint)
}

func relayBaseMismatch(configuredBase, endpoint string) bool {
	configuredBase = strings.TrimSuffix(strings.TrimSpace(configuredBase), "/")
	endpointBase := strings.TrimSuffix(strings.TrimSpace(endpointBaseURL(endpoint)), "/")
	if configuredBase == "" || endpointBase == "" {
		return false
	}
	return configuredBase != endpointBase
}

func nextDelay(current time.Duration) time.Duration {
	if current <= 0 {
		return 2 * time.Second
	}
	if current < 10*time.Second {
		current *= 2
	}
	if current > 10*time.Second {
		current = 10 * time.Second
	}
	return current
}

func generatePendingCode() string {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return "pc_" + base64.RawURLEncoding.EncodeToString(buf)
}

func defaultNodeName() string {
	name, err := os.Hostname()
	if err != nil {
		return "localhost"
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "localhost"
	}
	return name
}

func endpointBaseURL(endpoint string) string {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return ""
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	default:
		return ""
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimSuffix(u.String(), "/")
}

// SetAccessPassword pushes the user-chosen access password to the relay for
// this device's public node and persists it locally so status can echo whether
// one is configured. An empty password clears it on the relay.
func (m *Manager) SetAccessPassword(ctx context.Context, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	creds, err := m.service.store.Load()
	if err != nil {
		return err
	}
	if creds.Relay.DeviceToken == "" || creds.Relay.Endpoint == "" {
		return errors.New("relay not bound")
	}
	base := endpointBaseURL(creds.Relay.Endpoint)
	if base == "" {
		return errors.New("invalid relay endpoint")
	}
	if err := m.service.SetAccessPassword(ctx, base, creds.Relay.DeviceToken, password); err != nil {
		return err
	}
	creds.Relay.AccessPassword = strings.TrimSpace(password)
	if err := m.service.store.Save(creds); err != nil {
		return err
	}
	return nil
}

// RenameNode changes the display name of this device's node on the relay and
// persists it locally so status and future bind polls use the new name.
func (m *Manager) RenameNode(ctx context.Context, nodeName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" {
		return errors.New("node name required")
	}
	if runes := []rune(nodeName); len(runes) > 64 {
		nodeName = string(runes[:64])
	}
	creds, err := m.service.store.Load()
	if err != nil {
		return err
	}
	if creds.Relay.DeviceToken == "" || creds.Relay.Endpoint == "" {
		return errors.New("relay not bound")
	}
	base := endpointBaseURL(creds.Relay.Endpoint)
	if base == "" {
		return errors.New("invalid relay endpoint")
	}
	if err := m.service.SetNodeName(ctx, base, creds.Relay.DeviceToken, nodeName); err != nil {
		return err
	}
	creds.Relay.NodeName = nodeName
	if err := m.service.store.Save(creds); err != nil {
		return err
	}
	m.nodeName = nodeName
	return nil
}

// UnbindNode removes the node from the relay and clears local relay
// credentials so the device no longer connects to that relay.
func (m *Manager) UnbindNode(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	creds, err := m.service.store.Load()
	if err != nil {
		return err
	}
	if creds.Relay.DeviceToken == "" || creds.Relay.Endpoint == "" {
		return errors.New("relay not bound")
	}
	base := endpointBaseURL(creds.Relay.Endpoint)
	nodeID := creds.Relay.NodeID
	token := creds.Relay.DeviceToken
	if base != "" && nodeID != "" {
		unbindErr := m.service.UnbindNode(ctx, base, token, nodeID)
		if unbindErr != nil {
			log.Printf("[relay] relay unbind request failed (continuing local clear): %v", unbindErr)
		}
	}
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.pendingCode = ""
	m.lastError = ""
	if err := m.service.store.Clear(); err != nil {
		return err
	}
	return nil
}
