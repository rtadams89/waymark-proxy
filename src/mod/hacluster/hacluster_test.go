package hacluster

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sample() *Config {
	return &Config{
		Enabled: true, VIP: "10.10.20.5", Prefix: 24, RouterID: 51, Password: "s3cret", Nodes: []Node{
			{Name: "zoraxy1", Address: "10.10.20.10", Interface: "ens18", Priority: 150},
			{Name: "zoraxy2", Address: "10.10.20.11", Interface: "ens18", Priority: 100},
		},
	}
}

func TestValidate(t *testing.T) {
	v, err := sample().Validate()
	if err != nil || v.VIP != "10.10.20.5" {
		t.Fatalf("valid config rejected: %v", err)
	}
	if v, err := (&Config{}).Validate(); err != nil || v.Enabled {
		t.Fatalf("empty disabled config must be accepted: %v", err)
	}
	bad := map[string]func(*Config){
		"vip":          func(c *Config) { c.VIP = "10.10.20" },
		"ipv6 vip":     func(c *Config) { c.VIP = "fd00::5" },
		"prefix":       func(c *Config) { c.Prefix = 33 },
		"router id":    func(c *Config) { c.RouterID = 0 },
		"password":     func(c *Config) { c.Password = "toolongpw" },
		"password chr": func(c *Config) { c.Password = "a b" },
		"one node":     func(c *Config) { c.Nodes = c.Nodes[:1] },
		"name":         func(c *Config) { c.Nodes[0].Name = "Bad_Name" },
		"node addr":    func(c *Config) { c.Nodes[0].Address = "10.10.21.10" },
		"node = vip":   func(c *Config) { c.Nodes[0].Address = "10.10.20.5" },
		"iface":        func(c *Config) { c.Nodes[0].Interface = "ens18;rm" },
		"priority":     func(c *Config) { c.Nodes[0].Priority = 255 },
		"dup prio":     func(c *Config) { c.Nodes[1].Priority = 150 },
		"dup addr":     func(c *Config) { c.Nodes[1].Address = "10.10.20.10" },
		"vip label":    func(c *Config) { c.VIPLabel = "bad;label" },
		"extra addr":   func(c *Config) { c.ExtraVIPs = []FloatingIP{{Address: "10.10.20", Prefix: 24}} },
		"extra ipv6":   func(c *Config) { c.ExtraVIPs = []FloatingIP{{Address: "fd00::6", Prefix: 64}} },
		"extra prefix": func(c *Config) { c.ExtraVIPs = []FloatingIP{{Address: "10.10.20.6", Prefix: 0}} },
		"extra = vip":  func(c *Config) { c.ExtraVIPs = []FloatingIP{{Address: "10.10.20.5", Prefix: 24}} },
		"extra twice": func(c *Config) {
			c.ExtraVIPs = []FloatingIP{{Address: "10.10.20.6", Prefix: 24}, {Address: "10.10.20.6", Prefix: 24}}
		},
		"extra = node": func(c *Config) { c.ExtraVIPs = []FloatingIP{{Address: "10.10.20.11", Prefix: 24}} },
		"extra label":  func(c *Config) { c.ExtraVIPs = []FloatingIP{{Address: "10.10.20.6", Prefix: 24, Label: "<b>"}} },
		"too many":     func(c *Config) { c.ExtraVIPs = make([]FloatingIP, MaxFloatingIPs) },
	}
	for name, mod := range bad {
		c := sample()
		mod(c)
		if _, err := c.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestExtraFloatingIPs(t *testing.T) {
	c := sample()
	c.VIPLabel = "Main site"
	c.ExtraVIPs = []FloatingIP{{Address: " 10.10.20.6 ", Prefix: 24, Label: "Internal"}, {Address: "192.168.50.5", Prefix: 32}}
	v, err := c.Validate()
	if err != nil {
		t.Fatalf("valid extra floating IPs rejected: %v", err)
	}
	if v.ExtraVIPs[0].Address != "10.10.20.6" {
		t.Errorf("address not normalised: %q", v.ExtraVIPs[0].Address)
	}
	ips := v.FloatingIPs()
	if len(ips) != 3 || ips[0].Address != "10.10.20.5" || ips[0].Label != "Main site" || ips[2].Address != "192.168.50.5" {
		t.Errorf("FloatingIPs() = %+v", ips)
	}
	if s := v.Settings(0, 1, ""); !strings.Contains(s, "\nEXTRA_VIPS=10.10.20.6/24,192.168.50.5/32\n") {
		t.Errorf("settings missing EXTRA_VIPS:\n%s", s)
	}
	if s := sample().Settings(0, 1, ""); !strings.Contains(s, "\nEXTRA_VIPS=\n") {
		t.Errorf("EXTRA_VIPS should be empty without extra addresses:\n%s", s)
	}
	// The maximum is accepted
	c = sample()
	for i := 0; i < MaxFloatingIPs-1; i++ {
		c.ExtraVIPs = append(c.ExtraVIPs, FloatingIP{Address: fmt.Sprintf("10.10.30.%d", i+1), Prefix: 24})
	}
	v, err = c.Validate()
	if err != nil {
		t.Fatalf("%d floating IPs rejected: %v", MaxFloatingIPs, err)
	}
	conf, res, out := runHelper(t, v.Settings(0, 8, ""), true)
	if res.Status != "ok" || strings.Count(conf, " dev ens18\n") != MaxFloatingIPs {
		t.Fatalf("helper did not render %d floating IPs: %+v\n%s\n%s", MaxFloatingIPs, res, out, conf)
	}
}

func TestSettingsAndSelf(t *testing.T) {
	c := sample()
	self := c.FindSelf([]net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("10.10.20.11")})
	if self != 1 {
		t.Fatalf("self = %d", self)
	}
	s := c.Settings(self, 42, "")
	for _, want := range []string{"APPLY_ID=42", "ENABLED=1", "NODE_NAME=zoraxy2", "LOCAL_IP=10.10.20.11", "PEER_IPS=10.10.20.10", "PRIORITY=100", "PREEMPT=0"} {
		if !strings.Contains(s, want+"\n") {
			t.Errorf("settings missing %s:\n%s", want, s)
		}
	}
	if s := c.Settings(-1, 7, "zoraxy9"); !strings.Contains(s, "ENABLED=0\n") || strings.Contains(s, "VIP=") {
		t.Errorf("not-a-member settings: %s", s)
	}
	if c.ActiveNode() != "zoraxy1" {
		t.Errorf("active node %s", c.ActiveNode())
	}
}

// runHelper runs deploy/waymark-ha-apply against a temp folder (no systemctl,
// no interface checks) and returns the rendered config and result.
func runHelper(t *testing.T, settings string, fakeKeepalived bool, extraEnv ...string) (string, ApplyResult, string) {
	t.Helper()
	dir := t.TempDir()
	haDir, stateDir := filepath.Join(dir, "ha"), filepath.Join(dir, "state")
	os.MkdirAll(haDir, 0750)
	os.MkdirAll(stateDir, 0750)
	if settings != "" {
		os.WriteFile(filepath.Join(haDir, "cluster.env"), []byte(settings), 0640)
	}
	u, _ := user.Current()
	cmd := exec.Command("bash", "deploy/waymark-ha-apply")
	env := append(os.Environ(), "HA_DIR="+haDir, "HA_STATE_DIR="+stateDir, "KEEPALIVED_CONF="+filepath.Join(dir, "etc", "keepalived.conf"),
		"HA_OWNER="+u.Username, "HA_SKIP_IFCHECK=1", "HA_SKIP_SYSTEMCTL=1")
	if fakeKeepalived {
		bin := filepath.Join(dir, "bin")
		os.MkdirAll(bin, 0755)
		os.WriteFile(filepath.Join(bin, "keepalived"), []byte("#!/bin/sh\nexit 0\n"), 0755)
		env = append(env, "PATH="+bin+":"+os.Getenv("PATH"))
	}
	cmd.Env = append(env, extraEnv...)
	out, _ := cmd.CombinedOutput()
	conf, _ := os.ReadFile(filepath.Join(dir, "etc", "keepalived.conf"))
	rt := Runtime{Dir: haDir, StateDir: stateDir}
	res, _ := rt.ReadResult()
	return string(conf), res, string(out)
}

func haveKeepalived() bool {
	_, err := exec.LookPath("keepalived")
	return err == nil
}

func TestPreviewMatchesHelper(t *testing.T) {
	u, _ := user.Current()
	for _, mod := range []func(*Config){
		func(c *Config) {},
		func(c *Config) { c.Password = ""; c.Preempt = true },
		func(c *Config) {
			c.Nodes = append(c.Nodes, Node{Name: "zoraxy3", Address: "10.10.20.12", Interface: "eth0", Priority: 50})
		},
		func(c *Config) { c.Enabled = false },
		func(c *Config) {
			c.ExtraVIPs = []FloatingIP{{Address: "10.10.20.6", Prefix: 24, Label: "Internal"}, {Address: "172.16.5.9", Prefix: 32}}
		},
	} {
		c := sample()
		mod(c)
		for self := range c.Nodes {
			conf, res, out := runHelper(t, c.Settings(self, 5, c.Nodes[self].Name), true)
			if res.Status != "ok" || res.ApplyID != 5 {
				t.Fatalf("helper failed: %+v\n%s", res, out)
			}
			if want := c.Preview(self, u.Username, DefaultScriptsDir); conf != want {
				t.Fatalf("preview differs from helper output\n--- helper\n%s\n--- preview\n%s", conf, want)
			}
		}
	}
}

func TestHelperRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"unknown key":   "APPLY_ID=9\nENABLED=1\nEVIL=1\n",
		"bad vip":       strings.Replace(sample().Settings(0, 9, ""), "VIP=10.10.20.5", "VIP=10.10.20.5;reboot", 1),
		"bad iface":     strings.Replace(sample().Settings(0, 9, ""), "INTERFACE=ens18", "INTERFACE=ens18 x", 1),
		"bad prio":      strings.Replace(sample().Settings(0, 9, ""), "PRIORITY=150", "PRIORITY=999", 1),
		"bad password":  strings.Replace(sample().Settings(0, 9, ""), "PASSWORD=s3cret", "PASSWORD=$(id)", 1),
		"peer is self":  strings.Replace(sample().Settings(0, 9, ""), "PEER_IPS=10.10.20.11", "PEER_IPS=10.10.20.10", 1),
		"duplicate key": sample().Settings(0, 9, "") + "VIP=10.10.20.6\n",
		"vip is self":   strings.Replace(sample().Settings(0, 9, ""), "VIP=10.10.20.5", "VIP=10.10.20.10", 1),
		"extra inject":  withExtraVIPs("10.10.20.6/24;reboot"),
		"extra no pfx":  withExtraVIPs("10.10.20.6"),
		"extra bad pfx": withExtraVIPs("10.10.20.6/33"),
		"extra bad ip":  withExtraVIPs("10.10.20.256/24"),
		"extra is vip":  withExtraVIPs("10.10.20.5/24"),
		"extra is peer": withExtraVIPs("10.10.20.11/24"),
		"extra twice":   withExtraVIPs("10.10.20.6/24,10.10.20.6/24"),
		"extra many":    withExtraVIPs(strings.Repeat("10.1.1.1/24,", 15) + "10.1.1.1/24"),
	}
	for name, settings := range cases {
		conf, res, out := runHelper(t, settings, true)
		if res.Status != "error" || conf != "" {
			t.Errorf("%s: expected rejection, got %+v (%s)", name, res, out)
		}
		if res.ApplyID != 9 {
			t.Errorf("%s: error not matched to the apply ID: %+v", name, res)
		}
		if strings.Contains(name, "extra") && !strings.Contains(res.Message, "EXTRA_VIPS") {
			t.Errorf("%s: rejected for another reason: %s", name, res.Message)
		}
		if strings.Contains(res.Message, "reboot") || strings.Contains(res.Message, "$(id)") {
			t.Errorf("%s: message echoes the value: %s", name, res.Message)
		}
	}
}

