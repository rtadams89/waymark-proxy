package main

/*
	ha_custom.go (custom fork)

	Active/passive pairs with keepalived (see mod/hacluster). Waymark:
	- stores the cluster definition (shared by all nodes; synced in a
	  later phase) and writes this node's settings file for the root
	  helper, then reports the helper's result
	- writes a heartbeat every 2 seconds while its proxy listener answers
	  (TCP connect plus TLS handshake); keepalived's check script requires it
	- reads the role keepalived reports, shows it in the UI, and holds
	  automatic certificate renewal unless this node is the active one
*/

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"imuslab.com/zoraxy/mod/acme"
	"imuslab.com/zoraxy/mod/hacluster"
	"imuslab.com/zoraxy/mod/utils"
)

var (
	haExportInstaller = flag.String("ha-export-installer", "", "Custom fork: write the HA (keepalived) root installer files into this folder and exit")
	haDirFlag         = flag.String("ha-dir", hacluster.DefaultDir, "Custom fork: HA folder shared with keepalived (created by the HA installer)")
	haStateDirFlag    = flag.String("ha-state-dir", hacluster.DefaultStateDir, "Custom fork: folder where the HA helper reports results (created by the HA installer)")
)

// HA settings: conf/ha.json (in sys.db as custom_fork/ha_cluster until 2.16.0)
const (
	haConfigKey  = "ha_cluster"
	haConfigFile = "ha.json"
)

func haConfigPath() string { return filepath.Join(CONF_FOLDER, haConfigFile) }

// readHAConfigFile reads conf/ha.json; nil, nil when there is none.
func readHAConfigFile() (*hacluster.Config, error) {
	b, err := os.ReadFile(haConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := hacluster.DefaultConfig()
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", haConfigPath(), err)
	}
	return cfg, nil
}

func writeHAConfigFile(cfg *hacluster.Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return utils.WriteFileAtomic(haConfigPath(), append(b, '\n'), 0600) // holds the cluster key
}

// moveHAConfigToFile copies the HA settings from sys.db to conf/ha.json
// when copyFromDB (on the first start of 2.16.0, or after restoring an
// older backup) and checks that the file can be read. The copy in sys.db is
// kept for an earlier version put back by a roll back. Run at start-up
// before anything uses haConfig.
func moveHAConfigToFile(copyFromDB bool) error {
	if copyFromDB && sysdb.TableExists(migrationTable) && sysdb.KeyExists(migrationTable, haConfigKey) {
		cfg := hacluster.DefaultConfig()
		if err := sysdb.Read(migrationTable, haConfigKey, cfg); err != nil {
			return err
		}
		return writeHAConfigFile(cfg)
	}
	_, err := readHAConfigFile()
	return err
}

type haHeartbeat struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason"`
	Time   int64  `json:"time"`
}

var ha struct {
	mu        sync.Mutex
	cfg       *hacluster.Config
	rt        *hacluster.Runtime
	heartbeat haHeartbeat
	started   bool
}

func init() {
	acme.RenewalHold = haRenewalHold
}

// handleHAMaintenanceFlags runs -ha-export-installer. Returns true if Waymark
// should exit.
func handleHAMaintenanceFlags() bool {
	if *haExportInstaller == "" {
		return false
	}
	if err := hacluster.ExportInstaller(*haExportInstaller); err != nil {
		fmt.Println("Could not write the HA installer: " + err.Error())
		os.Exit(1)
	}
	fmt.Println("HA installer written to " + *haExportInstaller + ". Review the files, then run as root:")
	fmt.Println("  sudo bash " + strings.TrimRight(*haExportInstaller, "/") + "/install.sh")
	return true
}

func haRuntime() *hacluster.Runtime {
	return &hacluster.Runtime{Dir: *haDirFlag, StateDir: *haStateDirFlag}
}

