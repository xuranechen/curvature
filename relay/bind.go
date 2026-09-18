package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const bindTTL = 15 * time.Minute

var (
	errBindNotFound = errors.New("bind_not_found")
	errBindExpired  = errors.New("bind_expired")
)

type BindService struct {
	store *Store
	base  string
}

func NewBindService(store *Store, base string) *BindService {
	return &BindService{store: store, base: strings.TrimSuffix(base, "/")}
}

// poll handles GET /api/bind/poll
func (b *BindService) poll(code, deviceID, purpose, nodeName string) (map[string]any, error) {
	code = strings.TrimSpace(code)
	deviceID = strings.TrimSpace(deviceID)
	nodeName = strings.TrimSpace(nodeName)
	if code == "" {
		return nil, errors.New("code_required")
	}
	bind := b.store.getBind(code)
	now := time.Now().UTC()
	if bind == nil {
		bind = &Bind{
			Code:      code,
			DeviceID:  deviceID,
			Purpose:   strings.TrimSpace(purpose),
			NodeName:  nodeName,
			Status:    "pending",
			CreatedAt: now,
		}
		if err := b.store.createBind(bind); err != nil {
			return nil, err
		}
		return map[string]any{
			"status":             "pending",
			"next_poll_after_ms": int64(3000),
		}, nil
	}
	if now.Sub(bind.CreatedAt) > bindTTL && bind.Status == "pending" {
		bind.Status = "expired"
		_ = b.store.updateBind(bind)
	}
	switch bind.Status {
	case "pending":
		return map[string]any{
			"status":             "pending",
			"next_poll_after_ms": int64(3000),
		}, nil
	case "confirmed":
		dev := b.store.getDeviceByNode(bind.NodeID)
		if dev == nil {
			return nil, errors.New("device_not_found")
		}
		out := map[string]any{
			"status":             "confirmed",
			"device_token":       dev.DeviceToken,
			"node_id":            dev.NodeID,
			"node_name":          dev.NodeName,
			"endpoint":           dev.Endpoint,
			"next_poll_after_ms": int64(0),
		}
		return out, nil
	case "claimed", "revoked":
		return map[string]any{"status": bind.Status}, nil
	default:
		return map[string]any{"status": bind.Status}, nil
	}
}

// confirm handles POST /api/bind/confirm (called from the /bind web page)
func (b *BindService) confirm(code string) (map[string]any, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errBindNotFound
	}
	bind := b.store.getBind(code)
	if bind == nil {
		return nil, errBindNotFound
	}
	if bind.Status != "pending" {
		return nil, errors.New("bind_not_pending")
	}
	if time.Now().UTC().Sub(bind.CreatedAt) > bindTTL {
		bind.Status = "expired"
		_ = b.store.updateBind(bind)
		return nil, errBindExpired
	}

	nodeID := "nd_" + randHex(32)
	deviceToken := "dt_" + randToken()
	nodeName := strings.TrimSpace(bind.NodeName)
	if nodeName == "" {
		nodeName = defaultNodeNameForBind(b)
	}
	for i := 0; i < 100 && b.store.isNodeNameTaken(nodeName, ""); i++ {
		nodeName = defaultNodeNameForBind(b)
	}

	dev := &Device{
		NodeID:      nodeID,
		NodeName:    nodeName,
		DeviceID:    bind.DeviceID,
		DeviceToken: deviceToken,
		Endpoint:    b.wsEndpoint(),
		BoundAt:     time.Now().UTC(),
	}
	if err := b.store.saveDevice(dev); err != nil {
		return nil, err
	}

	bind.Status = "confirmed"
	bind.NodeID = nodeID
	if err := b.store.updateBind(bind); err != nil {
		return nil, err
	}

	return map[string]any{
		"status":       "confirmed",
		"node_id":      nodeID,
		"node_name":    nodeName,
		"node_url":     b.nodeURL(nodeID),
		"endpoint":     dev.Endpoint,
		"device_token": deviceToken,
	}, nil
}

// status reports current bind state for the /bind web page
func (b *BindService) status(code string) (map[string]any, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errBindNotFound
	}
	bind := b.store.getBind(code)
	if bind == nil {
		return nil, errBindNotFound
	}
	out := map[string]any{
		"code":       bind.Code,
		"status":     bind.Status,
		"device_id":  bind.DeviceID,
		"node_name":  bind.NodeName,
		"purpose":    bind.Purpose,
		"created_at": bind.CreatedAt,
	}
	if bind.Status == "confirmed" && bind.NodeID != "" {
		out["node_url"] = b.nodeURL(bind.NodeID)
	}
	return out, nil
}

func (b *BindService) nodeURL(nodeID string) string {
	return b.base + "/n/" + url.PathEscape(nodeID) + "/"
}

func (b *BindService) wsEndpoint() string {
	u, err := url.Parse(b.base)
	if err != nil {
		return b.base + "/ws"
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = "/ws"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func randToken() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// randHex returns n lowercase hex characters. Node IDs are embedded in DNS
// hostnames, so they must stay lowercase and avoid "-" and "_".
func randHex(n int) string {
	const digits = "0123456789abcdef"
	buf := make([]byte, n)
	raw := make([]byte, (n+1)/2)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	for i := range buf {
		buf[i] = digits[raw[i/2]>>uint((i%2)*4)&0x0f]
	}
	return string(buf)
}

// defaultNodeNameForBind returns a default display name for a freshly bound
// node. A counter suffix avoids collisions when multiple devices bind without
// an explicit node name.
func defaultNodeNameForBind(b *BindService) string {
	base := "home-node"
	if !b.store.isNodeNameTaken(base, "") {
		return base
	}
	for i := 2; i <= 100; i++ {
		probe := base + "-" + strconv.Itoa(i)
		if !b.store.isNodeNameTaken(probe, "") {
			return probe
		}
	}
	return base + "-" + randHex(6)
}
