package main

/*
	hasync_custom.go (custom fork)

	HA phase 2: configuration sync from the active node to standby nodes
	(see mod/hacluster/sync.go for the snapshot format and authentication).

	- every node with sync on runs a TLS listener on its own address
	  (cluster settings: sync port) serving its state and a snapshot of its
	  configuration to the other cluster nodes
	- a standby polls the other nodes every 10 seconds; when the active node's
	  configuration changed and has stayed the same for one poll, it
	  downloads and checks a snapshot, stages it, and restarts Waymark in
	  place. The staged snapshot is applied at startup, before any module
	  loads its configuration (haApplyPendingSync, called from start.go)
	- while sync is on, a standby refuses configuration changes in the admin
	  UI (haReadOnly); its HA settings stay editable so sync can be paused
*/

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"imuslab.com/zoraxy/mod/hacluster"
	"imuslab.com/zoraxy/mod/utils"
)

const (
	haSyncPendingFile = "sync-pending.json"
	haSyncAppliedFile = "sync-applied.json"
	haSyncInterval    = 10 * time.Second
	haSyncMinGap      = 30 * time.Second // between two applies
)

type haSyncApplied struct {
	Hash       string `json:"hash"`       // last configuration applied from the active node
	From       string `json:"from"`       // node it came from
	Time       int64  `json:"time"`       // when it was applied
	FailedHash string `json:"failedHash"` // configuration whose apply failed (not retried automatically)
	Error      string `json:"error"`
}

// haSyncPeerInfo is what a node reports about itself to the other nodes
// (/waymark-ha/v1/state): used for syncing and for the cluster overview.
type haSyncPeerInfo struct {
	Node            string `json:"node"`
	Role            string `json:"role"`
	Version         string `json:"version"`
	Hash            string `json:"hash"`
	HeartbeatOK     bool   `json:"heartbeatOk"`
	HeartbeatReason string `json:"heartbeatReason"`
	VIPHeld         bool   `json:"vipHeld"`    // holds the primary floating IP
	VIPsHeld        int    `json:"vipsHeld"`   // how many floating IPs it holds
	Keepalived      string `json:"keepalived"` // systemd state
	SyncStatus      string `json:"syncStatus"`
	SyncDetail      string `json:"syncDetail"`
	Started         int64  `json:"started"` // when Waymark started
}

// haPeerStatus is the last poll of another node.
type haPeerStatus struct {
	Info     haSyncPeerInfo
	LastSeen int64  // last successful poll
	LastPoll int64  // last attempt
	Err      string // error of the last attempt, if it failed
}

var haSync struct {
	mu         sync.Mutex
	server     *http.Server
	listenAddr string
	listenErr  string

	status     string // off, active, in-sync, waiting, restarting, no-active, error
	detail     string
	activeNode string
	activeAddr string
	lastCheck  int64
	seenHash   string                   // settle check: last hash seen on the active node
	servedSnap map[string]int64         // peer address -> last snapshot served
	servedPoll map[string]int64         // peer address -> last state request
	peers      map[string]*haPeerStatus // peer address -> last poll
	ticking    sync.Mutex               // one tick at a time
	restarting bool
}

func haSyncPath(name string) string { return filepath.Join(*haDirFlag, name) }

func readSyncApplied() haSyncApplied {
	var a haSyncApplied
	if b, err := os.ReadFile(haSyncPath(haSyncAppliedFile)); err == nil {
		json.Unmarshal(b, &a)
	}
	return a
}

func writeSyncFileAtomic(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(*haDirFlag, "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	tmp.Chmod(0600) // contains keys and password hashes
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), haSyncPath(name))
}

func haSyncSetStatus(status, detail string) {
	haSync.mu.Lock()
	haSync.status, haSync.detail = status, detail
	haSync.lastCheck = time.Now().Unix()
	haSync.mu.Unlock()
}

/* Startup: apply a staged snapshot before modules load their configuration */

