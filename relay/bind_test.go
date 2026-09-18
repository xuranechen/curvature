package main

import (
	"testing"
	"time"
)

func TestDefaultNodeNameForBindAvoidsCollisions(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	b := NewBindService(store, "https://relay.test")

	if got := defaultNodeNameForBind(b); got != "home-node" {
		t.Fatalf("first name = %q, want home-node", got)
	}
	if err := store.saveDevice(&Device{
		NodeID:   "nd_aaaa",
		NodeName: "home-node",
		Endpoint: "wss://relay.test/ws",
		BoundAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("saveDevice() error = %v", err)
	}
	if got := defaultNodeNameForBind(b); got != "home-node-2" {
		t.Fatalf("second name = %q, want home-node-2", got)
	}
	if err := store.saveDevice(&Device{
		NodeID:   "nd_bbbb",
		NodeName: "home-node-2",
		Endpoint: "wss://relay.test/ws",
		BoundAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("saveDevice() error = %v", err)
	}
	if got := defaultNodeNameForBind(b); got != "home-node-3" {
		t.Fatalf("third name = %q, want home-node-3", got)
	}
}
