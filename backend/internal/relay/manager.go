package relay

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
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
	PasswordSet  bool   `json:"password_set"`
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

	configFingerprint string
	knownServices     map[string]LocalService
	watcher           *fsnotify.Watcher
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
	nodeName := defaultNodeName()
	if persisted := service.store.LoadNodeName(); persisted != "" {
		nodeName = persisted
	}
	m := &Manager{
		service:   service,
		noRelayer: noRelayer,
		relayBase: strings.TrimSuffix(resolvedRelayBase, "/"),
		nodeName:  nodeName,
	}
	// Keep the in-memory node name and the unified relay.json in sync when the
	// relay reports a node name during the WebSocket handshake.
	service.SetNodeNameChangedHook(func(name string) {
		m.mu.Lock()
		m.nodeName = strings.TrimSpace(name)
		if saveErr := m.service.store.SaveNodeName(m.nodeName); saveErr != nil {
			log.Printf("[relay] save synced node name failed: %v", saveErr)
		}
		m.mu.Unlock()
	})
	return m, nil
}

func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return nil
	}
	m.started = true
	m.ctx = ctx

	m.knownServices = map[string]LocalService{}
	if services, listErr := m.service.store.ListServices(); listErr == nil {
		for _, service := range services {
			m.knownServices[service.Slug] = service
		}
	}
	m.initConfigWatchLocked(ctx)

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
		// The relay may have deleted this device (admin /nodes page, data
		// reset, migration). Probe before trusting the local credentials so a
		// deleted device falls back to a fresh bind instead of staying
		// dead-bound.
		probed, probeErr := m.probeBoundDevice(creds.Relay)
		if probeErr != nil {
			// Transient failure: keep the bound state and let the WebSocket
			// reconnect loop surface any real invalidation.
			return m.statusLocked(), nil
		}
		if probed {
			return m.statusLocked(), nil
		}
		log.Printf("[relay] device no longer registered on relay; entering re-bind flow")
		if clearErr := m.service.store.Clear(); clearErr != nil {
			m.lastError = clearErr.Error()
			return m.statusLocked(), clearErr
		}
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		m.pendingCode = ""
		m.lastError = "device was removed from the relay, re-binding"
	}
	if m.ctx == nil {
		return m.statusLocked(), errors.New("relay manager not started")
	}
	m.ensurePendingLocked()
	m.startPollingLocked(m.ctx, m.pendingCode)
	return m.statusLocked(), nil
}

// probeBoundDevice reports whether the relay still has a valid record for the
// given credentials. The first return value is false when the device was
// deleted server-side; the error is non-nil only for transient transport or
// unexpected relay failures.
func (m *Manager) probeBoundDevice(creds RelayCredentials) (bool, error) {
	base := endpointBaseURL(creds.Endpoint)
	if base == "" || creds.DeviceToken == "" || creds.NodeID == "" {
		return false, errors.New("incomplete relay credentials")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ok, err := m.service.ProbeDevice(ctx, base, creds.DeviceToken, creds.NodeID)
	if err != nil {
		return ok, err
	}
	return ok, nil
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
	if err == nil && strings.TrimSpace(creds.Relay.AccessPassword) != "" {
		status.PasswordSet = true
	}
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

// initConfigWatchLocked records the current unified config fingerprint and
// starts watching relay.json so external edits (hand-editing the file) are
// applied and pushed to the relay without restarting the process.
func (m *Manager) initConfigWatchLocked(ctx context.Context) {
	if _, fingerprint, err := m.service.store.Snapshot(); err == nil {
		m.configFingerprint = fingerprint
	}
	m.startConfigWatcher(ctx)
}

func (m *Manager) startConfigWatcher(ctx context.Context) {
	path := m.service.store.Path()
	if path == "" || m.watcher != nil {
		return
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("[relay] config watcher unavailable: %v", err)
		return
	}
	if err := watcher.Add(filepath.Dir(path)); err != nil {
		_ = watcher.Close()
		log.Printf("[relay] config watcher add failed: %v", err)
		return
	}
	m.watcher = watcher
	go m.watchConfigLoop(ctx, watcher, filepath.Clean(path))
	log.Printf("[relay] watching relay config for external changes: %s", path)
}

func (m *Manager) watchConfigLoop(ctx context.Context, watcher *fsnotify.Watcher, path string) {
	defer func() {
		_ = watcher.Close()
		m.mu.Lock()
		if m.watcher == watcher {
			m.watcher = nil
		}
		m.mu.Unlock()
	}()

	base := filepath.Base(path)
	debounce := time.NewTimer(time.Hour)
	if !debounce.Stop() {
		<-debounce.C
	}
	pending := false

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Name != "" && filepath.Base(event.Name) != base {
				continue
			}
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename|fsnotify.Remove) == 0 {
				continue
			}
			pending = true
			debounce.Reset(250 * time.Millisecond)
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("[relay] config watch error: %v", err)
		case <-debounce.C:
			if !pending {
				continue
			}
			pending = false
			m.reconcileFromDisk(ctx)
		}
	}
}

