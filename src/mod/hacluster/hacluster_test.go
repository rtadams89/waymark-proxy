package hacluster

import (
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
	}
	for name, mod := range bad {
		c := sample()
		mod(c)
		if _, err := c.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
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

// runHelper runs deploy/zoraxy-ha-apply against a temp folder (no systemctl,
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
	cmd := exec.Command("bash", "deploy/zoraxy-ha-apply")
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
	}
	for name, settings := range cases {
		conf, res, out := runHelper(t, settings, true)
		if res.Status != "error" || conf != "" {
			t.Errorf("%s: expected rejection, got %+v (%s)", name, res, out)
		}
		if res.ApplyID != 9 {
			t.Errorf("%s: error not matched to the apply ID: %+v", name, res)
		}
		if strings.Contains(res.Message, "reboot") || strings.Contains(res.Message, "$(id)") {
			t.Errorf("%s: message echoes the value: %s", name, res.Message)
		}
	}
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
	cmd := exec.Command("bash", "deploy/zoraxy-ha-apply")
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
	cmd := exec.Command("bash", "deploy/notify", "INSTANCE", "ZORAXY", "MASTER", "150")
	cmd.Env = append(os.Environ(), "HA_DIR="+rt.Dir)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if r := rt.ReadRole(); r.State != "MASTER" || r.Since == 0 {
		t.Fatalf("role: %+v", r)
	}
	exec.Command("bash", "-c", "HA_DIR="+rt.Dir+" bash deploy/notify INSTANCE ZORAXY 'EVIL; rm' 1").Run()
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
		if (n == "install.sh" || n == "check" || n == "notify" || n == "zoraxy-ha-apply") && st.Mode()&0100 == 0 {
			t.Errorf("%s not executable", n)
		}
	}
}
