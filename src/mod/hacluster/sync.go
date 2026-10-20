package hacluster

/*
	sync.go (custom fork): configuration sync between HA nodes (phase 2).

	The standby pulls a snapshot of the active node's configuration: the
	config folder entries in SyncConfEntries and the database tables in
	SyncTables. The snapshot is staged and applied when Waymark next starts,
	before any module loads its configuration, so nothing has to be
	reloaded while running.

	Nodes authenticate each other with the cluster key over TLS 1.3. The TLS
	certificates are throwaway self-signed ones; instead both sides send an
	HMAC of the cluster key over the connection's exported keying material
	(RFC 5705/8446), so a proof is only valid on the TLS session it was made
	on and cannot be relayed by a man in the middle.
*/

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"imuslab.com/zoraxy/mod/utils"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SyncConfEntries are the config folder entries copied from the active
// node. Entries ending in ".json" are files; the rest are folders, mirrored
// exactly (files missing on the active node are removed).
var SyncConfEntries = []string{
	"proxy",                // HTTP proxy rules
	"l4proxy",              // TCP/UDP Layer 4 proxy rules
	"certs",                // TLS certificates and keys
	"redirect",             // redirection rules
	"access",               // access rules
	"rules",                // path rules
	"sso",                  // SSO provider settings
	"acme_conf.json",       // certificate auto renewal settings
	"plugin_groups.json",   // plugin tag groups
	"trusted_proxies.json", // trusted proxy addresses
	"log_conf.json",        // log rotation settings
	"log_outputs.json",     // log file and syslog outputs
}

// SyncTables are the database tables copied from the active node.
// Statistics, dashboard counters, the Geo-IP updater state, plugin state
// and live SSO sessions stay per node.
var SyncTables = []string{
	"settings", "auth", "acme", "acme_dns_profiles", "acmepref", "redirect",
	"oauth2", "authelia", "authentik", "auth_sso_forward",
	"zorxauth", "zorxauth_users", "webserv", "custom_ui", "custom_fork",
}

// SyncSkipKeys are per-node keys inside synced tables.
var SyncSkipKeys = map[string]map[string]bool{
	"custom_fork": {"asset_cache_daily": true},
}

const (
	maxSyncFileSize  = 16 << 20
	maxSyncTotalSize = 128 << 20
	syncEKMLabel     = "waymark-ha-sync-v1"
	// Headers carrying the two proofs
	SyncAuthHeader  = "X-Waymark-Ha-Auth"
	SyncProofHeader = "X-Waymark-Ha-Proof"
)

var clusterKeyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SyncFile is one config file.
type SyncFile struct {
	Mode uint32 `json:"m"`
	Data []byte `json:"d"`
}

// SyncContent is what is compared and applied.
type SyncContent struct {
	Files  map[string]SyncFile                   `json:"files"`  // path relative to the config folder, with "/"
	Tables map[string]map[string]json.RawMessage `json:"tables"` // table -> key -> JSON value
}

// Snapshot is SyncContent plus where and when it was taken.
type Snapshot struct {
	Version string      `json:"version"` // Waymark version of the node that took it
	Node    string      `json:"node"`
	Created int64       `json:"created"`
	Hash    string      `json:"hash"`
	Content SyncContent `json:"content"`
}

// Store is the part of Waymark's database used by sync.
type Store interface {
	NewTable(table string) error
	TableExists(table string) bool
	ListTable(table string) ([][][]byte, error)
	Write(table string, key string, value interface{}) error
	Delete(table string, key string) error
}

// Hash returns the content's SHA-256 (canonical: JSON with sorted keys).
func (c *SyncContent) Hash() string {
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func isFileEntry(e string) bool { return strings.HasSuffix(e, ".json") }

// allowedSyncPath reports whether rel (slash separated) is inside a synced
// config entry and contains nothing that could escape the config folder.
func allowedSyncPath(rel string) bool {
	if rel == "" || strings.Contains(rel, "\\") || strings.HasPrefix(rel, "/") || strings.ContainsRune(rel, 0) {
		return false
	}
	if path.Clean(rel) != rel {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".hasync-") {
			return false
		}
	}
	for _, e := range SyncConfEntries {
		if isFileEntry(e) {
			if rel == e {
				return true
			}
		} else if strings.HasPrefix(rel, e+"/") {
			return true
		}
	}
	return false
}