// withExtraVIPs is sample()'s settings for node 0 with EXTRA_VIPS set to v.
func withExtraVIPs(v string) string {
	return strings.Replace(sample().Settings(0, 9, ""), "EXTRA_VIPS=\n", "EXTRA_VIPS="+v+"\n", 1)
}

func TestHelperRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	haDir, stateDir := filepath.Join(dir, "ha"), filepath.Join(dir, "state")
	os.MkdirAll(haDir, 0750)
	os.MkdirAll(stateDir, 0750)
	secret := filepath.Join(dir, "secret")
	os.WriteFile(secret, []byte("VIP=1.2.3.4\nroot:hash\n"), 0600)
	os.Symlink(secret, filepath.Join(haDir, "cluster.env"))
	u, _ := user.Current()
	cmd := exec.Command("bash", "deploy/waymark-ha-apply")
	cmd.Env = append(os.Environ(), "HA_DIR="+haDir, "HA_STATE_DIR="+stateDir, "KEEPALIVED_CONF="+filepath.Join(dir, "k.conf"), "HA_OWNER="+u.Username, "HA_SKIP_IFCHECK=1", "HA_SKIP_SYSTEMCTL=1")
	cmd.Run()
	res, _ := (&Runtime{Dir: haDir, StateDir: stateDir}).ReadResult()
	if res.Status != "error" || !strings.Contains(res.Message, "symlink") || strings.Contains(res.Message, "root:hash") {
		t.Fatalf("symlink not rejected safely: %+v", res)
	}
}

