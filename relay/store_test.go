package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsNodeNameTaken(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.saveDevice(&Device{
		NodeID:   "nd_aaaa",
		NodeName: "Office Mac",
		Endpoint: "wss://relay.test/ws",
		BoundAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("saveDevice() error = %v", err)
	}
	if err := store.saveDevice(&Device{
		NodeID:   "nd_bbbb",
		NodeName: "客厅电脑",
		Endpoint: "wss://relay.test/ws",
		BoundAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("saveDevice() error = %v", err)
	}

	cases := []struct {
		name    string
		exclude string
		want    bool
	}{
		{"Office Mac", "", true},
		{"office mac", "", true}, // case-insensitive
		{"客厅电脑", "", true},
		{"客厅电脑", "nd_bbbb", false}, // current device keeps its own name
		{"Living Room", "", false},
		{"home-node", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		if got := store.isNodeNameTaken(c.name, c.exclude); got != c.want {
			t.Fatalf("isNodeNameTaken(%q, %q) = %v, want %v", c.name, c.exclude, got, c.want)
		}
	}
}

func TestIsNodeNameTakenWithNoDevices(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if store.isNodeNameTaken("any-name", "") {
		t.Fatal("expected no name to be taken with empty device list")
	}
}

func TestStoreMigratesLegacyFilesIntoUnifiedState(t *testing.T) {
	dir := t.TempDir()
	legacy := map[string]string{
		"binds.json":    `{"bc_1":{"code":"bc_1","status":"pending"}}`,
		"devices.json":  `{"nd_1":{"node_id":"nd_1","node_name":"Legacy","device_token":"dt_1","endpoint":"wss://relay.test/ws","bound_at":"2024-01-01T00:00:00Z"}}`,
		"services.json": `{"web@nd_1":{"slug":"web","node_id":"nd_1","name":"Web","enabled":true}}`,
	}
	for name, content := range legacy {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write legacy %s error = %v", name, err)
		}
	}

	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if dev := store.getDeviceByNode("nd_1"); dev == nil || dev.NodeName != "Legacy" {
		t.Fatalf("legacy device not migrated: %+v", dev)
	}
	if bind := store.getBind("bc_1"); bind == nil || bind.Status != "pending" {
		t.Fatalf("legacy bind not migrated: %+v", bind)
	}
	if svc := store.getService("web", "nd_1"); svc == nil || !svc.Enabled {
		t.Fatalf("legacy service not migrated: %+v", svc)
	}

	statePath := filepath.Join(dir, stateFileName)
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("unified state file missing: %v", err)
	}
	for name := range legacy {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("legacy file %s should be removed, stat err = %v", name, err)
		}
	}

	// A fresh store must reload the persisted unified state.
	reloaded, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore() reload error = %v", err)
	}
	if dev := reloaded.getDeviceByNode("nd_1"); dev == nil || dev.NodeName != "Legacy" {
		t.Fatalf("unified state not reloaded: %+v", dev)
	}
}
