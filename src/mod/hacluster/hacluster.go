// Package hacluster (custom fork) is Waymark's side of an active/passive pair
// run by keepalived.
//
// Waymark runs as an unprivileged user, so it never writes keepalived's
// config. It writes a small settings file for this node (cluster.env) into
// a folder it owns; a root-owned helper (deploy/waymark-ha-apply, started by
// a systemd path unit) validates it, renders keepalived.conf from a fixed
// template, tests it and reloads keepalived, then reports the result.
//
// keepalived in turn runs two root-owned scripts as the waymark user: a
// health check that reads Waymark's heartbeat file, and a notify script that
// records the node's role (MASTER, BACKUP, FAULT) for Waymark to read.
package hacluster

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Node is one Waymark instance in the pair.
type Node struct {
	Name      string `json:"name"`      // lowercase letters, digits and dashes
	Address   string `json:"address"`   // the node's own IPv4 address on the shared VLAN
	Interface string `json:"interface"` // network interface carrying that address, e.g. ens18
	Priority  int    `json:"priority"`  // 1-254; the highest healthy node holds the floating IP
}

// FloatingIP is one additional floating address. All floating IPs belong
// to one VRRP group: the active node holds every one of them.
type FloatingIP struct {
	Address string `json:"address"` // IPv4 address
	Prefix  int    `json:"prefix"`  // its prefix length, e.g. 24
	Label   string `json:"label"`   // optional note, shown in Waymark only
}

// MaxFloatingIPs is the most floating IPs a cluster can have (VIP + extras).
const MaxFloatingIPs = 16

// Config is the cluster definition, shared by all nodes.
type Config struct {
	Enabled  bool   `json:"enabled"`
	VIP      string `json:"vip"`      // primary floating IPv4 address; node addresses are in its subnet
	Prefix   int    `json:"prefix"`   // its prefix length, e.g. 24
	VIPLabel string `json:"vipLabel"` // optional note for the primary floating IP

	// ExtraVIPs are more floating IPs on the same interface, moved together
	// with VIP
	ExtraVIPs []FloatingIP `json:"extraVips"`

	RouterID int    `json:"routerId"` // VRRP virtual router ID, 1-255, unique on the VLAN
	Password string `json:"password"` // optional VRRP password, up to 8 letters/digits
	Preempt  bool   `json:"preempt"`  // take the floating IP back when a higher-priority node recovers
	Nodes    []Node `json:"nodes"`

	// Configuration sync (phase 2): standby nodes pull the active node's
	// configuration over TLS, authenticated with the cluster key
	SyncEnabled bool   `json:"syncEnabled"`
	SyncPort    int    `json:"syncPort"`   // TCP port of each node's sync listener
	ClusterKey  string `json:"clusterKey"` // 64 hex characters, the same on every node
}

// DefaultSyncPort is the sync listener's port unless set otherwise.
const DefaultSyncPort = 9280

// DefaultConfig is the starting point shown in the UI.
func DefaultConfig() *Config {
	return &Config{Prefix: 24, RouterID: 51, Nodes: []Node{}, ExtraVIPs: []FloatingIP{}, SyncPort: DefaultSyncPort}
}