func TestRealKeepalivedAcceptsConfig(t *testing.T) {
	if !haveKeepalived() {
		t.Skip("keepalived not installed")
	}
	// keepalived checks that the interface exists: use one this machine has
	ifaces, _ := net.Interfaces()
	name := "lo"
	for _, i := range ifaces {
		if i.Flags&net.FlagLoopback == 0 && len(i.Name) <= 15 {
			name = i.Name
			break
		}
	}
	// keepalived also requires the scripts to exist in a folder only root can write
	if os.Geteuid() != 0 {
		t.Skip("needs root to create a root-owned scripts folder")
	}
	scripts, err := os.MkdirTemp("/usr/local/lib", "zoraxy-ha-test-")
	if err != nil {
		t.Skip("cannot create a scripts folder: " + err.Error())
	}
	defer os.RemoveAll(scripts)
	os.Chmod(scripts, 0755)
	for _, n := range []string{"check", "notify"} {
		b, _ := os.ReadFile("deploy/" + n)
		os.WriteFile(filepath.Join(scripts, n), b, 0755)
	}
	c := sample()
	c.Nodes[0].Interface, c.Nodes[1].Interface = name, name
	_, res, out := runHelper(t, c.Settings(0, 3, ""), false, "HA_SCRIPTS_DIR="+scripts)
	if res.Status != "ok" {
		t.Fatalf("keepalived -t rejected the config: %+v\n%s", res, out)
	}
	// Several floating IPs are accepted too
	c.ExtraVIPs = []FloatingIP{{Address: "10.10.20.6", Prefix: 24}, {Address: "172.16.9.9", Prefix: 32}}
	if _, res, out := runHelper(t, c.Settings(0, 5, ""), false, "HA_SCRIPTS_DIR="+scripts); res.Status != "ok" {
		t.Fatalf("keepalived -t rejected several floating IPs: %+v\n%s", res, out)
	}
	c.ExtraVIPs = nil
	// ...and a missing interface is caught by keepalived -t
	c.Nodes[0].Interface = "nosuchif0"
	if _, res, _ := runHelper(t, c.Settings(0, 4, ""), false, "HA_SCRIPTS_DIR="+scripts); res.Status != "error" || !strings.Contains(res.Message, "nosuchif0") {
		t.Fatalf("missing interface not caught: %+v", res)
	}
}

