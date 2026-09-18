package relay

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

const (
	wsFrameData  byte = 1
	wsFrameClose byte = 2

	relayReconnectInitialBackoff = time.Second
	relayReconnectMaxBackoff     = 30 * time.Second
	relayReconnectStableDuration = time.Minute
)

const relayDeviceIDHeader = "X-Curvature-Device-ID"
const relayNodeNameHeader = "X-Curvature-Relay-Node-Name"
const relayNodePasswordHeader = "X-Curvature-Node-Password"

// relayAccessPasswordWireValue returns the value to send on the device WebSocket
// handshake for an access password. Pure ASCII passwords travel as-is for
// compatibility with relays that only understand raw passwords. Anything else
// is sent as the lowercase SHA-256 hex digest so non-ASCII bytes survive HTTP
// header transport (proxies may reject or mangle non-ASCII header values).
func relayAccessPasswordWireValue(password string) string {
	pw := strings.TrimSpace(password)
	if pw == "" {
		return ""
	}
	ascii := true
	for _, b := range []byte(pw) {
		if b >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return pw
	}
	sum := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(sum[:])
}

type Service struct {
	localAddr string
	localURL  string
	store     *RelayStore
	client    *http.Client
	useTLS    bool

	// nodeNameChangedHook, when set, is invoked whenever the relay reports a
	// node name that differs from the locally persisted one (e.g. the node was
	// renamed on the relay's /nodes page). The manager uses it to keep its
	// in-memory name and the unified relay.json file in sync.
	nodeNameChangedHook func(name string)
}

// SetNodeNameChangedHook registers a callback invoked with the authoritative
// node display name echoed by the relay during the WebSocket handshake.
func (s *Service) SetNodeNameChangedHook(fn func(name string)) {
	s.nodeNameChangedHook = fn
}

type credentialResponse struct {
	DeviceToken string `json:"device_token"`
	NodeID      string `json:"node_id"`
	NodeName    string `json:"node_name"`
	Endpoint    string `json:"endpoint"`
}

type bindPollResponse struct {
	Status          string `json:"status"`
	NextPollAfterMS int64  `json:"next_poll_after_ms"`
	credentialResponse
}

type BindPollResult struct {
	Status        string
	NextPollAfter time.Duration
	Credentials   RelayCredentials
}

type relayDialError struct {
	statusCode int
	status     string
	errorCode  string
	err        error
}

func (e *relayDialError) Error() string {
	if e == nil {
		return ""
	}
	status := strings.TrimSpace(e.status)
	if status == "" && e.statusCode > 0 {
		status = fmt.Sprintf("HTTP %d", e.statusCode)
	}
	message := "relay websocket dial failed"
	if status != "" {
		message += ": " + status
	}
	if e.errorCode != "" {
		message += ": " + e.errorCode
	}
	if e.err != nil {
		message += ": " + e.err.Error()
	}
	return message
}

