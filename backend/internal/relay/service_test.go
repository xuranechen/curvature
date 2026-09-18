package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGetOrCreateDeviceIDStable(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)

	first, err := getOrCreateDeviceID()
	if err != nil {
		t.Fatalf("getOrCreateDeviceID() first error = %v", err)
	}
	if first == "" || !strings.HasPrefix(first, "md_") {
		t.Fatalf("unexpected device id = %q", first)
	}

	second, err := getOrCreateDeviceID()
	if err != nil {
		t.Fatalf("getOrCreateDeviceID() second error = %v", err)
	}
	if second != first {
		t.Fatalf("device id changed: first=%q second=%q", first, second)
	}
}

func TestCredentialsStoreSaveLoad(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)

	store, err := NewRelayStore()
	if err != nil {
		t.Fatalf("NewRelayStore() error = %v", err)
	}

	input := Credentials{
		Relay: RelayCredentials{
			DeviceToken: "dev_123",
			NodeID:      "node_123",
			NodeName:    "Office Mac",
			Endpoint:    "wss://relay.example.com/ws/connector",
		},
	}
	if err := store.Save(input); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Relay != input.Relay {
		t.Fatalf("Load() = %+v, want %+v", got.Relay, input.Relay)
	}

	info, err := os.Stat(store.filePath)
	if err != nil {
		t.Fatalf("credentials file missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestCredentialsStoreClear(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)

	store, err := NewRelayStore()
	if err != nil {
		t.Fatalf("NewRelayStore() error = %v", err)
	}
	if err := store.Save(Credentials{
		Relay: RelayCredentials{
			DeviceToken: "dev_123",
			NodeID:      "node_123",
			Endpoint:    "wss://relay.example.com/ws/connector",
		},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Relay.DeviceToken != "" || got.Relay.Endpoint != "" || got.Relay.NodeID != "" {
		t.Fatalf("expected empty credentials after clear, got %+v", got.Relay)
	}
}

func TestCredentialsStoreNodeNamePersistsAcrossRelayBaseChanges(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)

	store, err := NewRelayStore()
	if err != nil {
		t.Fatalf("NewRelayStore() error = %v", err)
	}
	if err := store.SaveNodeName("客厅电脑"); err != nil {
		t.Fatalf("SaveNodeName() error = %v", err)
	}
	if got := store.LoadNodeName(); got != "客厅电脑" {
		t.Fatalf("LoadNodeName() = %q, want 客厅电脑", got)
	}
	// Saving the relay base must not wipe the node name.
	if err := store.SaveRelayBase("https://relay.example.com"); err != nil {
		t.Fatalf("SaveRelayBase() error = %v", err)
	}
	if got := store.LoadNodeName(); got != "客厅电脑" {
		t.Fatalf("LoadNodeName() after SaveRelayBase = %q, want 客厅电脑", got)
	}
	if got := store.LoadRelayBase(); got != "https://relay.example.com" {
		t.Fatalf("LoadRelayBase() = %q", got)
	}
	// Clearing the relay base must keep the node name.
	if err := store.SaveRelayBase(""); err != nil {
		t.Fatalf("SaveRelayBase(\"\") error = %v", err)
	}
	if got := store.LoadNodeName(); got != "客厅电脑" {
		t.Fatalf("LoadNodeName() after clearing relay base = %q, want 客厅电脑", got)
	}
}

func TestCredentialsStoreAccessPasswordBeforeBinding(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)

	store, err := NewRelayStore()
	if err != nil {
		t.Fatalf("NewRelayStore() error = %v", err)
	}
	// A password set before binding must be persisted and survive the bind.
	if err := store.SaveAccessPassword("pre-bind-pw"); err != nil {
		t.Fatalf("SaveAccessPassword() error = %v", err)
	}
	if got := store.LoadAccessPassword(); got != "pre-bind-pw" {
		t.Fatalf("LoadAccessPassword() = %q, want pre-bind-pw", got)
	}
	// Binding must not wipe the pre-bound access password.
	if err := store.Save(Credentials{
		Relay: RelayCredentials{
			DeviceToken: "dev_123",
			NodeID:      "node_123",
			Endpoint:    "wss://relay.example.com/ws/connector",
		},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Relay.AccessPassword != "pre-bind-pw" {
		t.Fatalf("Load().AccessPassword after bind = %q, want pre-bind-pw", got.Relay.AccessPassword)
	}
	// Clearing the password clears both the top-level value and the bound copy.
	if err := store.SaveAccessPassword(""); err != nil {
		t.Fatalf("SaveAccessPassword(\"\") error = %v", err)
	}
	if got, err := store.Load(); err != nil || got.Relay.AccessPassword != "" {
		t.Fatalf("Load() after clear = %+v, err=%v; want empty password", got, err)
	}
}

func TestRelayAccessPasswordWireValue(t *testing.T) {
	// Pure ASCII passwords travel as-is for relay compatibility.
	if got := relayAccessPasswordWireValue("secret123"); got != "secret123" {
		t.Fatalf("ascii wire value = %q, want secret123", got)
	}
	if got := relayAccessPasswordWireValue("  secret123  "); got != "secret123" {
		t.Fatalf("trimmed ascii wire value = %q, want secret123", got)
	}
	// Non-ASCII passwords are sent as the lowercase SHA-256 hex digest so they
	// survive HTTP header transport untouched.
	got := relayAccessPasswordWireValue("秘密密码")
	if got == "" || len(got) != sha256.Size*2 || !isLowerHex(got) {
		t.Fatalf("non-ascii wire value = %q, want a %d-char lower hex digest", got, sha256.Size*2)
	}
	sum := sha256.Sum256([]byte("秘密密码"))
	if got != hex.EncodeToString(sum[:]) {
		t.Fatalf("non-ascii wire value = %q, want %q", got, hex.EncodeToString(sum[:]))
	}
	if relayAccessPasswordWireValue("") != "" {
		t.Fatal("empty password wire value should be empty")
	}
}

func isLowerHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func TestBuildBindPollURL(t *testing.T) {
	got, err := buildBindPollURL("https://relay.example.com", "pc_123", "", "")
	if err != nil {
		t.Fatalf("buildBindPollURL() error = %v", err)
	}
	if got != "https://relay.example.com/api/bind/poll?code=pc_123" {
		t.Fatalf("buildBindPollURL() = %q", got)
	}
	got, err = buildBindPollURL("https://relay.example.com", "pc_123", "purpose-x", "my-node")
	if err != nil {
		t.Fatalf("buildBindPollURL() error = %v", err)
	}
	if got != "https://relay.example.com/api/bind/poll?code=pc_123&node_name=my-node&purpose=purpose-x" {
		t.Fatalf("buildBindPollURL() with purpose+node_name = %q", got)
	}
}

func TestPrepareLocalProxyHeadersRemovesRelayInternalHeaders(t *testing.T) {
	original, err := http.NewRequest(http.MethodGet, "https://test-node-relay.a9gent.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	original.Host = "test-node-relay.a9gent.com"
	original.Header.Set("X-Curvature-Relayed", "1")
	original.Header.Set("X-Curvature-Relay-Service-Slug", "test")
	targetURL, err := url.Parse("http://127.0.0.1:5173/")
	if err != nil {
		t.Fatal(err)
	}
	outbound := original.Clone(original.Context())

	prepareLocalProxyHeaders(outbound, original, targetURL, true)

	if got := outbound.Header.Get("X-Curvature-Relayed"); got != "" {
		t.Fatalf("X-Curvature-Relayed = %q, want empty", got)
	}
	if got := outbound.Header.Get("X-Curvature-Relay-Service-Slug"); got != "" {
		t.Fatalf("X-Curvature-Relay-Service-Slug = %q, want empty", got)
	}
	if got := outbound.Header.Get("X-Forwarded-Host"); got != "test-node-relay.a9gent.com" {
		t.Fatalf("X-Forwarded-Host = %q", got)
	}
}

func TestPrepareLocalProxyHeadersKeepsRelayedHeaderForNodeProxy(t *testing.T) {
	original, err := http.NewRequest(http.MethodGet, "https://relay.a9gent.com/n/node/", nil)
	if err != nil {
		t.Fatal(err)
	}
	original.Host = "relay.a9gent.com"
	original.Header.Set("X-Curvature-Relayed", "1")
	targetURL, err := url.Parse("http://127.0.0.1:7331/")
	if err != nil {
		t.Fatal(err)
	}
	outbound := original.Clone(original.Context())

	prepareLocalProxyHeaders(outbound, original, targetURL, false)

	if got := outbound.Header.Get("X-Curvature-Relayed"); got != "1" {
		t.Fatalf("X-Curvature-Relayed = %q, want 1", got)
	}
	if got := outbound.Header.Get("X-Forwarded-Host"); got != "relay.a9gent.com" {
		t.Fatalf("X-Forwarded-Host = %q", got)
	}
}

func TestServicePollBind(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)

	svc, err := NewService(":7331", false)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	svc.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != "https://relay.example.com/api/bind/poll?code=pc_live" {
				t.Fatalf("unexpected poll URL: %s", req.URL.String())
			}
			if strings.TrimSpace(req.Header.Get(relayDeviceIDHeader)) == "" {
				t.Fatal("expected device id header on bind poll request")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"status":"confirmed","device_token":"dev_live","node_id":"node_live","node_name":"Office Mac","endpoint":"wss://relay.example.com/ws/connector"}`)),
			}, nil
		}),
	}

	result, err := svc.PollBind(context.Background(), "https://relay.example.com", "pc_live")
	if err != nil {
		t.Fatalf("PollBind() error = %v", err)
	}
	if result.Status != "confirmed" {
		t.Fatalf("status = %q", result.Status)
	}
	if result.Credentials.DeviceToken != "dev_live" {
		t.Fatalf("device token = %q", result.Credentials.DeviceToken)
	}
	if result.Credentials.NodeName != "Office Mac" {
		t.Fatalf("node name = %q", result.Credentials.NodeName)
	}
}

func TestServiceStoreRelayNodeNameFromHandshake(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)

	svc, err := NewService(":7331", false)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	creds := RelayCredentials{
		DeviceToken: "dev_live",
		NodeID:      "node_live",
		Endpoint:    "wss://relay.example.com/ws/connector",
	}
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set(relayNodeNameHeader, "Renamed Mac")

	svc.storeRelayNodeName(creds, resp)

	got, err := svc.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Relay.NodeName != "Renamed Mac" {
		t.Fatalf("node name = %q", got.Relay.NodeName)
	}
}

func TestServiceStoreRelayNodeNameInvokesHook(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)
	t.Setenv("APPDATA", configRoot)

	svc, err := NewService(":7331", false)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	var hooked string
	svc.SetNodeNameChangedHook(func(name string) {
		hooked = name
	})

	creds := RelayCredentials{
		DeviceToken: "dev_live",
		NodeID:      "node_live",
		Endpoint:    "wss://relay.example.com/ws/connector",
	}
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set(relayNodeNameHeader, "Renamed Mac")

	svc.storeRelayNodeName(creds, resp)

	if hooked != "Renamed Mac" {
		t.Fatalf("hook name = %q, want Renamed Mac", hooked)
	}
	got, err := svc.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Relay.NodeName != "Renamed Mac" {
		t.Fatalf("node name = %q", got.Relay.NodeName)
	}
}

func TestRelayStatusErrorTranslatesCodes(t *testing.T) {
	relayStatusErrorTest := func(code string) string {
		resp := &http.Response{
			StatusCode: http.StatusConflict,
			Status:     "409 Conflict",
			Body:       io.NopCloser(strings.NewReader(`{"error":"` + code + `"}`)),
		}
		return relayStatusError(resp)
	}
	if got := relayStatusErrorTest("node_name_taken"); !strings.Contains(got, "already in use") {
		t.Fatalf("node_name_taken message = %q", got)
	}
	if got := relayStatusErrorTest("password_length"); !strings.Contains(got, "4-128") {
		t.Fatalf("password_length message = %q", got)
	}
}

func TestManagerStartBindingGeneratesPendingCode(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)

	manager, err := NewManager(":7331", false, "https://relay.example.com", false)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	status := manager.Status()
	if status.PendingCode != "" {
		t.Fatalf("start should not generate pending code, got %q", status.PendingCode)
	}
	status, err = manager.StartBinding()
	if err != nil {
		t.Fatalf("StartBinding() error = %v", err)
	}
	if status.PendingCode == "" {
		t.Fatal("expected pending code")
	}
	if status.Bound {
		t.Fatal("expected unbound status")
	}
	if status.RelayBaseURL != "https://relay.example.com" {
		t.Fatalf("relay base url = %q", status.RelayBaseURL)
	}
	if status.NodeName == "" {
		t.Fatal("expected node name")
	}
}

func TestManagerNoRelayerDoesNotGeneratePendingCode(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)

	manager, err := NewManager(":7331", true, "https://relay.example.com", false)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	status := manager.Status()
	if status.PendingCode != "" {
		t.Fatalf("expected no pending code, got %q", status.PendingCode)
	}
	if !status.NoRelayer {
		t.Fatal("expected no-relayer status")
	}
}

func TestManagerPollConfirmedStartsRelay(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)

	manager, err := NewManager(":7331", false, "https://relay.example.com", false)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	requests := make(chan string, 8)
	manager.service.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests <- req.URL.String()
			switch {
			case strings.HasPrefix(req.URL.String(), "https://relay.example.com/api/bind/poll?code=pc_"):
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"status":"confirmed","device_token":"dev_live","node_id":"node_live","node_name":"Office Mac","endpoint":"wss://relay.example.com/ws/connector"}`)),
				}, nil
			case req.URL.String() == "http://localhost:7331/health":
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     http.Header{},
					Body:       io.NopCloser(strings.NewReader("ok")),
				}, nil
			default:
				return nil, context.Canceled
			}
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := manager.StartBinding(); err != nil {
		t.Fatalf("StartBinding() error = %v", err)
	}

	timeout := time.After(4 * time.Second)
	for {
		select {
		case raw := <-requests:
			if raw == "http://localhost:7331/health" {
				creds, err := manager.service.store.Load()
				if err != nil {
					t.Fatalf("Load() error = %v", err)
				}
				if creds.Relay.NodeID != "node_live" {
					t.Fatalf("node id = %q", creds.Relay.NodeID)
				}
				if creds.Relay.NodeName != "Office Mac" {
					t.Fatalf("node name = %q", creds.Relay.NodeName)
				}
				return
			}
		case <-timeout:
			t.Fatal("relay did not start after confirmed poll")
		}
	}
}