func haConfig() *hacluster.Config {
	ha.mu.Lock()
	defer ha.mu.Unlock()
	if ha.cfg == nil {
		cfg := hacluster.DefaultConfig()
		if loaded, err := readHAConfigFile(); err == nil && loaded != nil {
			cfg = loaded
		} else if err != nil {
			SystemWideLogger.PrintAndLog("ha", "Cannot read the HA settings", err)
		}
		ha.cfg = cfg
	}
	c := *ha.cfg
	c.Nodes = append([]hacluster.Node{}, ha.cfg.Nodes...)
	return &c
}

func localIPs() []net.IP {
	ips := []net.IP{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			ips = append(ips, n.IP)
		}
	}
	return ips
}

func haSelf(cfg *hacluster.Config) int {
	return cfg.FindSelf(localIPs())
}

func haOwner() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "waymark"
}

// initHA starts the heartbeat and re-applies this node's settings if they
// changed while Waymark was not running. Called once at startup.
func initHA() {
	ha.mu.Lock()
	if ha.started {
		ha.mu.Unlock()
		return
	}
	ha.started = true
	ha.rt = haRuntime()
	ha.mu.Unlock()

	go func() {
		for {
			haWriteHeartbeat()
			time.Sleep(2 * time.Second)
		}
	}()
	initHASync()

	cfg := haConfig()
	if cfg.Enabled {
		if installed, _ := ha.rt.Installed(); installed {
			go func() {
				// Settings unchanged since the last apply? Then keepalived already has them
				current, _ := os.ReadFile(ha.rt.Dir + "/cluster.env")
				want := cfg.Settings(haSelf(cfg), 0, haNodeName(cfg))
				if stripApplyID(string(current)) != stripApplyID(want) {
					if res, err := haApply(cfg); err != nil {
						SystemWideLogger.PrintAndLog("ha", "Applying HA settings at startup failed", err)
					} else if res.Status != "ok" {
						SystemWideLogger.PrintAndLog("ha", "Applying HA settings at startup failed: "+res.Message, nil)
					}
				}
			}()
		}
	}
}

func stripApplyID(s string) string {
	lines := []string{}
	for _, l := range strings.Split(s, "\n") {
		if !strings.HasPrefix(l, "APPLY_ID=") {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

func haNodeName(cfg *hacluster.Config) string {
	if self := haSelf(cfg); self >= 0 {
		return cfg.Nodes[self].Name
	}
	host, _ := os.Hostname()
	name := strings.ToLower(host)
	clean := strings.Builder{}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			clean.WriteRune(r)
		}
	}
	if s := clean.String(); s != "" && len(s) <= 32 && s[0] != '-' {
		return s
	}
	return "node"
}

// haProbeListener connects to the proxy listener and, when it serves TLS,
// completes a handshake (Waymark always has its built-in default
// certificate). A bare connect-and-close would make the TLS server log a
// handshake error every 2 seconds, and a finished handshake also shows the
// listener is actually serving rather than only accepting connections.
func haProbeListener(addr string, useTLS bool, proxyProtocol bool) error {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if proxyProtocol {
		// The listener expects a PROXY protocol header first; v1 UNKNOWN is valid
		if _, err := conn.Write([]byte("PROXY UNKNOWN\r\n")); err != nil {
			return err
		}
	}
	if !useTLS {
		return nil
	}
	tc := tls.Client(conn, &tls.Config{ServerName: "localhost", InsecureSkipVerify: true}) //Only checks that the listener answers
	if err := tc.Handshake(); err != nil {
		return err
	}
	return tc.Close()
}

// haWriteHeartbeat checks that the proxy listener accepts connections and
// records the result for keepalived's check script.
func haWriteHeartbeat() {
	ok, reason := true, ""
	if dynamicProxyRouter == nil || !dynamicProxyRouter.Running {
		ok, reason = false, "proxy service is not running"
	} else {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(dynamicProxyRouter.Option.Port))
		if err := haProbeListener(addr, dynamicProxyRouter.Option.UseTls, dynamicProxyRouter.Option.UseProxyProtocol); err != nil {
			ok, reason = false, "proxy listener "+addr+" is not answering"
		}
	}
	if holding, why := haSwitchHolding(); holding && ok {
		// Manual switch-over: report unhealthy so keepalived hands over (haswitch_custom.go)
		ok, reason = false, why
	}
	now := time.Now()
	ha.mu.Lock()
	ha.heartbeat = haHeartbeat{OK: ok, Reason: reason, Time: now.Unix()}
	rt := ha.rt
	enabled := ha.cfg != nil && ha.cfg.Enabled
	ha.mu.Unlock()
	if rt == nil || !enabled {
		return
	}
	if installed, _ := rt.Installed(); installed {
		rt.WriteHeartbeat(ok, reason, now)
	}
}

