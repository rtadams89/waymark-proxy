package hacluster

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLiveChanges(t *testing.T) {
	base := func() *SyncContent {
		return &SyncContent{
			Files:  map[string]SyncFile{"ui.json": {Mode: 0640, Data: []byte(`{"a":1}`)}, "listener.json": {Mode: 0640, Data: []byte(`{}`)}},
			Tables: map[string]map[string]json.RawMessage{"auth": {"u": json.RawMessage(`"x"`)}},
		}
	}
	local, in := base(), base()
	if c, ok := LiveChanges(local, in); !ok || len(c) != 0 {
		t.Fatalf("equal content: %v %v", c, ok)
	}
	in.Files["ui.json"] = SyncFile{Mode: 0640, Data: []byte(`{"a":2}`)}
	if c, ok := LiveChanges(local, in); !ok || len(c) != 1 || c[0] != "ui.json" {
		t.Fatalf("ui.json change: %v %v", c, ok)
	}
	in.Files["listener.json"] = SyncFile{Mode: 0640, Data: []byte(`{"port":1}`)}
	if _, ok := LiveChanges(local, in); ok {
		t.Fatal("a listener change must need the restart")
	}
	in = base()
	in.Tables["auth"]["u"] = json.RawMessage(`"y"`)
	if _, ok := LiveChanges(local, in); ok {
		t.Fatal("a table change must need the restart")
	}
	in = base()
	delete(in.Files, "listener.json")
	if _, ok := LiveChanges(local, in); ok {
		t.Fatal("a removed file must need the restart")
	}

	dir := t.TempDir()
	in = base()
	in.Files["ui.json"] = SyncFile{Mode: 0640, Data: []byte(`{"a":3}`)}
	if err := ApplyLiveEntries(dir, in, []string{"ui.json"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "ui.json")); string(b) != `{"a":3}` {
		t.Fatalf("ui.json not written: %s", b)
	}
	if err := ApplyLiveEntries(dir, in, []string{"listener.json"}); err == nil {
		t.Fatal("only live entries may be applied this way")
	}
}
