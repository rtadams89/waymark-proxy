package hacluster

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// memStore is an in-memory Store that encodes values like Waymark's database.
type memStore map[string]map[string][]byte

func (m memStore) NewTable(t string) error {
	if m[t] == nil {
		m[t] = map[string][]byte{}
	}
	return nil
}
func (m memStore) TableExists(t string) bool { _, ok := m[t]; return ok }
func (m memStore) Delete(t, k string) error  { delete(m[t], k); return nil }
func (m memStore) Write(t, k string, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m.NewTable(t)
	m[t][k] = b
	return nil
}
func (m memStore) ListTable(t string) ([][][]byte, error) {
	keys := []string{}
	for k := range m[t] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := [][][]byte{}
	for _, k := range keys {
		out = append(out, [][]byte{[]byte(k), m[t][k]})
	}
	return out, nil
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	for rel, data := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0755)
		if err := os.WriteFile(p, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSyncRoundTrip(t *testing.T) {
	active, standby := t.TempDir(), t.TempDir()
	writeFiles(t, active, map[string]string{
		"proxy/a.example.com.config": `{"rule":"a"}`,
		"proxy/b.example.com.config": `{"rule":"b"}`,
		"certs/a.example.com.pem":    "CERT",
		"certs/a.example.com.key":    "KEY",
		"rules/pathrules/x.json":     "{}",
		"acme_conf.json":             `{"enabled":true}`,
		"geodb/ip2country.csv":       "big per-node file",
		"log_conf.json":              "{}",
		"version":                    "3",
	})
	writeFiles(t, standby, map[string]string{
		"proxy/a.example.com.config":    `{"rule":"old a"}`,
		"proxy/gone.example.com.config": `{"rule":"deleted on the active node"}`,
		"trusted_proxies.json":          "[]",
		"geodb/ip2country.csv":          "standby's own copy",
		"log_conf.json":                 `{"standby":true}`,
	})
	adb, sdb := memStore{}, memStore{}
	adb.Write("auth", "passhash/admin", "hash1")
	adb.Write("auth", "sessionkey", "k1")
	adb.Write("custom_fork", "ha_cluster", map[string]any{"enabled": true})
	adb.Write("custom_fork", "asset_cache_daily", "active's stats")
	adb.Write("stats", "2026_10_16", "active's stats")
	sdb.Write("auth", "passhash/olduser", "x")
	sdb.Write("custom_fork", "asset_cache_daily", "standby's stats")
	sdb.Write("stats", "2026_10_16", "standby's stats")

	c, err := BuildContent(active, adb)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Files["geodb/ip2country.csv"]; ok {
		t.Fatal("geodb must not sync")
	}
	if _, ok := c.Files["log_conf.json"]; ok {
		t.Fatal("log config must not sync")
	}
	if _, ok := c.Tables["custom_fork"]["asset_cache_daily"]; ok {
		t.Fatal("per-node key synced")
	}
	if _, ok := c.Tables["stats"]; ok {
		t.Fatal("stats synced")
	}
	snap := &Snapshot{Hash: c.Hash(), Content: *c}

	// Through JSON, as on the wire
	wire, _ := json.Marshal(snap)
	var got Snapshot
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ApplyContent(standby, sdb, &got.Content); err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string { b, _ := os.ReadFile(filepath.Join(standby, rel)); return string(b) }
	if read("proxy/a.example.com.config") != `{"rule":"a"}` || read("proxy/b.example.com.config") != `{"rule":"b"}` || read("certs/a.example.com.key") != "KEY" || read("acme_conf.json") != `{"enabled":true}` {
		t.Fatal("files not applied")
	}
	if _, err := os.Stat(filepath.Join(standby, "proxy/gone.example.com.config")); err == nil {
		t.Fatal("rule deleted on the active node is still on the standby")
	}
	if _, err := os.Stat(filepath.Join(standby, "trusted_proxies.json")); err == nil {
		t.Fatal("file missing on the active node is still on the standby")
	}
	if read("geodb/ip2country.csv") != "standby's own copy" || read("log_conf.json") != `{"standby":true}` {
		t.Fatal("per-node files were touched")
	}
	if string(sdb["auth"]["passhash/admin"]) != `"hash1"` || sdb["auth"]["passhash/olduser"] != nil {
		t.Fatalf("auth table not mirrored: %v", sdb["auth"])
	}
	if string(sdb["custom_fork"]["asset_cache_daily"]) != `"standby's stats"` || string(sdb["stats"]["2026_10_16"]) != `"standby's stats"` {
		t.Fatal("per-node data was touched")
	}
	leftovers, _ := filepath.Glob(filepath.Join(standby, ".hasync-*"))
	if len(leftovers) > 0 {
		t.Fatalf("staging folders left behind: %v", leftovers)
	}
	// Same configuration, same hash
	c2, _ := BuildContent(standby, sdb)
	if c2.Hash() != c.Hash() {
		t.Fatal("hash differs after apply")
	}
}

func TestSyncValidateRejects(t *testing.T) {
	bad := []func(c *SyncContent){
		func(c *SyncContent) { c.Files["../etc/passwd"] = SyncFile{Data: []byte("x")} },
		func(c *SyncContent) { c.Files["proxy/../../x"] = SyncFile{Data: []byte("x")} },
		func(c *SyncContent) { c.Files["/abs/proxy/x"] = SyncFile{Data: []byte("x")} },
		func(c *SyncContent) { c.Files["geodb/x"] = SyncFile{Data: []byte("x")} },
		func(c *SyncContent) { c.Files["proxy\\x"] = SyncFile{Data: []byte("x")} },
		func(c *SyncContent) { c.Files["proxy/.hasync-new-x/y"] = SyncFile{Data: []byte("x")} },
		func(c *SyncContent) { c.Tables["stats"] = map[string]json.RawMessage{"k": json.RawMessage(`1`)} },
		func(c *SyncContent) {
			c.Tables["custom_fork"] = map[string]json.RawMessage{"asset_cache_daily": json.RawMessage(`1`)}
		},
		func(c *SyncContent) { c.Tables["auth"] = map[string]json.RawMessage{"k": json.RawMessage(`{bad`)} },
	}
	for i, mod := range bad {
		c := &SyncContent{Files: map[string]SyncFile{}, Tables: map[string]map[string]json.RawMessage{}}
		mod(c)
		s := &Snapshot{Hash: c.Hash(), Content: *c}
		if err := s.Validate(); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	c := &SyncContent{Files: map[string]SyncFile{"proxy/a": {Data: []byte("x")}}, Tables: map[string]map[string]json.RawMessage{}}
	s := &Snapshot{Hash: c.Hash(), Content: *c}
	s.Content.Files["proxy/a"] = SyncFile{Data: []byte("tampered")}
	if err := s.Validate(); err == nil {
		t.Error("tampered snapshot accepted")
	}
}

func startSyncServer(t *testing.T, key string, allowed bool) string {
	cert, err := SelfSignedTLS()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", SyncServerTLSConfig(cert))
	if err != nil {
		t.Fatal(err)
	}
	h := &SyncHandler{
		Key:     func() []byte { return KeyBytes(key) },
		Allowed: func(net.IP) bool { return allowed },
		State:   func() any { return map[string]string{"role": "MASTER"} },
		Snapshot: func() (*Snapshot, error) {
			c := &SyncContent{Files: map[string]SyncFile{"proxy/a": {Data: []byte("x")}}, Tables: map[string]map[string]json.RawMessage{}}
			return &Snapshot{Hash: c.Hash(), Content: *c}, nil
		},
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

func TestSyncAuth(t *testing.T) {
	key := NewClusterKey()
	addr := startSyncServer(t, key, true)
	var st map[string]string
	if err := SyncFetch(addr, KeyBytes(key), "/waymark-ha/v1/state", &st, 3*time.Second); err != nil || st["role"] != "MASTER" {
		t.Fatalf("state: %v %v", st, err)
	}
	var snap Snapshot
	if err := SyncFetch(addr, KeyBytes(key), "/waymark-ha/v1/snapshot", &snap, 3*time.Second); err != nil || snap.Validate() != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// Wrong key: refused
	if err := SyncFetch(addr, KeyBytes(NewClusterKey()), "/waymark-ha/v1/state", &st, 3*time.Second); !errors.Is(err, ErrSyncAuth) {
		t.Fatalf("wrong key: %v", err)
	}
	// Not a cluster node: refused
	addr2 := startSyncServer(t, key, false)
	if err := SyncFetch(addr2, KeyBytes(key), "/waymark-ha/v1/state", &st, 3*time.Second); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("unlisted address: %v", err)
	}
	// A proof made on one TLS session is useless on another (replay)
	conn, _ := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13})
	cs := conn.ConnectionState()
	proof, _ := syncProof(KeyBytes(key), "client", &cs)
	conn.Close()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	req, _ := http.NewRequest("GET", "https://"+addr+"/waymark-ha/v1/state", nil)
	req.Header.Set(SyncAuthHeader, proof)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed proof: %v %v", resp, err)
	}
}

func TestConfigSyncValidation(t *testing.T) {
	base := func() *Config {
		return &Config{Enabled: true, VIP: "10.0.0.5", Prefix: 24, RouterID: 51, Nodes: []Node{
			{Name: "a", Address: "10.0.0.10", Interface: "eth0", Priority: 150},
			{Name: "b", Address: "10.0.0.11", Interface: "eth0", Priority: 100}}}
	}
	c := base()
	c.SyncEnabled = true
	if _, err := c.Validate(); err == nil {
		t.Fatal("sync without a key accepted")
	}
	c.ClusterKey = strings.ToUpper(NewClusterKey())
	v, err := c.Validate()
	if err != nil || v.SyncPort != DefaultSyncPort || v.ClusterKey != strings.ToLower(c.ClusterKey) {
		t.Fatalf("%+v %v", v, err)
	}
	c.SyncPort = 70000
	if _, err := c.Validate(); err == nil {
		t.Fatal("bad port accepted")
	}
}