var (
	namePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	ifacePattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,15}$`)
	passwordPattern = regexp.MustCompile(`^[A-Za-z0-9]{0,8}$`)
	labelPattern    = regexp.MustCompile(`^[A-Za-z0-9 ._()/+-]{0,32}$`)
)

func parseIPv4(s string) (net.IP, bool) {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil || ip.To4() == nil || strings.Contains(s, ":") {
		return nil, false
	}
	return ip.To4(), true
}

// Validate returns a normalised copy, or an error naming the first problem.
// A disabled config is still validated if it has nodes, so it can be saved
// and enabled later.
func (c *Config) Validate() (*Config, error) {
	if c == nil {
		return nil, errors.New("no settings given")
	}
	v := *c
	v.VIP = strings.TrimSpace(v.VIP)
	v.VIPLabel = strings.TrimSpace(v.VIPLabel)
	v.Nodes = append([]Node{}, c.Nodes...)
	v.ExtraVIPs = append([]FloatingIP{}, c.ExtraVIPs...)
	if !v.Enabled && v.VIP == "" && len(v.Nodes) == 0 && len(v.ExtraVIPs) == 0 {
		return &v, nil
	}
	vip, ok := parseIPv4(v.VIP)
	if !ok {
		return nil, errors.New("floating IP must be an IPv4 address")
	}
	if v.Prefix < 1 || v.Prefix > 32 {
		return nil, errors.New("prefix length must be 1-32")
	}
	if !labelPattern.MatchString(v.VIPLabel) {
		return nil, errors.New("floating IP label: up to 32 letters, digits, spaces and . _ ( ) / + -")
	}
	if 1+len(v.ExtraVIPs) > MaxFloatingIPs {
		return nil, fmt.Errorf("at most %d floating IPs", MaxFloatingIPs)
	}
	floating := map[string]bool{vip.String(): true}
	for i := range v.ExtraVIPs {
		f := &v.ExtraVIPs[i]
		f.Address, f.Label = strings.TrimSpace(f.Address), strings.TrimSpace(f.Label)
		label := fmt.Sprintf("floating IP %d", i+2)
		ip, ok := parseIPv4(f.Address)
		if !ok {
			return nil, fmt.Errorf("%s must be an IPv4 address", label)
		}
		label = "floating IP " + ip.String()
		if f.Prefix < 1 || f.Prefix > 32 {
			return nil, fmt.Errorf("%s: prefix length must be 1-32", label)
		}
		if !labelPattern.MatchString(f.Label) {
			return nil, fmt.Errorf("%s: label can have up to 32 letters, digits, spaces and . _ ( ) / + -", label)
		}
		if floating[ip.String()] {
			return nil, fmt.Errorf("%s is listed twice", label)
		}
		floating[ip.String()] = true
		f.Address = ip.String()
	}
	if v.RouterID < 1 || v.RouterID > 255 {
		return nil, errors.New("router ID must be 1-255")
	}
	if !passwordPattern.MatchString(v.Password) {
		return nil, errors.New("password must be up to 8 letters or digits (VRRP limit)")
	}
	if v.SyncPort == 0 {
		v.SyncPort = DefaultSyncPort
	}
	v.ClusterKey = strings.ToLower(strings.TrimSpace(v.ClusterKey))
	if v.SyncPort < 1 || v.SyncPort > 65535 {
		return nil, errors.New("sync port must be 1-65535")
	}
	if v.SyncEnabled && !clusterKeyPattern.MatchString(v.ClusterKey) {
		return nil, errors.New("configuration sync needs a cluster key (64 hex characters); use Generate")
	}
	if v.ClusterKey != "" && !clusterKeyPattern.MatchString(v.ClusterKey) {
		return nil, errors.New("cluster key must be 64 hex characters")
	}
	if len(v.Nodes) < 2 || len(v.Nodes) > 4 {
		return nil, errors.New("add 2 to 4 nodes")
	}
	subnet := &net.IPNet{IP: vip.Mask(net.CIDRMask(v.Prefix, 32)), Mask: net.CIDRMask(v.Prefix, 32)}
	names, addrs, prios := map[string]bool{}, map[string]bool{}, map[int]bool{}
	for i := range v.Nodes {
		n := &v.Nodes[i]
		n.Name = strings.ToLower(strings.TrimSpace(n.Name))
		n.Address = strings.TrimSpace(n.Address)
		n.Interface = strings.TrimSpace(n.Interface)
		label := fmt.Sprintf("node %d", i+1)
		if !namePattern.MatchString(n.Name) {
			return nil, fmt.Errorf("%s: name must be lowercase letters, digits and dashes (up to 32)", label)
		}
		label = "node " + n.Name
		ip, ok := parseIPv4(n.Address)
		if !ok {
			return nil, fmt.Errorf("%s: address must be an IPv4 address", label)
		}
		if floating[ip.String()] {
			return nil, fmt.Errorf("%s: address must differ from the floating IPs", label)
		}
		if !subnet.Contains(ip) {
			return nil, fmt.Errorf("%s: address %s is not in the floating IP's subnet %s", label, n.Address, subnet)
		}
		if !ifacePattern.MatchString(n.Interface) {
			return nil, fmt.Errorf("%s: interface name is invalid", label)
		}
		if n.Priority < 1 || n.Priority > 254 {
			return nil, fmt.Errorf("%s: priority must be 1-254", label)
		}
		if names[n.Name] || addrs[ip.String()] || prios[n.Priority] {
			return nil, fmt.Errorf("%s: names, addresses and priorities must all be different", label)
		}
		names[n.Name], addrs[ip.String()], prios[n.Priority] = true, true, true
		n.Address = ip.String()
	}
	v.VIP = vip.String()
	return &v, nil
}

// FloatingIPs returns every floating IP, the primary one first.
func (c *Config) FloatingIPs() []FloatingIP {
	out := []FloatingIP{}
	if c.VIP != "" {
		out = append(out, FloatingIP{Address: c.VIP, Prefix: c.Prefix, Label: c.VIPLabel})
	}
	return append(out, c.ExtraVIPs...)
}

// FindSelf returns the index of the node whose address is assigned to this
// machine, or -1.
func (c *Config) FindSelf(local []net.IP) int {
	for i, n := range c.Nodes {
		ip, ok := parseIPv4(n.Address)
		if !ok {
			continue
		}
		for _, l := range local {
			if l.Equal(ip) {
				return i
			}
		}
	}
	return -1
}

// Settings renders cluster.env for node self. With self < 0 (or HA off)
// only ENABLED=0 is meaningful.
func (c *Config) Settings(self int, applyID int64, nodeName string) string {
	var b strings.Builder
	w := func(k, v string) { b.WriteString(k + "=" + v + "\n") }
	b.WriteString("# Written by Waymark (HA settings for this node). Applied by waymark-ha-apply.\n")
	w("APPLY_ID", strconv.FormatInt(applyID, 10))
	if !c.Enabled || self < 0 || self >= len(c.Nodes) {
		w("ENABLED", "0")
		w("NODE_NAME", nodeName)
		return b.String()
	}
	me := c.Nodes[self]
	peers := []string{}
	for i, n := range c.Nodes {
		if i != self {
			peers = append(peers, n.Address)
		}
	}
	w("ENABLED", "1")
	w("NODE_NAME", me.Name)
	w("INTERFACE", me.Interface)
	w("LOCAL_IP", me.Address)
	w("PEER_IPS", strings.Join(peers, ","))
	w("VIP", c.VIP)
	w("PREFIX", strconv.Itoa(c.Prefix))
	if len(c.ExtraVIPs) > 0 {
		// Only written when there are extra addresses, so a single floating
		// IP still works with a helper from before 2.2.0
		extra := []string{}
		for _, f := range c.ExtraVIPs {
			extra = append(extra, f.Address+"/"+strconv.Itoa(f.Prefix))
		}
		w("EXTRA_VIPS", strings.Join(extra, ","))
	}
	w("ROUTER_ID", strconv.Itoa(c.RouterID))
	w("PRIORITY", strconv.Itoa(me.Priority))
	w("PASSWORD", c.Password)
	preempt := "0"
	if c.Preempt {
		preempt = "1"
	}
	w("PREEMPT", preempt)
	return b.String()
}

// ActiveNode returns the name of the node expected to hold the floating IP
// when all are healthy (highest priority).
func (c *Config) ActiveNode() string {
	nodes := append([]Node{}, c.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Priority > nodes[j].Priority })
	if len(nodes) == 0 {
		return ""
	}
	return nodes[0].Name
}