func TestRuntimeFiles(t *testing.T) {
	dir := t.TempDir()
	rt := &Runtime{Dir: filepath.Join(dir, "ha"), StateDir: filepath.Join(dir, "state")}
	if ok, _ := rt.Installed(); ok {
		t.Fatal("installed without folders")
	}
	os.MkdirAll(rt.Dir, 0750)
	os.MkdirAll(rt.StateDir, 0750)
	if ok, why := rt.Installed(); !ok {
		t.Fatalf("not installed: %s", why)
	}
	if r := rt.ReadRole(); r.State != "" {
		t.Fatalf("role before notify: %+v", r)
	}
	// notify script writes the role
	cmd := exec.Command("bash", "deploy/notify", "INSTANCE", "WAYMARK", "MASTER", "150")
	cmd.Env = append(os.Environ(), "HA_DIR="+rt.Dir)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if r := rt.ReadRole(); r.State != "MASTER" || r.Since == 0 {
		t.Fatalf("role: %+v", r)
	}
	exec.Command("bash", "-c", "HA_DIR="+rt.Dir+" bash deploy/notify INSTANCE WAYMARK 'EVIL; rm' 1").Run()
	if r := rt.ReadRole(); r.State != "MASTER" {
		t.Fatalf("invalid state accepted: %+v", r)
	}
	// check script follows the heartbeat
	check := func() bool {
		c := exec.Command("bash", "deploy/check")
		c.Env = append(os.Environ(), "HA_DIR="+rt.Dir)
		return c.Run() == nil
	}
	if check() {
		t.Fatal("healthy without a heartbeat")
	}
	rt.WriteHeartbeat(true, "", time.Now())
	if !check() {
		t.Fatal("fresh ok heartbeat not healthy")
	}
	rt.WriteHeartbeat(false, "listener down", time.Now())
	if check() {
		t.Fatal("fail heartbeat counted healthy")
	}
	rt.WriteHeartbeat(true, "", time.Now().Add(-10*time.Second))
	if check() {
		t.Fatal("stale heartbeat counted healthy")
	}
}

