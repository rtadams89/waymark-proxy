package main

import "testing"

func TestHaPeerRowHidesStaleState(t *testing.T) {
	// Never polled
	row := map[string]any{}
	if haPeerRow(row, nil) != nil || row["reachable"] != false {
		t.Fatalf("unpolled node: %v", row)
	}

	// Answered the latest poll: current state
	row = map[string]any{}
	info := haPeerRow(row, &haPeerStatus{Info: haSyncPeerInfo{Role: "BACKUP"}, LastSeen: 100, LastPoll: 100})
	if info == nil || info.Role != "BACKUP" || row["reachable"] != true || row["lastRole"] != nil {
		t.Fatalf("reachable node: %v %v", info, row)
	}

	// Was active, now unreachable: only the last role, as history
	row = map[string]any{}
	info = haPeerRow(row, &haPeerStatus{Info: haSyncPeerInfo{Role: "MASTER", VIPHeld: true}, LastSeen: 100, LastPoll: 160, Err: "connection refused"})
	if info != nil {
		t.Fatalf("unreachable node must not report a current state: %v", info)
	}
	if row["reachable"] != false || row["lastRole"] != "MASTER" || row["lastSeen"] != int64(100) || row["role"] != nil {
		t.Fatalf("unreachable node row: %v", row)
	}

	// Unreachable and never seen: no last role
	row = map[string]any{}
	haPeerRow(row, &haPeerStatus{LastPoll: 160, Err: "timeout"})
	if _, ok := row["lastRole"]; ok {
		t.Fatalf("never-seen node should have no lastRole: %v", row)
	}
}