func syncTableAllowed(t string) bool {
	for _, s := range SyncTables {
		if s == t {
			return true
		}
	}
	return false
}

// BuildContent reads the synced configuration of this node.
func BuildContent(confDir string, db Store) (*SyncContent, error) {
	c := &SyncContent{Files: map[string]SyncFile{}, Tables: map[string]map[string]json.RawMessage{}}
	total := 0
	for _, e := range SyncConfEntries {
		root := filepath.Join(confDir, filepath.FromSlash(e))
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(confDir, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !allowedSyncPath(rel) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Size() > maxSyncFileSize {
				return fmt.Errorf("%s is larger than %d MB", rel, maxSyncFileSize>>20)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			total += len(data)
			if total > maxSyncTotalSize {
				return fmt.Errorf("configuration is larger than %d MB", maxSyncTotalSize>>20)
			}
			c.Files[rel] = SyncFile{Mode: uint32(info.Mode().Perm() & 0666), Data: data}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, t := range SyncTables {
		rows := map[string]json.RawMessage{}
		if db.TableExists(t) {
			entries, err := db.ListTable(t)
			if err != nil {
				return nil, fmt.Errorf("reading table %s: %w", t, err)
			}
			for _, kv := range entries {
				if len(kv) != 2 || SyncSkipKeys[t][string(kv[0])] {
					continue
				}
				if !json.Valid(kv[1]) {
					continue
				}
				rows[string(kv[0])] = json.RawMessage(append([]byte{}, kv[1]...))
			}
		}
		c.Tables[t] = rows
	}
	return c, nil
}

// Validate checks a snapshot received from a peer before it is staged.
func (s *Snapshot) Validate() error {
	if s.Content.Files == nil || s.Content.Tables == nil {
		return errors.New("snapshot is incomplete")
	}
	if got := s.Content.Hash(); got != s.Hash {
		return errors.New("snapshot does not match its hash")
	}
	total := 0
	for rel, f := range s.Content.Files {
		if !allowedSyncPath(rel) {
			return fmt.Errorf("snapshot contains a file outside the synced configuration: %q", rel)
		}
		if len(f.Data) > maxSyncFileSize {
			return fmt.Errorf("snapshot file %s is too large", rel)
		}
		total += len(f.Data)
	}
	if total > maxSyncTotalSize {
		return errors.New("snapshot is too large")
	}
	for t, rows := range s.Content.Tables {
		if !syncTableAllowed(t) {
			return fmt.Errorf("snapshot contains a table that is not synced: %q", t)
		}
		for k, v := range rows {
			if SyncSkipKeys[t][k] {
				return fmt.Errorf("snapshot contains per-node key %s/%s", t, k)
			}
			if !json.Valid(v) {
				return fmt.Errorf("snapshot value %s/%s is not valid JSON", t, k)
			}
		}
	}
	return nil
}

// ApplyContent replaces this node's synced configuration with c. Run it
// before any module has loaded its configuration. Tables are applied
// first, then each config entry is swapped in as a whole.
func ApplyContent(confDir string, db Store, c *SyncContent) error {
	for _, t := range SyncTables {
		rows := c.Tables[t]
		if !db.TableExists(t) {
			if len(rows) == 0 {
				continue
			}
			if err := db.NewTable(t); err != nil {
				return fmt.Errorf("creating table %s: %w", t, err)
			}
		}
		existing, err := db.ListTable(t)
		if err != nil {
			return fmt.Errorf("reading table %s: %w", t, err)
		}
		for _, kv := range existing {
			k := string(kv[0])
			if SyncSkipKeys[t][k] {
				continue
			}
			if _, keep := rows[k]; !keep {
				if err := db.Delete(t, k); err != nil {
					return fmt.Errorf("removing %s/%s: %w", t, k, err)
				}
			}
		}
		keys := make([]string, 0, len(rows))
		for k := range rows {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if SyncSkipKeys[t][k] {
				continue
			}
			if err := db.Write(t, k, rows[k]); err != nil {
				return fmt.Errorf("writing %s/%s: %w", t, k, err)
			}
		}
	}

	if err := os.MkdirAll(confDir, 0775); err != nil {
		return err
	}
	for _, e := range SyncConfEntries {
		target := filepath.Join(confDir, filepath.FromSlash(e))
		if isFileEntry(e) {
			f, ok := c.Files[e]
			if !ok {
				if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				continue
			}
			if err := writeSyncFile(target, f); err != nil {
				return err
			}
			continue
		}
		// Folder: build the new copy next to it, then swap
		staging := filepath.Join(confDir, ".hasync-new-"+e)
		old := filepath.Join(confDir, ".hasync-old-"+e)
		os.RemoveAll(staging)
		os.RemoveAll(old)
		if err := os.MkdirAll(staging, 0775); err != nil {
			return err
		}
		for rel, f := range c.Files {
			if !strings.HasPrefix(rel, e+"/") {
				continue
			}
			dst := filepath.Join(staging, filepath.FromSlash(strings.TrimPrefix(rel, e+"/")))
			if err := os.MkdirAll(filepath.Dir(dst), 0775); err != nil {
				return err
			}
			if err := writeSyncFile(dst, f); err != nil {
				return err
			}
		}
		if _, err := os.Stat(target); err == nil {
			if err := os.Rename(target, old); err != nil {
				return err
			}
		}
		if err := os.Rename(staging, target); err != nil {
			os.Rename(old, target)
			return err
		}
		os.RemoveAll(old)
	}
	return nil
}

func writeSyncFile(dst string, f SyncFile) error {
	mode := os.FileMode(f.Mode) & 0666
	if mode == 0 {
		mode = 0644
	}
	return utils.WriteFileAtomic(dst, f.Data, mode)
}

/* Authentication */

func syncProof(key []byte, role string, cs *tls.ConnectionState) (string, error) {
	if cs == nil || cs.Version < tls.VersionTLS13 {
		return "", errors.New("TLS 1.3 required")
	}
	ekm, err := cs.ExportKeyingMaterial(syncEKMLabel, nil, 32)
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(role))
	m.Write([]byte{0})
	m.Write(ekm)
	return hex.EncodeToString(m.Sum(nil)), nil
}

func checkProof(key []byte, role string, cs *tls.ConnectionState, got string) bool {
	want, err := syncProof(key, role, cs)
	if err != nil || len(got) != len(want) {
		return false
	}
	return hmac.Equal([]byte(want), []byte(got))
}

// KeyBytes converts a cluster key to HMAC key bytes, or nil if invalid.
func KeyBytes(clusterKey string) []byte {
	if !clusterKeyPattern.MatchString(clusterKey) {
		return nil
	}
	b, _ := hex.DecodeString(clusterKey)
	return b
}

// SelfSignedTLS returns a throwaway certificate for the sync listener.
func SelfSignedTLS() (tls.Certificate, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "waymark-ha-sync"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}, nil
}

// SyncServerTLSConfig is the listener's TLS configuration.
func SyncServerTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}
}

