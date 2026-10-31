package main

import (
	"encoding/json"
	"testing"
)

func TestNodeStatsCachedAndSent(t *testing.T) {
	a := haCurrentNodeStats()
	if a == nil || a.Collected == 0 {
		t.Fatal("no stats")
	}
	if b := haCurrentNodeStats(); b != a {
		t.Fatal("stats should be reused within the cache window")
	}
	// The figures travel with the HA status to the other nodes
	js, err := json.Marshal(haSyncPeerInfo{Node: "n1", Stats: a})
	if err != nil {
		t.Fatal(err)
	}
	var back haSyncPeerInfo
	if err := json.Unmarshal(js, &back); err != nil || back.Stats == nil || back.Stats.Collected != a.Collected {
		t.Fatalf("stats lost in the peer status: %s", js)
	}
	// An older node sends no stats; that must decode cleanly
	var old haSyncPeerInfo
	if err := json.Unmarshal([]byte(`{"node":"n2","role":"BACKUP"}`), &old); err != nil || old.Stats != nil {
		t.Fatalf("status from an older node: %+v %v", old, err)
	}
}