func TestExportInstaller(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kit")
	if err := ExportInstaller(dir); err != nil {
		t.Fatal(err)
	}
	for _, n := range DeployFileNames {
		st, err := os.Stat(filepath.Join(dir, n))
		if err != nil {
			t.Fatalf("%s missing", n)
		}
		b, _ := os.ReadFile(filepath.Join(dir, n))
		if strings.Contains(string(b), "\r") {
			t.Errorf("%s has CR line endings", n)
		}
		if (n == "install.sh" || n == "check" || n == "notify" || n == "waymark-ha-apply") && st.Mode()&0100 == 0 {
			t.Errorf("%s not executable", n)
		}
	}
}

// keepaliveLab runs the helper repeatedly against one folder with fake
// keepalived and systemctl binaries. The fake systemctl keeps keepalived's
// state in a file and logs every call; HANG=1 makes start/restart hang
// (keepalived never reporting ready to systemd).
type keepaliveLab struct {
	t                         *testing.T
	dir, haDir, stateDir, bin string
	user                      string
}

func newKeepaliveLab(t *testing.T) *keepaliveLab {
	dir := t.TempDir()
	l := &keepaliveLab{t: t, dir: dir, haDir: filepath.Join(dir, "ha"), stateDir: filepath.Join(dir, "state"), bin: filepath.Join(dir, "bin")}
	for _, d := range []string{l.haDir, l.stateDir, l.bin} {
		os.MkdirAll(d, 0755)
	}
	u, _ := user.Current()
	l.user = u.Username
	os.WriteFile(filepath.Join(l.bin, "keepalived"), []byte("#!/bin/sh\nexit 0\n"), 0755)
	fake := `#!/bin/bash
echo "$*" >> "$LAB/calls"
st="$(cat "$LAB/kstate" 2>/dev/null || echo inactive)"
case "$*" in
"is-active --quiet keepalived") [ "$st" = active ] ;;
"enable keepalived") exit 0 ;;
"restart keepalived")
	[ -f "$LAB/onrestart" ] && { bash "$LAB/onrestart"; rm -f "$LAB/onrestart"; }
	if [ -f "$LAB/hang" ]; then sleep 10; exit 1; fi
	echo active > "$LAB/kstate" ;;
"reload keepalived") exit 0 ;;
"stop keepalived" | "disable --now keepalived") echo inactive > "$LAB/kstate" ;;
*) exit 0 ;;
esac
`
	os.WriteFile(filepath.Join(l.bin, "systemctl"), []byte(fake), 0755)
	return l
}