func (e *relayDialError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func NewService(localAddr string, useTLS bool) (*Service, error) {
	store, err := NewRelayStore()
	if err != nil {
		return nil, err
	}
	if _, err := getOrCreateDeviceID(); err != nil {
		return nil, err
	}

	var client *http.Client
	if useTLS {
		// InsecureSkipVerify is used because the relay connects to the local
		// Curvature server (loopback or same machine), which may present a
		// self-signed certificate. No traffic leaves the host.
		client = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	} else {
		// Do not apply a whole-request timeout here. Relay traffic can include
		// large static assets and streamed responses; http.Client.Timeout would
		// abort the body read mid-transfer and surface as a 502 on the relayed
		// path even when the local server is healthy.
		client = &http.Client{}
	}

	return &Service{
		localAddr: localAddr,
		localURL:  addrToURL(localAddr, "", useTLS),
		store:     store,
		client:    client,
		useTLS:    useTLS,
	}, nil
}

func (s *Service) Run(ctx context.Context) error {
	creds, err := s.store.Load()
	if err != nil {
		return err
	}
	if creds.Relay.DeviceToken == "" || creds.Relay.Endpoint == "" {
		return nil
	}
	if err := s.waitForLocalServer(ctx); err != nil {
		return err
	}

	backoff := relayReconnectInitialBackoff
	for {
		startedAt := time.Now()

		creds, err := s.store.Load()
		if err != nil {
			return err
		}
		if creds.Relay.DeviceToken == "" || creds.Relay.Endpoint == "" {
			return nil
		}
		// Push the locally saved node name and access password to the relay
		// before dialing so both sides always agree, even after a restart or a
		// password set before the device was bound. Sync failures are non-fatal:
		// the WebSocket dial itself will surface any residual mismatch.
		if err := s.syncSettingsToRelay(ctx, creds.Relay); err != nil {
			log.Printf("[relay] sync settings to relay failed (continuing): %v", err)
		}

		err = s.runSession(ctx, creds.Relay)
		if ctx.Err() != nil {
			return nil
		}
		if isPermanentRelayError(err) {
			return err
		}
		if time.Since(startedAt) >= relayReconnectStableDuration {
			backoff = relayReconnectInitialBackoff
		}
		log.Printf("[relay] reconnecting after error in %s: %v", backoff, err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < relayReconnectMaxBackoff {
			backoff *= 2
			if backoff > relayReconnectMaxBackoff {
				backoff = relayReconnectMaxBackoff
			}
		}
	}
}

// syncSettingsToRelay pushes this device's locally saved node name and access
// password to the relay so the relay and this device stay in agreement. Each
// setting is synced independently; the first failure is reported but does not
// stop the other settings from being pushed.
func (s *Service) syncSettingsToRelay(ctx context.Context, creds RelayCredentials) error {
	base := endpointBaseURL(creds.Endpoint)
	if base == "" || creds.DeviceToken == "" {
		return nil
	}
	var firstErr error
	if name := strings.TrimSpace(creds.NodeName); name != "" {
		if err := s.SetNodeName(ctx, base, creds.DeviceToken, name); err != nil {
			log.Printf("[relay] sync node name failed: %v", err)
			firstErr = err
		}
	}
	if err := s.SetAccessPassword(ctx, base, creds.DeviceToken, creds.AccessPassword); err != nil {
		log.Printf("[relay] sync access password failed: %v", err)
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Service) PollBind(ctx context.Context, baseURL, pendingCode string) (BindPollResult, error) {
	return s.PollBindPurpose(ctx, baseURL, pendingCode, "")
}

func (s *Service) PollBindPurpose(ctx context.Context, baseURL, pendingCode, purpose string) (BindPollResult, error) {
	return s.pollBind(ctx, baseURL, pendingCode, purpose, "")
}

func (s *Service) PollBindPurposeWithNodeName(ctx context.Context, baseURL, pendingCode, purpose, nodeName string) (BindPollResult, error) {
	return s.pollBind(ctx, baseURL, pendingCode, purpose, nodeName)
}

func (s *Service) pollBind(ctx context.Context, baseURL, pendingCode, purpose, nodeName string) (BindPollResult, error) {
	pollURL, err := buildBindPollURL(baseURL, pendingCode, purpose, nodeName)
	if err != nil {
		return BindPollResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
	if err != nil {
		return BindPollResult{}, err
	}
	if err := s.attachDeviceID(req); err != nil {
		return BindPollResult{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return BindPollResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return BindPollResult{}, fmt.Errorf("relay bind poll failed: %s %s", resp.Status, strings.TrimSpace(string(payload)))
	}

	var out bindPollResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return BindPollResult{}, err
	}
	result := BindPollResult{
		Status:        strings.TrimSpace(out.Status),
		NextPollAfter: time.Duration(out.NextPollAfterMS) * time.Millisecond,
	}
	if result.Status == "confirmed" {
		result.Credentials = RelayCredentials{
			DeviceToken: strings.TrimSpace(out.DeviceToken),
			NodeID:      strings.TrimSpace(out.NodeID),
			NodeName:    strings.TrimSpace(out.NodeName),
			Endpoint:    strings.TrimSpace(out.Endpoint),
		}
	}
	return result, nil
}

func (s *Service) attachDeviceID(req *http.Request) error {
	if s == nil || req == nil {
		return nil
	}
	deviceID, err := getOrCreateDeviceID()
	if err != nil {
		return err
	}
	if strings.TrimSpace(deviceID) != "" {
		req.Header.Set(relayDeviceIDHeader, deviceID)
	}
	return nil
}

// SetAccessPassword updates the access password the relay enforces for this
// device's public node URL. An empty password clears any existing one.
func (s *Service) SetAccessPassword(ctx context.Context, baseURL, deviceToken, password string) error {
	reqURL, err := buildAccessPasswordURL(baseURL)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{"access_password": strings.TrimSpace(password)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(deviceToken))
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("relay set access password failed: %s", relayStatusError(resp))
	}
	// Verify the relay confirmed the change by reading back password_set.
	var out struct {
		PasswordSet bool `json:"password_set"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out); err == nil {
		wantSet := strings.TrimSpace(password) != ""
		if out.PasswordSet != wantSet {
			log.Printf("[relay] warning: relay reports password_set=%v but %v was requested", out.PasswordSet, wantSet)
		}
	}
	return nil
}

// SetNodeName renames this device's node on the relay. The relay echoes the
// name back on the /nodes page, bind/auth pages and the WebSocket handshake.
func (s *Service) SetNodeName(ctx context.Context, baseURL, deviceToken, nodeName string) error {
	reqURL, err := buildNodeNameURL(baseURL)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{"node_name": strings.TrimSpace(nodeName)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(deviceToken))
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("relay set node name failed: %s", relayStatusError(resp))
	}
	return nil
}

func buildNodeNameURL(baseURL string) (string, error) {
	base, err := parseRelayBase(baseURL)
	if err != nil {
		return "", err
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + "/api/device/node-name"
	base.RawQuery = ""
	base.Fragment = ""
	return base.String(), nil
}

// UnbindNode asks the relay to forget a node. It is meant to be called from
// the local Curvature server after the user confirms unbinding, so the relay
// may be fading out of the picture entirely.
func (s *Service) UnbindNode(ctx context.Context, baseURL, deviceToken, nodeID string) error {
	reqURL, err := buildDeviceURL(baseURL, nodeID)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(deviceToken))
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("relay unbind failed: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// ProbeDevice checks whether the device is still registered on the relay. It
// returns (true, nil) when the relay still knows this device, (false, nil) when
// the device was deleted (node gone / token invalid), or an error for transient
// transport failures. StartBinding uses this to detect a relay-side deletion
// and fall back to a fresh bind flow instead of staying dead-bound.
func (s *Service) ProbeDevice(ctx context.Context, baseURL, deviceToken, nodeID string) (bool, error) {
	reqURL, err := buildDeviceURL(baseURL, nodeID)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(deviceToken))
	resp, err := s.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound, http.StatusUnauthorized:
		return false, nil
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return false, fmt.Errorf("relay device probe failed: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
}

func buildAccessPasswordURL(baseURL string) (string, error) {
	base, err := parseRelayBase(baseURL)
	if err != nil {
		return "", err
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + "/api/device/access-password"
	base.RawQuery = ""
	base.Fragment = ""
	return base.String(), nil
}

func buildDeviceURL(baseURL, nodeID string) (string, error) {
	base, err := parseRelayBase(baseURL)
	if err != nil {
		return "", err
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + "/api/devices/" + url.PathEscape(nodeID)
	base.RawQuery = ""
	base.Fragment = ""
	return base.String(), nil
}

func parseRelayBase(baseURL string) (*url.URL, error) {
	baseURL = strings.TrimSpace(strings.TrimSuffix(baseURL, "/"))
	if baseURL == "" {
		return nil, errors.New("relay base URL required")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "http", "https":
		return u, nil
	default:
		return nil, fmt.Errorf("unsupported relay base URL scheme: %s", u.Scheme)
	}
}

func buildBindPollURL(baseURL, pendingCode, purpose, nodeName string) (string, error) {
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	pendingCode = strings.TrimSpace(pendingCode)
	purpose = strings.TrimSpace(purpose)
	nodeName = strings.TrimSpace(nodeName)
	if baseURL == "" {
		return "", errors.New("relay base URL required")
	}
	if pendingCode == "" {
		return "", errors.New("pending code required")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http", "https":
		u.Path = strings.TrimSuffix(u.Path, "/") + "/api/bind/poll"
		q := u.Query()
		q.Set("code", pendingCode)
		if purpose != "" {
			q.Set("purpose", purpose)
		}
		if nodeName != "" {
			q.Set("node_name", nodeName)
		}
		u.RawQuery = q.Encode()
		u.Fragment = ""
		return u.String(), nil
	default:
		return "", fmt.Errorf("unsupported relay base URL scheme: %s", u.Scheme)
	}
}

func (s *Service) runSession(ctx context.Context, creds RelayCredentials) error {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+creds.DeviceToken)
	if pw := relayAccessPasswordWireValue(creds.AccessPassword); pw != "" {
		headers.Set(relayNodePasswordHeader, pw)
	}
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, creds.Endpoint, headers)
	if err != nil {
		if resp != nil {
			return newRelayDialError(resp, err)
		}
		return err
	}
	defer conn.Close()
	s.storeRelayNodeName(creds, resp)

	wsConn := NewWebSocketNetConn(conn)
	yamuxConfig := yamux.DefaultConfig()
	yamuxConfig.MaxStreamWindowSize = 4 << 20
	yamuxConfig.ConnectionWriteTimeout = 60 * time.Second
	yamuxConfig.EnableKeepAlive = true
	yamuxConfig.KeepAliveInterval = 30 * time.Second
	muxSession, err := yamux.Client(wsConn, yamuxConfig)
	if err != nil {
		return err
	}
	defer muxSession.Close()

	errCh := make(chan error, 1)
	go func() {
		for {
			stream, err := muxSession.Accept()
			if err != nil {
				errCh <- err
				return
			}
			go func() {
				if err := s.handleStream(ctx, stream); err != nil {
					log.Printf("[relay] stream failed: %v", err)
				}
			}()
		}
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Service) handleStream(ctx context.Context, stream net.Conn) error {
	defer stream.Close()

	reader := bufio.NewReader(stream)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	serviceSlug := NormalizeServiceSlug(req.Header.Get(serviceSlugHeader))
	req.Header.Del(serviceSlugHeader)
	if serviceSlug != "" {
		return s.handleServiceStream(req, stream, serviceSlug)
	}
	if websocket.IsWebSocketUpgrade(req) {
		return s.proxyWebSocket(req, stream)
	}
	return s.proxyHTTP(req, stream)
}

func (s *Service) handleServiceStream(req *http.Request, stream net.Conn, slug string) error {
	service, ok, err := s.store.GetService(slug)
	if err != nil {
		return err
	}
	if !ok {
		return writeSimpleHTTPError(stream, http.StatusNotFound, "service_not_found")
	}
	if !service.Enabled {
		return writeSimpleHTTPError(stream, http.StatusForbidden, "service_disabled")
	}
	if websocket.IsWebSocketUpgrade(req) {
		return s.proxyWebSocketToBase(req, stream, service.LocalURL, true)
	}
	return s.proxyHTTPToBase(req, stream, service.LocalURL, true)
}

func (s *Service) proxyHTTP(req *http.Request, stream io.Writer) error {
	return s.proxyHTTPToBase(req, stream, s.localURL, false)
}

func (s *Service) proxyHTTPToBase(req *http.Request, stream io.Writer, baseURL string, stripRelayInternalHeaders bool) error {
	targetURL, err := localTargetURL(baseURL, req.URL)
	if err != nil {
		return err
	}
	outbound := req.Clone(req.Context())
	outbound.URL = targetURL
	outbound.RequestURI = ""
	outbound.Host = targetURL.Host
	prepareLocalProxyHeaders(outbound, req, targetURL, stripRelayInternalHeaders)

	resp, err := s.client.Do(outbound)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rewriteLocalProxyResponse(resp, req, targetURL)
	return resp.Write(stream)
}

func (s *Service) proxyWebSocket(req *http.Request, stream io.ReadWriter) error {
	return s.proxyWebSocketToBase(req, stream, s.localURL, false)
}

func (s *Service) proxyWebSocketToBase(req *http.Request, stream io.ReadWriter, baseURL string, stripRelayInternalHeaders bool) error {
	targetURL, err := websocketTargetURL(baseURL, req.URL)
	if err != nil {
		return err
	}
	headers := cloneHeader(req.Header)
	headers.Del("Connection")
	headers.Del("Upgrade")
	headers.Del("Sec-WebSocket-Key")
	headers.Del("Sec-WebSocket-Version")
	headers.Del("Sec-WebSocket-Extensions")
	headers.Del("Origin")
	if stripRelayInternalHeaders {
		headers.Del("X-Curvature-Relay-Service-Slug")
		headers.Del("X-Curvature-Relayed")
	}
	if localOrigin := originFromBaseURL(baseURL); localOrigin != "" {
		headers.Set("Origin", localOrigin)
	}

	dialer := *websocket.DefaultDialer
	if s.useTLS {
		// InsecureSkipVerify is used because the relay connects to the local
		// Curvature server (loopback or same machine), which may present a
		// self-signed certificate. No traffic leaves the host.
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	if protocol := strings.TrimSpace(req.Header.Get("Sec-WebSocket-Protocol")); protocol != "" {
		dialer.Subprotocols = splitHeaderValues(protocol)
	}
	localConn, resp, err := dialer.DialContext(req.Context(), targetURL, headers)
	if err != nil {
		if resp != nil {
			_ = resp.Write(stream)
		}
		return err
	}
	defer localConn.Close()

	if resp == nil {
		return errors.New("relay websocket upgrade missing response")
	}
	if err := resp.Write(stream); err != nil {
		return err
	}

	errCh := make(chan error, 2)
	go bridgeStreamToWebSocket(stream, localConn, errCh)
	go bridgeWebSocketToStream(localConn, stream, errCh)
	err = <-errCh
	_ = writeWSCloseFrame(stream, websocket.CloseNormalClosure, "connector_closed")
	return err
}

func (s *Service) waitForLocalServer(ctx context.Context) error {
	healthURL := strings.TrimSuffix(s.localURL, "/") + "/health"
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			return err
		}
		resp, err := s.client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) storeRelayNodeName(creds RelayCredentials, resp *http.Response) {
	if s == nil || s.store == nil || resp == nil {
		return
	}
	nodeName := strings.TrimSpace(resp.Header.Get(relayNodeNameHeader))
	if nodeName == "" || nodeName == strings.TrimSpace(creds.NodeName) {
		return
	}
	creds.NodeName = nodeName
	if err := s.store.Save(Credentials{Relay: creds}); err != nil {
		log.Printf("[relay] save relay node name failed: %v", err)
	}
	if s.nodeNameChangedHook != nil {
		s.nodeNameChangedHook(nodeName)
	}
}

func addrToURL(addr, path string, useTLS bool) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimSuffix(addr, "/") + path
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host = "localhost"
		port = strings.TrimPrefix(addr, ":")
	}
	if host == "" {
		host = "localhost"
	}
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if port == "" {
		port = "7331"
	}
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%s%s", scheme, host, port, path)
}

func localTargetURL(base string, requestURL *url.URL) (*url.URL, error) {
	target, err := url.Parse(strings.TrimSuffix(base, "/") + requestURL.RequestURI())
	if err != nil {
		return nil, err
	}
	target.Fragment = ""
	return target, nil
}

func websocketTargetURL(base string, requestURL *url.URL) (string, error) {
	target, err := localTargetURL(base, requestURL)
	if err != nil {
		return "", err
	}
	switch target.Scheme {
	case "http":
		target.Scheme = "ws"
	case "https":
		target.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported websocket target scheme: %s", target.Scheme)
	}
	return target.String(), nil
}

func prepareLocalProxyHeaders(outbound, original *http.Request, targetURL *url.URL, stripRelayInternalHeaders bool) {
	if stripRelayInternalHeaders {
		outbound.Header.Del("X-Curvature-Relay-Service-Slug")
		outbound.Header.Del("X-Curvature-Relayed")
	}
	if original.Host != "" {
		outbound.Header.Set("X-Forwarded-Host", original.Host)
	}
	if original.URL != nil && original.URL.Scheme != "" {
		outbound.Header.Set("X-Forwarded-Proto", original.URL.Scheme)
	}
	if origin := strings.TrimSpace(original.Header.Get("Origin")); origin != "" {
		outbound.Header.Set("X-Forwarded-Origin", origin)
		if localOrigin := originFromURL(targetURL); localOrigin != "" {
			outbound.Header.Set("Origin", localOrigin)
		}
	}
}

func rewriteLocalProxyResponse(resp *http.Response, original *http.Request, targetURL *url.URL) {
	if resp == nil || original == nil || targetURL == nil {
		return
	}
	publicBase := publicBaseFromRequest(original)
	localBase := originFromURL(targetURL)
	if publicBase != "" && localBase != "" {
		if location := strings.TrimSpace(resp.Header.Get("Location")); strings.HasPrefix(location, localBase) {
			resp.Header.Set("Location", publicBase+strings.TrimPrefix(location, localBase))
		}
	}
	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) == 0 {
		return
	}
	resp.Header.Del("Set-Cookie")
	for _, cookie := range cookies {
		parts := strings.Split(cookie, ";")
		filtered := parts[:0]
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if strings.HasPrefix(strings.ToLower(trimmed), "domain=") {
				continue
			}
			filtered = append(filtered, part)
		}
		resp.Header.Add("Set-Cookie", strings.Join(filtered, ";"))
	}
}

func publicBaseFromRequest(req *http.Request) string {
	if req == nil || req.Host == "" {
		return ""
	}
	proto := "https"
	if req.TLS == nil {
		proto = "http"
	}
	if forwarded := strings.TrimSpace(req.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		proto = strings.Split(forwarded, ",")[0]
	}
	return proto + "://" + req.Host
}

func originFromBaseURL(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	return originFromURL(u)
}

func originFromURL(u *url.URL) string {
	if u == nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func writeSimpleHTTPError(w io.Writer, status int, code string) error {
	resp := &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"` + code + `"}`)),
	}
	return resp.Write(w)
}

func splitHeaderValues(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func cloneHeader(header http.Header) http.Header {
	clone := make(http.Header, len(header))
	for key, values := range header {
		clone[key] = append([]string(nil), values...)
	}
	return clone
}

func newRelayDialError(resp *http.Response, err error) error {
	dialErr := &relayDialError{err: err}
	if resp == nil {
		return dialErr
	}
	dialErr.statusCode = resp.StatusCode
	dialErr.status = resp.Status
	dialErr.errorCode = readRelayErrorCode(resp)
	return dialErr
}

func readRelayErrorCode(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return ""
	}
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return ""
	}
	return strings.TrimSpace(out.Error)
}

// relayStatusError builds a human-readable message from a non-2xx relay
// response, translating known error codes to friendly text where possible.
func relayStatusError(resp *http.Response) string {
	if resp == nil {
		return "empty response"
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	raw := strings.TrimSpace(string(body))
	code := readRelayErrorCodeRaw(raw)
	switch code {
	case "node_name_taken":
		return "node name is already in use on this relay"
	case "node_name_required", "node_name_invalid":
		return "node name is missing or invalid"
	case "password_length":
		return "access password must be 4-128 characters"
	case "device_token_invalid":
		return "device token was rejected by the relay"
	}
	if raw != "" {
		return resp.Status + " (" + raw + ")"
	}
	return resp.Status
}

func readRelayErrorCodeRaw(body string) string {
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return ""
	}
	return strings.TrimSpace(out.Error)
}

func isPermanentRelayError(err error) bool {
	if err == nil {
		return false
	}
	var dialErr *relayDialError
	if !errors.As(err, &dialErr) {
		return false
	}
	if dialErr.statusCode == http.StatusUnauthorized && dialErr.errorCode == "device_token_invalid" {
		return true
	}
	if dialErr.statusCode == http.StatusForbidden && dialErr.errorCode == "access_password_invalid" {
		return true
	}
	return false
}