// haApply writes this node's settings and waits for the helper's result.
func haApply(cfg *hacluster.Config) (hacluster.ApplyResult, error) {
	rt := haRuntime()
	if installed, why := rt.Installed(); !installed {
		return hacluster.ApplyResult{}, fmt.Errorf("the HA integration is not installed on this node (%s)", why)
	}
	applyID := time.Now().UnixNano()
	if err := rt.WriteSettings(cfg.Settings(haSelf(cfg), applyID, haNodeName(cfg))); err != nil {
		return hacluster.ApplyResult{}, fmt.Errorf("could not write HA settings: %w", err)
	}
	// The helper bounds each keepalived start to 20 seconds and may apply a
	// newer save straight after, so allow for a slow start plus a restore
	res, err := rt.WaitForResult(applyID, 75*time.Second)
	if err != nil {
		return res, err
	}
	if res.Status == "ok" {
		SystemWideLogger.PrintAndLog("ha", "HA settings applied: "+res.Message, nil)
	} else {
		SystemWideLogger.PrintAndLog("ha", "HA settings rejected by waymark-ha-apply: "+res.Message, nil)
	}
	return res, nil
}

// haRenewalHold is acme.RenewalHold: automatic renewal runs only on the
// node keepalived made active (or when HA is off or its state is unknown).
func haRenewalHold() (bool, string) {
	cfg := haConfig()
	if !cfg.Enabled {
		return false, ""
	}
	switch haEffectiveRole(haRuntime()).State {
	case "BACKUP":
		return true, "this node is the HA standby (the active node renews)"
	case "FAULT":
		return true, "this node's HA state is FAULT"
	case "STOP":
		return true, "keepalived is stopped on this node"
	}
	return false, ""
}

// haServiceState asks systemd whether a unit is running (active, inactive,
// failed, ...). Read-only, so it works as the unprivileged waymark user.
// Returns "unknown" when systemd can't be asked.
func haServiceState(unit string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "systemctl", "is-active", unit).Output()
	if state := strings.TrimSpace(string(out)); state != "" && !strings.ContainsAny(state, " \n") {
		return state
	}
	return "unknown"
}

// haEffectiveRole is the role keepalived last reported, except that a
// keepalived systemd says is not running counts as STOP (the role file is
// stale if keepalived was killed without running its stop notification).
func haEffectiveRole(rt *hacluster.Runtime) hacluster.Role {
	role := rt.ReadRole()
	if role.State != "STOP" {
		if st := haServiceState("keepalived"); st == "inactive" || st == "failed" {
			role.State = "STOP"
		}
	}
	return role
}

/* API */