func TestManagerPollTerminalBindStatusStopsPolling(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)

	manager, err := NewManager(":7331", false, "https://relay.example.com", false)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	requests := make(chan string, 4)
	manager.service.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.HasPrefix(req.URL.String(), "https://relay.example.com/api/bind/poll?code=pc_") {
				requests <- req.URL.String()
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"status":"expired"}`)),
				}, nil
			}
			return nil, context.Canceled
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := manager.StartBinding(); err != nil {
		t.Fatalf("StartBinding() error = %v", err)
	}
	firstPendingCode := manager.Status().PendingCode
	if firstPendingCode == "" {
		t.Fatal("expected initial pending code")
	}

	timeout := time.After(5 * time.Second)
	for {
		select {
		case <-requests:
			status := manager.Status()
			if status.LastError != "expired" {
				continue
			}
			if status.PendingCode == "" {
				return
			}
			t.Fatalf("expected pending code to clear after expired status, got first=%q current=%q", firstPendingCode, status.PendingCode)
		case <-timeout:
			t.Fatal("pending code did not clear after expired bind status")
		}
	}
}

func TestManagerEmptyRelayBaseDisablesRelay(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)

	manager, err := NewManager(":7331", false, "", false)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	status := manager.Status()
	if !status.NoRelayer {
		t.Fatalf("expected noRelayer to be true when relay base is empty, got %v", status.NoRelayer)
	}
	if status.RelayBaseURL != "" {
		t.Fatalf("relay base url = %q, want empty", status.RelayBaseURL)
	}
	if status.PendingCode != "" {
		t.Fatalf("expected no pending code before explicit bind start, got %q", status.PendingCode)
	}
}

func TestManagerPermanentRelayErrorClearsCredentialsAndWaitsForExplicitRebind(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)

	manager, err := NewManager(":7331", false, "https://relay.example.com", false)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err := manager.service.store.Save(Credentials{
		Relay: RelayCredentials{
			DeviceToken: "dev_live",
			NodeID:      "node_live",
			Endpoint:    "wss://relay.example.com/ws/connector",
		},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	manager.service.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.HasPrefix(req.URL.String(), "http://localhost:7331/health"):
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     http.Header{},
					Body:       io.NopCloser(strings.NewReader("ok")),
				}, nil
			default:
				return nil, nil
			}
		}),
	}

	manager.handlePermanentRelayError(&relayDialError{
		statusCode: http.StatusUnauthorized,
		status:     "401 Unauthorized",
		errorCode:  "device_token_invalid",
		err:        errors.New("websocket: bad handshake"),
	})

	status := manager.Status()
	if status.Bound {
		t.Fatal("expected unbound status after permanent relay error")
	}
	if status.PendingCode != "" {
		t.Fatalf("expected no pending code before explicit rebind, got %q", status.PendingCode)
	}
	if !strings.Contains(status.LastError, "device_token_invalid") {
		t.Fatalf("last error = %q", status.LastError)
	}
	creds, err := manager.service.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if creds.Relay.DeviceToken != "" || creds.Relay.Endpoint != "" {
		t.Fatalf("expected cleared credentials, got %+v", creds.Relay)
	}
}

func TestManagerStartClearsCredentialsWhenRelayBaseChanges(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)

	manager, err := NewManager(":7331", false, "https://relay-new.example.com", false)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err := manager.service.store.Save(Credentials{
		Relay: RelayCredentials{
			DeviceToken: "dev_live",
			NodeID:      "node_live",
			Endpoint:    "wss://relay-old.example.com/ws/connector",
		},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	status := manager.Status()
	if status.Bound {
		t.Fatal("expected unbound status after relay base changed")
	}
	if status.PendingCode != "" {
		t.Fatalf("expected no pending code before explicit bind start, got %q", status.PendingCode)
	}
	if !strings.Contains(status.LastError, "rebinding required") {
		t.Fatalf("last error = %q", status.LastError)
	}

	creds, err := manager.service.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if creds.Relay.DeviceToken != "" || creds.Relay.Endpoint != "" || creds.Relay.NodeID != "" {
		t.Fatalf("expected cleared credentials after relay base changed, got %+v", creds.Relay)
	}
}

func TestIsPermanentRelayErrorDetectsHandshakeStatus(t *testing.T) {
	if !isPermanentRelayError(&relayDialError{
		statusCode: http.StatusUnauthorized,
		status:     "401 Unauthorized",
		errorCode:  "device_token_invalid",
		err:        errors.New("websocket: bad handshake"),
	}) {
		t.Fatal("expected device_token_invalid to be treated as permanent")
	}
	if !isPermanentRelayError(&relayDialError{
		statusCode: http.StatusForbidden,
		status:     "403 Forbidden",
		errorCode:  "access_password_invalid",
		err:        errors.New("websocket: bad handshake"),
	}) {
		t.Fatal("expected access_password_invalid to be treated as permanent")
	}
	if isPermanentRelayError(&relayDialError{
		statusCode: http.StatusUnauthorized,
		status:     "401 Unauthorized",
		errorCode:  "device_token_required",
		err:        errors.New("websocket: bad handshake"),
	}) {
		t.Fatal("device_token_required should not be treated as permanent")
	}
	if isPermanentRelayError(&relayDialError{
		statusCode: http.StatusNotFound,
		status:     "404 Not Found",
		errorCode:  "not_found",
		err:        errors.New("websocket: bad handshake"),
	}) {
		t.Fatal("404 handshake should not be treated as permanent")
	}
	if isPermanentRelayError(errors.New("websocket: bad handshake")) {
		t.Fatal("plain bad handshake without status should not be treated as permanent")
	}
}

func TestManagerPasswordMismatchPermanentErrorKeepsBind(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)
	t.Setenv("APPDATA", configRoot)

	manager, err := NewManager(":7331", false, "https://relay.example.com", false)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err := manager.service.store.Save(Credentials{
		Relay: RelayCredentials{
			DeviceToken:    "dev_live",
			NodeID:         "node_live",
			NodeName:       "Office Mac",
			Endpoint:       "wss://relay.example.com/ws/connector",
			AccessPassword: "secret-old",
		},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	manager.handlePermanentRelayError(&relayDialError{
		statusCode: http.StatusForbidden,
		status:     "403 Forbidden",
		errorCode:  "access_password_invalid",
		err:        errors.New("websocket: bad handshake"),
	})

	status := manager.Status()
	if !status.Bound {
		t.Fatal("expected still bound after access password mismatch")
	}
	if status.PasswordSet {
		t.Fatal("expected password to be cleared after access password mismatch")
	}
	if !strings.Contains(status.LastError, "access password mismatch") {
		t.Fatalf("last error = %q", status.LastError)
	}
	creds, err := manager.service.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if creds.Relay.DeviceToken == "" || creds.Relay.Endpoint == "" {
		t.Fatalf("expected credentials to be kept, got %+v", creds.Relay)
	}
	if creds.Relay.AccessPassword != "" {
		t.Fatalf("expected access password cleared, got %q", creds.Relay.AccessPassword)
	}
}

func TestManagerReconcilesExternalRelayConfigChange(t *testing.T) {
	manager, err := NewManager(":7331", false, "https://relay.example.com", false)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	// Guard against the shared real config dir: point the store at a temp file
	// so the watcher observes only this test's writes.
	path := filepath.Join(t.TempDir(), "relay.json")
	manager.service.store.filePath = path
	if err := manager.service.store.SaveRelayBase("https://relay.example.com"); err != nil {
		t.Fatalf("SaveRelayBase() error = %v", err)
	}
	if err := manager.service.store.SaveNodeName("Original Name"); err != nil {
		t.Fatalf("SaveNodeName() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"relay_base_url":"https://relay.example.com","node_name":"External Edit"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	deadline := time.After(4 * time.Second)
	for {
		if manager.Status().NodeName == "External Edit" {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("external node name change was not applied, node name = %q", manager.Status().NodeName)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestNewRelayDialErrorReadsErrorCode(t *testing.T) {
	err := newRelayDialError(&http.Response{
		StatusCode: http.StatusUnauthorized,
		Status:     "401 Unauthorized",
		Body:       io.NopCloser(strings.NewReader(`{"error":"device_token_invalid"}`)),
	}, errors.New("websocket: bad handshake"))

	if !isPermanentRelayError(err) {
		t.Fatalf("expected parsed device_token_invalid to be permanent, got %v", err)
	}
}

func TestLocalTargetURL(t *testing.T) {
	target, err := localTargetURL("http://127.0.0.1:7331", mustParseURL("/api/file?root=a"))
	if err != nil {
		t.Fatalf("localTargetURL() error = %v", err)
	}
	if target.String() != "http://127.0.0.1:7331/api/file?root=a" {
		t.Fatalf("localTargetURL() = %s", target.String())
	}
}

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