func (l *keepaliveLab) settings(applyID int64, enabled bool, priority int) string {
	c := &Config{Enabled: enabled, VIP: "10.0.0.5", Prefix: 24, RouterID: 51, Nodes: []Node{
		{Name: "a", Address: "10.0.0.10", Interface: "eth0", Priority: priority},
		{Name: "b", Address: "10.0.0.11", Interface: "eth0", Priority: 10},
	}}
	return c.Settings(0, applyID, "a")
}

func (l *keepaliveLab) save(content string) {
	os.WriteFile(filepath.Join(l.haDir, "cluster.env"), []byte(content), 0640)
}

func (l *keepaliveLab) run() (ApplyResult, string, time.Duration) {
	os.Remove(filepath.Join(l.dir, "calls"))
	cmd := exec.Command("bash", "deploy/waymark-ha-apply")
	cmd.Env = append(os.Environ(), "HA_DIR="+l.haDir, "HA_STATE_DIR="+l.stateDir, "KEEPALIVED_CONF="+filepath.Join(l.dir, "etc", "keepalived.conf"),
		"HA_OWNER="+l.user, "HA_SKIP_IFCHECK=1", "HA_SYSTEMCTL_TIMEOUT=1", "LAB="+l.dir, "PATH="+l.bin+":"+os.Getenv("PATH"))
	start := time.Now()
	out, _ := cmd.CombinedOutput()
	res, _ := (&Runtime{Dir: l.haDir, StateDir: l.stateDir}).ReadResult()
	calls, _ := os.ReadFile(filepath.Join(l.dir, "calls"))
	l.t.Logf("helper output: %s", out)
	return res, string(calls), time.Since(start)
}

func (l *keepaliveLab) state() string {
	b, _ := os.ReadFile(filepath.Join(l.dir, "kstate"))
	return strings.TrimSpace(string(b))
}

func (l *keepaliveLab) conf() string {
	b, _ := os.ReadFile(filepath.Join(l.dir, "etc", "keepalived.conf"))
	return string(b)
}

