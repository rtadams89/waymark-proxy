//go:build !mipsle && !riscv64

package database

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"imuslab.com/zoraxy/mod/database/dbinc"
)

func TestFileTable(t *testing.T) {
	dir := t.TempDir()
	db, err := NewDatabase(filepath.Join(dir, "sys.db"), dbinc.BackendBoltDB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	db.Write("other", "x", "stays")

	conf := filepath.Join(dir, "conf", "listener.json")
	if err := os.MkdirAll(filepath.Dir(conf), 0775); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(conf, []byte(`{"inbound": 8443, "usetls": true, "devMode": true}`), 0640)
	if err := db.UseFileTable("settings", conf, 0640); err != nil {
		t.Fatal(err)
	}
	if db.Backend.TableExists("settings") {
		t.Fatal("a file table must not create a database table")
	}
	var port int
	db.Read("settings", "inbound", &port)
	if port != 8443 {
		t.Fatalf("read from the file: got %d", port)
	}

	// Missing key: no error, destination unchanged (as with bolt)
	timeout := 120
	if err := db.Read("settings", "idleTimeout", &timeout); err != nil || timeout != 120 {
		t.Fatalf("missing key: err=%v value=%d", err, timeout)
	}
	if db.KeyExists("settings", "idleTimeout") || !db.KeyExists("settings", "usetls") || !db.TableExists("settings") {
		t.Fatal("KeyExists / TableExists wrong")
	}

	// Raw JSON values (HA sync, key moves) are stored as they are
	db.Write("settings", "raw", json.RawMessage(`{"a":1}`))
	var m map[string]int
	db.Read("settings", "raw", &m)
	if m["a"] != 1 {
		t.Fatal("raw value not stored as JSON")
	}

	rows, _ := db.ListTable("settings")
	keys := []string{}
	for _, kv := range rows {
		keys = append(keys, string(kv[0]))
	}
	if strings.Join(keys, ",") != "devMode,inbound,raw,usetls" {
		t.Fatalf("ListTable keys %v", keys)
	}

	// Changed on disk (HA sync, hand edit): picked up
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(conf, []byte(`{"inbound": 9000}`), 0640)
	db.Read("settings", "inbound", &port)
	if port != 9000 || db.KeyExists("settings", "usetls") {
		t.Fatalf("file change not picked up: %d", port)
	}

	// Unreadable file: errors, and is not overwritten
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(conf, []byte(`{"inbound": `), 0640)
	if err := db.Write("settings", "usetls", true); err == nil {
		t.Fatal("write over an invalid file should fail")
	}
	if b, _ := os.ReadFile(conf); string(b) != `{"inbound": ` {
		t.Fatal("invalid file was overwritten")
	}
	os.WriteFile(conf, []byte(`{"inbound": 1}`), 0640)

	db.Delete("settings", "inbound")
	if db.KeyExists("settings", "inbound") {
		t.Fatal("delete")
	}
	var onDisk map[string]any
	b, _ := os.ReadFile(conf)
	if json.Unmarshal(b, &onDisk) != nil || len(onDisk) != 0 {
		t.Fatalf("file after delete: %s", b)
	}

	// Other tables still in the database
	var s string
	db.Read("other", "x", &s)
	if s != "stays" {
		t.Fatal("unrouted table affected")
	}

	// A new install: the file is created on first write
	ui := filepath.Join(dir, "conf", "webserv.json")
	if err := db.UseFileTable("webserv", ui, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ui); err == nil {
		t.Fatal("file created without any rows")
	}
	db.Write("webserv", "port", 5487)
	if info, err := os.Stat(ui); err != nil || (info.Mode().Perm() != 0600 && os.PathSeparator == '/') {
		t.Fatalf("file not written with its mode: %v", err)
	}
}