// haApplyPendingSync is called from startupSequence right after the
// database is opened.
func haApplyPendingSync() {
	p := haSyncPath(haSyncPendingFile)
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	os.Remove(p)
	var pending struct {
		From     string             `json:"from"`
		Snapshot hacluster.Snapshot `json:"snapshot"`
	}
	applied := readSyncApplied()
	fail := func(hash string, err error) {
		SystemWideLogger.PrintAndLog("ha-sync", "Applying the configuration from the active node failed", err)
		applied.FailedHash, applied.Error = hash, err.Error()
		writeSyncFileAtomic(haSyncAppliedFile, applied)
	}
	if err := json.Unmarshal(b, &pending); err != nil {
		fail("", fmt.Errorf("staged configuration is unreadable: %w", err))
		return
	}
	snap := &pending.Snapshot
	if err := snap.Validate(); err != nil {
		fail(snap.Hash, err)
		return
	}
	if snap.Version != DISPLAY_VERSION {
		fail(snap.Hash, fmt.Errorf("staged configuration is from Waymark %s, this is %s", snap.Version, DISPLAY_VERSION))
		return
	}
	if err := hacluster.ApplyContent(CONF_FOLDER, sysdb, &snap.Content); err != nil {
		fail(snap.Hash, err)
		return
	}
	writeSyncFileAtomic(haSyncAppliedFile, haSyncApplied{Hash: snap.Hash, From: pending.From, Time: time.Now().Unix()})
	SystemWideLogger.PrintAndLog("ha-sync", "Applied the configuration from active node "+pending.From+" ("+shortHash(snap.Hash)+")", nil)
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// haRestartForSync restarts Waymark in place (same process ID, so systemd
// keeps tracking it) to load a staged configuration.
func haRestartForSync() {
	SystemWideLogger.PrintAndLog("ha-sync", "Restarting to load the configuration from the active node", nil)
	exe, err := os.Executable()
	if err != nil {
		SystemWideLogger.PrintAndLog("ha-sync", "Cannot restart: executable path unknown", err)
		return
	}
	mdnsTickerStop = nil // the mDNS ticker may be mid-scan; exec ends it anyway
	ShutdownSeq()
	err = syscall.Exec(exe, os.Args, os.Environ())
	// Only reached if exec failed; exit so systemd can restart Waymark
	fmt.Println("HA sync: restart failed: " + err.Error())
	os.Exit(1)
}

/* Listener */

func haSyncKey() []byte {
	cfg := haConfig()
	if !cfg.Enabled || !cfg.SyncEnabled {
		return nil
	}
	return hacluster.KeyBytes(cfg.ClusterKey)
}

func haSyncAllowed(ip net.IP) bool {
	cfg := haConfig()
	self := haSelf(cfg)
	for i, n := range cfg.Nodes {
		if i != self && net.ParseIP(n.Address).Equal(ip) {
			return true
		}
	}
	return false
}

func haSyncLocalState() any {
	info := haLocalInfo()
	if c, err := hacluster.BuildContent(CONF_FOLDER, sysdb); err == nil {
		info.Hash = c.Hash()
	}
	return info
}

// haLocalInfo describes this node (without the configuration hash).
func haLocalInfo() haSyncPeerInfo {
	cfg := haConfig()
	ha.mu.Lock()
	hb := ha.heartbeat
	ha.mu.Unlock()
	haSync.mu.Lock()
	st, detail := haSync.status, haSync.detail
	haSync.mu.Unlock()
	return haSyncPeerInfo{
		Node: haNodeName(cfg), Role: haEffectiveRole(haRuntime()).State, Version: DISPLAY_VERSION,
		HeartbeatOK: hb.OK, HeartbeatReason: hb.Reason, VIPHeld: haVIPHeld(cfg), VIPsHeld: haCountFloatingIPsHeld(cfg),
		Keepalived: haServiceState("keepalived"), SyncStatus: st, SyncDetail: detail, Started: bootTime,
	}
}

func haVIPHeld(cfg *hacluster.Config) bool {
	ip := net.ParseIP(cfg.VIP)
	if ip == nil {
		return false
	}
	for _, l := range localIPs() {
		if l.Equal(ip) {
			return true
		}
	}
	return false
}

func haSyncLocalSnapshot() (*hacluster.Snapshot, error) {
	c, err := hacluster.BuildContent(CONF_FOLDER, sysdb)
	if err != nil {
		return nil, err
	}
	return &hacluster.Snapshot{Version: DISPLAY_VERSION, Node: haNodeName(haConfig()), Created: time.Now().Unix(), Hash: c.Hash(), Content: *c}, nil
}

// haSyncReconcileListener starts, moves or stops the sync listener to
// match the settings.
func haSyncReconcileListener() {
	cfg := haConfig()
	want := ""
	if self := haSelf(cfg); cfg.Enabled && cfg.SyncEnabled && hacluster.KeyBytes(cfg.ClusterKey) != nil && self >= 0 {
		want = net.JoinHostPort(cfg.Nodes[self].Address, strconv.Itoa(cfg.SyncPort))
	}
	haSync.mu.Lock()
	defer haSync.mu.Unlock()
	if want == haSync.listenAddr && (want == "" || haSync.server != nil) {
		return
	}
	if haSync.server != nil {
		haSync.server.Close()
		haSync.server = nil
	}
	haSync.listenAddr, haSync.listenErr = want, ""
	if want == "" {
		return
	}
	cert, err := hacluster.SelfSignedTLS()
	if err != nil {
		haSync.listenErr = err.Error()
		return
	}
	ln, err := net.Listen("tcp", want)
	if err != nil {
		haSync.listenErr = "cannot listen on " + want + ": " + err.Error()
		SystemWideLogger.PrintAndLog("ha-sync", haSync.listenErr, nil)
		return
	}
	handler := &hacluster.SyncHandler{
		Key:      haSyncKey,
		Allowed:  haSyncAllowed,
		State:    haSyncLocalState,
		Snapshot: haSyncLocalSnapshot,
		Served: func(remote, p string) {
			haSync.mu.Lock()
			if strings.HasSuffix(p, "/snapshot") {
				haSync.servedSnap[remote] = time.Now().Unix()
			} else {
				haSync.servedPoll[remote] = time.Now().Unix()
			}
			haSync.mu.Unlock()
		},
	}
	srv := &http.Server{Handler: handler, TLSConfig: hacluster.SyncServerTLSConfig(cert), ReadHeaderTimeout: 10 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	haSync.server = srv
	go srv.ServeTLS(ln, "", "")
	SystemWideLogger.PrintAndLog("ha-sync", "Configuration sync listening on "+want, nil)
}

/* Standby: poll the active node */

func haSyncLoop() {
	for {
		haSyncTick(false)
		time.Sleep(haSyncInterval)
	}
}

// haSyncTick checks the other nodes and pulls the active node's
// configuration when it changed. force skips the settle and rate checks
// (Sync now). Returns a message for the UI.
func haSyncTick(force bool) (string, bool) {
	haSync.ticking.Lock()
	defer haSync.ticking.Unlock()
	haSyncReconcileListener()

	cfg := haConfig()
	if !cfg.Enabled || !cfg.SyncEnabled {
		haSyncSetStatus("off", "Configuration sync is off")
		return "Configuration sync is off", false
	}
	key := hacluster.KeyBytes(cfg.ClusterKey)
	self := haSelf(cfg)
	if key == nil || self < 0 {
		haSyncSetStatus("error", "This node is not in the cluster's node list")
		return "This node is not in the cluster's node list", false
	}
	haSync.mu.Lock()
	restarting := haSync.restarting
	haSync.mu.Unlock()
	if restarting {
		return "Restarting to load the new configuration", true
	}
	// Ask every other node for its role and configuration hash
	type peerResult struct {
		node hacluster.Node
		info haSyncPeerInfo
		err  error
	}
	results := make([]peerResult, 0, len(cfg.Nodes)-1)
	var wg sync.WaitGroup
	var rmu sync.Mutex
	for i, n := range cfg.Nodes {
		if i == self {
			continue
		}
		wg.Add(1)
		go func(n hacluster.Node) {
			defer wg.Done()
			var info haSyncPeerInfo
			err := hacluster.SyncFetch(net.JoinHostPort(n.Address, strconv.Itoa(cfg.SyncPort)), key, "/waymark-ha/v1/state", &info, 3*time.Second)
			rmu.Lock()
			results = append(results, peerResult{n, info, err})
			rmu.Unlock()
		}(n)
	}
	wg.Wait()
	haSync.mu.Lock()
	now := time.Now().Unix()
	for _, r := range results {
		ps := haSync.peers[r.node.Address]
		if ps == nil {
			ps = &haPeerStatus{}
			haSync.peers[r.node.Address] = ps
		}
		ps.LastPoll = now
		if r.err != nil {
			ps.Err = r.err.Error()
			if errors.Is(r.err, hacluster.ErrSyncAuth) {
				ps.Err = "the cluster key does not match this node's"
			}
		} else {
			ps.Err, ps.Info, ps.LastSeen = "", r.info, now
		}
	}
	haSync.mu.Unlock()
	sort.Slice(results, func(i, j int) bool { return results[i].node.Name < results[j].node.Name })

	if haEffectiveRole(haRuntime()).State == "MASTER" {
		haSync.mu.Lock()
		haSync.activeNode, haSync.activeAddr = cfg.Nodes[self].Name, cfg.Nodes[self].Address
		haSync.seenHash = ""
		haSync.mu.Unlock()
		for _, r := range results {
			if r.err == nil && r.info.Role == "MASTER" {
				msg := r.node.Name + " also reports itself active (both nodes hold the floating IP?). Check that VRRP traffic passes between the nodes"
				haSyncSetStatus("error", msg)
				return msg, false
			}
		}
		haSyncSetStatus("active", "This node is active: standby nodes copy their configuration from it")
		return "This node is active: there is nothing to pull", false
	}

	var active []peerResult
	problems := []string{}
	for _, r := range results {
		switch {
		case r.err != nil && errors.Is(r.err, hacluster.ErrSyncAuth):
			problems = append(problems, r.node.Name+": the cluster key does not match this node's")
		case r.err != nil:
			problems = append(problems, r.node.Name+": "+r.err.Error())
		case r.info.Role == "MASTER":
			active = append(active, r)
		}
	}
	if len(active) == 0 {
		msg := "No active node found to copy the configuration from"
		if len(problems) > 0 {
			msg += " (" + strings.Join(problems, "; ") + ")"
		}
		haSyncSetStatus("no-active", msg)
		return msg, false
	}
	if len(active) > 1 {
		msg := "More than one node reports itself active; not syncing until that is resolved"
		haSyncSetStatus("error", msg)
		return msg, false
	}
	m := active[0]
	haSync.mu.Lock()
	haSync.activeNode, haSync.activeAddr = m.node.Name, m.node.Address
	haSync.mu.Unlock()
	if m.info.Version != DISPLAY_VERSION {
		msg := fmt.Sprintf("Not syncing: %s runs Waymark %s and this node runs %s. Run the same version on every node", m.node.Name, m.info.Version, DISPLAY_VERSION)
		haSyncSetStatus("error", msg)
		return msg, false
	}
	applied := readSyncApplied()
	if m.info.Hash == applied.Hash {
		haSync.mu.Lock()
		haSync.seenHash = ""
		haSync.mu.Unlock()
		msg := "In sync with " + m.node.Name
		haSyncSetStatus("in-sync", msg)
		return msg, false
	}
	if !force {
		if m.info.Hash == applied.FailedHash {
			msg := "Applying " + m.node.Name + "'s configuration failed: " + applied.Error + ". Fix it on the active node, or use Sync now to retry"
			haSyncSetStatus("error", msg)
			return msg, false
		}
		haSync.mu.Lock()
		settled := haSync.seenHash == m.info.Hash
		haSync.seenHash = m.info.Hash
		haSync.mu.Unlock()
		if !settled {
			msg := "Configuration changed on " + m.node.Name + "; copying it once it stops changing"
			haSyncSetStatus("waiting", msg)
			return msg, false
		}
		if time.Since(time.Unix(applied.Time, 0)) < haSyncMinGap {
			msg := "Configuration changed on " + m.node.Name + "; copying it shortly"
			haSyncSetStatus("waiting", msg)
			return msg, false
		}
	}

	var snap hacluster.Snapshot
	if err := hacluster.SyncFetch(net.JoinHostPort(m.node.Address, strconv.Itoa(cfg.SyncPort)), key, "/waymark-ha/v1/snapshot", &snap, 20*time.Second); err != nil {
		msg := "Could not download the configuration from " + m.node.Name + ": " + err.Error()
		haSyncSetStatus("error", msg)
		return msg, false
	}
	if err := haSyncCheckSnapshot(&snap); err != nil {
		msg := "Refused the configuration from " + m.node.Name + ": " + err.Error()
		haSyncSetStatus("error", msg)
		SystemWideLogger.PrintAndLog("ha-sync", msg, nil)
		return msg, false
	}
	if err := writeSyncFileAtomic(haSyncPendingFile, map[string]any{"from": m.node.Name, "snapshot": snap}); err != nil {
		msg := "Could not stage the configuration: " + err.Error()
		haSyncSetStatus("error", msg)
		return msg, false
	}
	haSync.mu.Lock()
	haSync.restarting = true
	haSync.mu.Unlock()
	msg := "Copied the configuration from " + m.node.Name + "; restarting Waymark to load it"
	haSyncSetStatus("restarting", msg)
	go func() {
		time.Sleep(time.Second) // let the Sync now response go out
		haRestartForSync()
	}()
	return msg, true
}

// haSyncCheckSnapshot validates a downloaded snapshot and makes sure it
// would keep this node in a syncing cluster.
func haSyncCheckSnapshot(snap *hacluster.Snapshot) error {
	if err := snap.Validate(); err != nil {
		return err
	}
	if snap.Version != DISPLAY_VERSION {
		return fmt.Errorf("it is from Waymark %s, this node runs %s", snap.Version, DISPLAY_VERSION)
	}
	raw, ok := snap.Content.Tables[migrationTable][haConfigKey]
	if !ok {
		return errors.New("it has no HA settings")
	}
	incoming := hacluster.DefaultConfig()
	if err := json.Unmarshal(raw, incoming); err != nil {
		return errors.New("its HA settings are unreadable")
	}
	if !incoming.Enabled || !incoming.SyncEnabled {
		return errors.New("HA or configuration sync is off in it")
	}
	if incoming.FindSelf(localIPs()) < 0 {
		return errors.New("its cluster settings do not include this node")
	}
	return nil
}

/* Read-only standby */

// haIsReadOnly reports whether configuration changes are refused on this
// node: HA and sync are on and this node is not the active one.
func haIsReadOnly() bool {
	cfg := haConfig()
	if !cfg.Enabled || !cfg.SyncEnabled || haSelf(cfg) < 0 {
		return false
	}
	return haRuntime().ReadRole().State != "MASTER"
}

func haReadOnlyMessage() string {
	cfg := haConfig()
	msg := "This node (" + haNodeName(cfg) + ") is the HA standby: its settings are copied from the active node and are read-only here. Make this change on the active node"
	haSync.mu.Lock()
	name, addr := haSync.activeNode, haSync.activeAddr
	haSync.mu.Unlock()
	if name != "" && name != haNodeName(cfg) {
		msg += " (" + name + ", " + addr + ")"
	}
	return msg + "."
}

// haReadOnly wraps the admin UI: on a read-only standby, requests that
// change settings are refused. Signing in and out and the HA settings
// (to pause sync on this node) still work.
func haReadOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path
		if !strings.HasPrefix(p, "/api/") || p == "/api/auth/login" || p == "/api/auth/logout" || strings.HasPrefix(p, "/api/ha/") {
			next.ServeHTTP(w, r)
			return
		}
		if haIsReadOnly() {
			js, _ := json.Marshal(map[string]string{"error": haReadOnlyMessage()})
			w.Header().Set("Content-Type", "application/json")
			w.Write(js)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// haWithReadOnly adds haReadOnly inside the admin UI's middleware.
func haWithReadOnly(mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler { return mw(haReadOnly(h)) }
}

/* Cluster overview */

// haClusterView lists every node with its state, for the HA page and the
// status page panel. Other nodes are known only while configuration sync is
// on (their state comes over the sync connection). A node's role, health,
// floating IP and sync fields are reported only when its latest poll
// answered; for an unreachable node only "lastRole" and "lastSeen" are
// given, so a stale "active" is never mistaken for the current state.
func haClusterView() []map[string]any {
	cfg := haConfig()
	self := haSelf(cfg)
	syncOn := cfg.SyncEnabled && hacluster.KeyBytes(cfg.ClusterKey) != nil && self >= 0
	local := haLocalInfo()
	out := []map[string]any{}
	haSync.mu.Lock()
	defer haSync.mu.Unlock()
	for i, n := range cfg.Nodes {
		row := map[string]any{"name": n.Name, "address": n.Address, "priority": n.Priority, "self": i == self}
		var info *haSyncPeerInfo
		switch {
		case i == self:
			info = &local
			row["reachable"], row["lastSeen"] = true, time.Now().Unix()
		case !syncOn:
			row["reachable"], row["error"] = false, "Status of other nodes is shown when configuration sync is on"
		default:
			info = haPeerRow(row, haSync.peers[n.Address])
		}
		if info != nil {
			row["role"] = info.Role
			row["heartbeatOk"] = info.HeartbeatOK
			row["heartbeatReason"] = info.HeartbeatReason
			row["vipHeld"] = info.VIPHeld
			row["vipsHeld"] = info.VIPsHeld
			row["keepalived"] = info.Keepalived
			row["version"] = info.Version
			row["versionMatch"] = info.Version == DISPLAY_VERSION
			row["syncStatus"] = info.SyncStatus
			row["syncDetail"] = info.SyncDetail
			row["started"] = info.Started
		}
		out = append(out, row)
	}
	return out
}

// haPeerRow fills the reachability fields of another node's row and returns
// its state when that state is current (the latest poll answered), else nil.
func haPeerRow(row map[string]any, ps *haPeerStatus) *haSyncPeerInfo {
	if ps == nil {
		row["reachable"], row["error"] = false, "Not checked yet"
		return nil
	}
	row["lastSeen"] = ps.LastSeen
	if ps.Err != "" {
		row["reachable"], row["error"] = false, ps.Err
		if ps.LastSeen > 0 {
			row["lastRole"] = ps.Info.Role
		}
		return nil
	}
	row["reachable"] = true
	info := ps.Info
	return &info
}

// HandleHACluster returns the cluster overview (status page panel).
// GET /api/ha/cluster
func HandleHACluster(w http.ResponseWriter, r *http.Request) {
	cfg := haConfig()
	js, _ := json.Marshal(map[string]any{
		"enabled":     cfg.Enabled,
		"syncEnabled": cfg.SyncEnabled,
		"vip":         cfg.VIP,
		"floatingIPs": cfg.FloatingIPs(),
		"nodes":       haClusterView(),
	})
	utils.SendJSONResponse(w, string(js))
}

/* API */

func haSyncStatus() map[string]any {
	cfg := haConfig()
	applied := readSyncApplied()
	haSync.mu.Lock()
	defer haSync.mu.Unlock()
	served := []map[string]any{}
	for _, n := range cfg.Nodes {
		if t, ok := haSync.servedPoll[n.Address]; ok {
			served = append(served, map[string]any{"node": n.Name, "address": n.Address, "lastCheck": t, "lastCopy": haSync.servedSnap[n.Address]})
		}
	}
	return map[string]any{
		"status":     haSync.status,
		"detail":     haSync.detail,
		"lastCheck":  haSync.lastCheck,
		"activeNode": haSync.activeNode,
		"listen":     haSync.listenAddr,
		"listenErr":  haSync.listenErr,
		"applied":    applied,
		"servedTo":   served,
	}
}

// HandleHASyncNow checks the active node now and pulls if it differs.
// POST /api/ha/sync/now
func HandleHASyncNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	msg, restarting := haSyncTick(true)
	js, _ := json.Marshal(map[string]any{"message": msg, "restarting": restarting})
	utils.SendJSONResponse(w, string(js))
}

func initHASync() {
	haSync.mu.Lock()
	haSync.servedSnap = map[string]int64{}
	haSync.servedPoll = map[string]int64{}
	haSync.peers = map[string]*haPeerStatus{}
	haSync.status, haSync.detail = "starting", "Checking the other nodes"
	haSync.mu.Unlock()
	go haSyncLoop()
}