func TestHelperManagesKeepalived(t *testing.T) {
	l := newKeepaliveLab(t)

	// HA on: keepalived enabled and started
	l.save(l.settings(1, true, 150))
	res, calls, _ := l.run()
	if res.Status != "ok" || res.ApplyID != 1 || l.state() != "active" || !strings.Contains(calls, "enable keepalived") || !strings.Contains(calls, "restart keepalived") {
		t.Fatalf("enable: %+v state=%s calls=%q", res, l.state(), calls)
	}

	// Already running: reloaded, not restarted
	l.save(l.settings(2, true, 140))
	res, calls, _ = l.run()
	if res.Status != "ok" || !strings.Contains(calls, "reload keepalived") || strings.Contains(calls, "restart") {
		t.Fatalf("reload: %+v calls=%q", res, calls)
	}

	// HA off: keepalived stopped and disabled
	l.save(l.settings(3, false, 140))
	res, calls, _ = l.run()
	if res.Status != "ok" || l.state() != "inactive" || !strings.Contains(calls, "disable --now keepalived") || !strings.Contains(res.Message, "stopped") {
		t.Fatalf("off: %+v state=%s calls=%q", res, l.state(), calls)
	}

	// keepalived hangs on start and there is no earlier HA config to go back to
	os.WriteFile(filepath.Join(l.dir, "hang"), nil, 0644)
	l.save(l.settings(4, true, 150))
	res, calls, took := l.run()
	if res.Status != "error" || res.ApplyID != 4 || !strings.Contains(res.Message, "keepalived is stopped") || strings.Contains(res.Message, "restored") || took > 8*time.Second {
		t.Fatalf("hang without previous: %+v took=%s calls=%q", res, took, calls)
	}

	// A working config, then a start that hangs: the working config comes back
	os.Remove(filepath.Join(l.dir, "hang"))
	l.save(l.settings(5, true, 150))
	if res, _, _ = l.run(); res.Status != "ok" {
		t.Fatalf("recover: %+v", res)
	}
	os.WriteFile(filepath.Join(l.dir, "kstate"), []byte("inactive"), 0644)
	os.WriteFile(filepath.Join(l.dir, "hang"), nil, 0644)
	l.save(l.settings(6, true, 120))
	res, _, _ = l.run()
	if res.Status != "error" || !strings.Contains(res.Message, "previous config was restored") || !strings.Contains(l.conf(), "priority 150") {
		t.Fatalf("restore: %+v\n%s", res, l.conf())
	}

	// Waymark saves again while the helper is busy: the newer save is applied too
	os.Remove(filepath.Join(l.dir, "hang"))
	os.WriteFile(filepath.Join(l.dir, "kstate"), []byte("inactive"), 0644)
	newer := l.settings(8, true, 110)
	os.WriteFile(filepath.Join(l.dir, "onrestart"), []byte("cat > "+filepath.Join(l.haDir, "cluster.env")+" <<'X'\n"+newer+"X\n"), 0644)
	l.save(l.settings(7, true, 100))
	res, _, _ = l.run()
	if res.Status != "ok" || res.ApplyID != 8 || !strings.Contains(l.conf(), "priority 110") {
		t.Fatalf("missed save: %+v\n%s", res, l.conf())
	}
}

func TestOutdatedInstalledFiles(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "waymark-ha-apply")
	scripts := filepath.Join(dir, "lib")
	os.MkdirAll(scripts, 0755)
	if _, ok := OutdatedInstalledFiles(helper, scripts); ok {
		t.Fatal("missing files reported as readable")
	}
	b, _ := os.ReadFile("deploy/waymark-ha-apply")
	// The service user set by install.sh is not a difference
	b = []byte(strings.Replace(string(b), `OWNER="${HA_OWNER:-waymark}"`, `OWNER="${HA_OWNER:-proxy}"`, 1))
	os.WriteFile(helper, b, 0755)
	for _, n := range []string{"check", "notify"} {
		c, _ := os.ReadFile("deploy/" + n)
		os.WriteFile(filepath.Join(scripts, n), c, 0755)
	}
	if out, ok := OutdatedInstalledFiles(helper, scripts); !ok || len(out) != 0 {
		t.Fatalf("current files reported outdated: %v %v", out, ok)
	}
	os.WriteFile(helper, append(b, []byte("# older\n")...), 0755)
	if out, ok := OutdatedInstalledFiles(helper, scripts); !ok || len(out) != 1 || out[0] != "waymark-ha-apply" {
		t.Fatalf("changed helper not detected: %v", out)
	}
}