// SyncHandler serves /waymark-ha/v1/state and /waymark-ha/v1/snapshot to
// peers that prove they have the cluster key, and runs the actions in
// Actions (POST /waymark-ha/v1/<action>, small JSON body) for them.
type SyncHandler struct {
	Key      func() []byte                 // current cluster key (nil: refuse everything)
	Allowed  func(remote net.IP) bool      // is this a cluster node?
	State    func() any                    // JSON for /state
	Snapshot func() (*Snapshot, error)     // for /snapshot
	Served   func(remote string, p string) // called after a successful request
	// Actions other nodes may ask this node to carry out (switch-over,
	// update), keyed by name. The handler gets the request body (JSON, at
	// most maxActionBody bytes) and the requesting node's address.
	Actions map[string]func(remote string, body []byte) (any, error)
}

const maxActionBody = 4096

var actionNamePattern = regexp.MustCompile(`^[a-z]{1,32}$`)

func (h *SyncHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	key := h.Key()
	if key == nil || ip == nil || !h.Allowed(ip) {
		http.Error(w, "not a cluster node", http.StatusForbidden)
		return
	}
	if !checkProof(key, "client", r.TLS, r.Header.Get(SyncAuthHeader)) {
		http.Error(w, "cluster key mismatch", http.StatusUnauthorized)
		return
	}
	proof, err := syncProof(key, "server", r.TLS)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var body any
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/waymark-ha/v1/state":
		body = h.State()
	case r.Method == http.MethodGet && r.URL.Path == "/waymark-ha/v1/snapshot":
		snap, err := h.Snapshot()
		if err != nil {
			http.Error(w, "could not read configuration: "+err.Error(), http.StatusInternalServerError)
			return
		}
		body = snap
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/waymark-ha/v1/"):
		name := strings.TrimPrefix(r.URL.Path, "/waymark-ha/v1/")
		action := h.Actions[name]
		if !actionNamePattern.MatchString(name) || action == nil {
			http.NotFound(w, r)
			return
		}
		in, err := io.ReadAll(io.LimitReader(r.Body, maxActionBody+1))
		if err != nil || len(in) > maxActionBody || (len(in) > 0 && !json.Valid(in)) {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		result, err := action(host, in)
		if err != nil {
			// Errors are reported in the body, so the caller can show them
			result = map[string]string{"error": err.Error()}
		}
		body = result
	default:
		http.NotFound(w, r)
		return
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if err := json.NewEncoder(gz).Encode(body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	gz.Close()
	w.Header().Set(SyncProofHeader, proof)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Encoding", "gzip")
	w.Write(buf.Bytes())
	if h.Served != nil {
		h.Served(host, r.URL.Path)
	}
}

// ErrSyncAuth means the peer was reachable but the cluster keys differ.
var ErrSyncAuth = errors.New("the cluster key does not match")

// SyncFetch requests p from the peer at addr (host:port) and decodes the
// JSON response into out. Both sides prove they hold the cluster key.
func SyncFetch(addr string, key []byte, p string, out any, timeout time.Duration) error {
	return syncRequest(addr, key, http.MethodGet, p, nil, out, timeout)
}

// SyncAction asks the peer at addr to carry out action (see
// SyncHandler.Actions) with the JSON body in, and decodes its answer into
// out. An action that failed answers {"error": "..."}.
func SyncAction(addr string, key []byte, action string, in any, out any, timeout time.Duration) error {
	if !actionNamePattern.MatchString(action) {
		return errors.New("invalid action name")
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if len(body) > maxActionBody {
		return errors.New("request too large")
	}
	return syncRequest(addr, key, http.MethodPost, "/waymark-ha/v1/"+action, body, out, timeout)
}

func syncRequest(addr string, key []byte, method string, p string, body []byte, out any, timeout time.Duration) error {
	if key == nil {
		return errors.New("no cluster key")
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		InsecureSkipVerify: true, // identity is proven with the cluster key below
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{"http/1.1"},
	})
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout + 30*time.Second))
	cs := conn.ConnectionState()
	auth, err := syncProof(key, "client", &cs)
	if err != nil {
		return err
	}
	var rb io.Reader
	if body != nil {
		rb = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, "https://"+addr+p, rb)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(SyncAuthHeader, auth)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Close = true
	if err := req.Write(conn); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrSyncAuth
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("peer answered %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if !checkProof(key, "server", &cs, resp.Header.Get(SyncProofHeader)) {
		return ErrSyncAuth
	}
	var rd io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return err
		}
		defer gz.Close()
		rd = gz
	}
	return json.NewDecoder(io.LimitReader(rd, maxSyncTotalSize*2)).Decode(out)
}