// HandleHAStatus returns the cluster settings and this node's live state.
// GET /api/ha/status
func HandleHAStatus(w http.ResponseWriter, r *http.Request) {
	cfg := haConfig()
	rt := haRuntime()
	installed, why := rt.Installed()
	self := haSelf(cfg)
	vipHeld := false
	if ip := net.ParseIP(cfg.VIP); ip != nil {
		for _, l := range localIPs() {
			if l.Equal(ip) {
				vipHeld = true
			}
		}
	}
	local := []string{}
	for _, ip := range localIPs() {
		if ip.To4() != nil && !ip.IsLoopback() {
			local = append(local, ip.String())
		}
	}
	lastApply, _ := rt.ReadResult()
	hold, holdReason := haRenewalHold()
	ha.mu.Lock()
	hb := ha.heartbeat
	ha.mu.Unlock()
	resp := map[string]any{
		"config":          cfg,
		"installed":       installed,
		"installedReason": why,
		"self":            self,
		"nodeName":        haNodeName(cfg),
		"localAddresses":  local,
		"role":            haEffectiveRole(rt),
		"services": map[string]string{
			"applyWatcher": haServiceState("waymark-ha-apply.path"),
			"keepalived":   haServiceState("keepalived"),
		},
		"vipHeld":        vipHeld,
		"floatingIPs":    haFloatingIPsHeld(cfg),
		"helper":         haHelperState(),
		"nonlocalBind":   haNonlocalBind(cfg),
		"heartbeat":      hb,
		"lastApply":      lastApply,
		"preview":        cfg.Preview(self, haOwner(), hacluster.DefaultScriptsDir),
		"renewalHeld":    hold,
		"renewalReason":  holdReason,
		"haDir":          rt.Dir,
		"installerFiles": hacluster.DeployFileNames,
		"executable":     haExecutable(),
		"sync":           haSyncStatus(),
		"cluster":        haClusterView(),
		"readOnly":       haIsReadOnly(),
		"switchover":     haSwitchStatus(),
		"rolling":        rollView(),
	}
	js, _ := json.Marshal(resp)
	utils.SendJSONResponse(w, string(js))
}

// HandleHAConfig saves the cluster settings and applies them on this node.
// POST /api/ha/config  config=<json>
func HandleHAConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := utils.PostPara(r, "config")
	if err != nil {
		utils.SendErrorResponse(w, "config not defined")
		return
	}
	submitted := hacluster.DefaultConfig()
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(submitted); err != nil {
		utils.SendErrorResponse(w, "invalid config: "+err.Error())
		return
	}
	cfg, err := submitted.Validate()
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	if cfg.Enabled && haSelf(cfg) < 0 {
		utils.SendErrorResponse(w, "none of the nodes has an address of this machine ("+strings.Join(nonLoopback(), ", ")+"); add this node to the list")
		return
	}
	if err := writeHAConfigFile(cfg); err != nil {
		utils.SendErrorResponse(w, "could not save HA settings")
		return
	}
	ha.mu.Lock()
	ha.cfg = cfg
	ha.mu.Unlock()
	SystemWideLogger.PrintAndLog("ha", "HA settings saved", nil)
	haSyncReconcileListener()

	resp := map[string]any{"saved": true}
	if installed, why := haRuntime().Installed(); !installed {
		resp["applied"] = false
		resp["message"] = "Saved. Not applied: the HA integration is not installed on this node (" + why + ")."
	} else if res, err := haApply(cfg); err != nil {
		resp["applied"] = false
		resp["message"] = "Saved, but applying failed: " + err.Error()
	} else {
		resp["applied"] = res.Status == "ok"
		resp["result"] = res
		resp["message"] = res.Message
	}
	js, _ := json.Marshal(resp)
	utils.SendJSONResponse(w, string(js))
}

// HandleHAApply re-applies the saved settings on this node.
// POST /api/ha/apply
func HandleHAApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	res, err := haApply(haConfig())
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	js, _ := json.Marshal(res)
	utils.SendJSONResponse(w, string(js))
}

// HandleHAInstallerFile returns one installer file for review in the UI.
// GET /api/ha/installer?file=<name>
func HandleHAInstallerFile(w http.ResponseWriter, r *http.Request) {
	name, _ := utils.GetPara(r, "file")
	valid := false
	for _, n := range hacluster.DeployFileNames {
		if n == name {
			valid = true
		}
	}
	if !valid {
		http.Error(w, "unknown file", http.StatusNotFound)
		return
	}
	content, err := hacluster.DeployFile(name)
	if err != nil {
		http.Error(w, "unknown file", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(content))
}

func nonLoopback() []string {
	out := []string{}
	for _, ip := range localIPs() {
		if ip.To4() != nil && !ip.IsLoopback() {
			out = append(out, ip.String())
		}
	}
	return out
}

func haExecutable() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "./waymark"
}