// reconcileFromDisk applies a relay.json that changed on disk. It reloads the
// unified config, updates the in-memory relay base URL and node name, and
// re-pushes any changed exposed services to the relay.
func (m *Manager) reconcileFromDisk(ctx context.Context) {
	cfg, fingerprint, err := m.service.store.Snapshot()
	if err != nil {
		log.Printf("[relay] reload relay config failed: %v", err)
		return
	}

	m.mu.Lock()
	if fingerprint == m.configFingerprint {
		m.mu.Unlock()
		return
	}
	m.configFingerprint = fingerprint
	newBase := strings.TrimSuffix(strings.TrimSpace(cfg.RelayBaseURL), "/")
	baseChanged := newBase != m.relayBase
	newNodeName := strings.TrimSpace(cfg.NodeName)
	nodeNameChanged := newNodeName != "" && newNodeName != m.nodeName
	m.mu.Unlock()

	if baseChanged {
		if _, err := m.SetRelayBaseURL(newBase); err != nil {
			log.Printf("[relay] apply relay base from file failed: %v", err)
		}
	}
	if nodeNameChanged {
		if err := m.RenameNode(ctx, newNodeName); err != nil {
			log.Printf("[relay] apply node name from file failed: %v", err)
		}
	}
	m.reconcileServices(ctx, cfg.Services)
	log.Printf("[relay] applied external relay config change")
}

func (m *Manager) reconcileServices(ctx context.Context, services []LocalService) {
	desired := make(map[string]LocalService, len(services))
	for _, service := range services {
		normalized, err := NormalizeLocalService(service)
		if err != nil {
			continue
		}
		desired[normalized.Slug] = normalized
	}

	m.mu.Lock()
	known := make(map[string]LocalService, len(m.knownServices))
	for slug, service := range m.knownServices {
		known[slug] = service
	}
	m.mu.Unlock()

	for slug, service := range desired {
		if prev, ok := known[slug]; ok && prev == service {
			continue
		}
		if _, err := m.SaveService(ctx, service); err != nil {
			log.Printf("[relay] sync service %q from file failed: %v", slug, err)
		}
	}
	for slug := range known {
		if _, ok := desired[slug]; ok {
			continue
		}
		if err := m.DeleteService(ctx, slug); err != nil {
			log.Printf("[relay] remove service %q from file failed: %v", slug, err)
		}
	}

	m.mu.Lock()
	m.knownServices = desired
	m.mu.Unlock()
}

func (m *Manager) rememberService(service LocalService) {
	m.mu.Lock()
	if m.knownServices == nil {
		m.knownServices = map[string]LocalService{}
	}
	m.knownServices[service.Slug] = service
	m.mu.Unlock()
}

func (m *Manager) forgetService(slug string) {
	m.mu.Lock()
	if m.knownServices != nil {
		delete(m.knownServices, slug)
	}
	m.mu.Unlock()
}

func (m *Manager) handlePermanentRelayError(err error) {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}

	// Determine whether this is a token invalidation or a password mismatch.
	// Password mismatches should only clear the password, not the entire
	// credential set, so the device can re-connect once the correct password
	// is supplied via the relay settings UI.
	isPasswordError := false
	var dialErr *relayDialError
	if errors.As(err, &dialErr) {
		isPasswordError = dialErr.statusCode == 403 && dialErr.errorCode == "access_password_invalid"
	}

	creds, loadErr := m.service.store.Load()
	if isPasswordError {
		// Clear only the stored access password; keep device token and
		// endpoint so the device can reconnect once the password is fixed.
		if loadErr == nil && creds.Relay.AccessPassword != "" {
			if saveErr := m.service.store.SaveAccessPassword(""); saveErr != nil {
				log.Printf("[relay] clear access password failed: %v", saveErr)
			}
		}
		m.lastError = "access password mismatch — update the password in relay settings"
	} else {
		if clearErr := m.service.store.Clear(); clearErr != nil {
			log.Printf("[relay] clear credentials failed after permanent error: %v", clearErr)
		}
		m.lastError = err.Error()
	}
	m.mu.Unlock()

	log.Printf("[relay] permanent relay error: %v", err)
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

// SetAccessPassword records the user-chosen access password for this device's
// public node. The password may be set before the device is bound: it is merely
// persisted locally then, and pushed to the relay once the device binds or
// reconnects. When already bound, it is pushed to the relay immediately and
// persisted locally. An empty password clears it on the relay.
func (m *Manager) SetAccessPassword(ctx context.Context, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	password = strings.TrimSpace(password)

	creds, err := m.service.store.Load()
	if err != nil {
		return err
	}
	if creds.Relay.DeviceToken == "" || creds.Relay.Endpoint == "" {
		// Not bound yet: persist locally; it is synced to the relay on bind
		// and on every reconnect.
		return m.service.store.SaveAccessPassword(password)
	}
	base := endpointBaseURL(creds.Relay.Endpoint)
	if base == "" {
		return errors.New("invalid relay endpoint")
	}
	if err := m.service.SetAccessPassword(ctx, base, creds.Relay.DeviceToken, password); err != nil {
		return err
	}
	if err := m.service.store.SaveAccessPassword(password); err != nil {
		return err
	}
	// If the session stopped (e.g. because the previous access password failed
	// on the relay), resume the connection now that the relay accepted the new
	// one. The sync-on-reconnect keeps both sides in agreement afterwards.
	if m.cancel == nil && m.ctx != nil {
		m.startLocked(m.ctx)
	}
	return nil
}

// RenameNode changes the display name of this device's node. When the device
// is bound it syncs the new name to the relay first and only persists locally
// after the relay accepts it (enforcing e.g. namespace uniqueness). When not
// bound yet it persists the name locally so it is used for the next bind poll.
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
		// Not bound yet: persist the name locally and it will be sent with
		// the next bind poll.
		if err := m.service.store.SaveNodeName(nodeName); err != nil {
			return err
		}
		m.nodeName = nodeName
		return nil
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
	if err := m.service.store.SaveNodeName(nodeName); err != nil {
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
