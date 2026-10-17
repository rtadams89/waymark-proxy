// Package hacluster (custom fork) is Zoraxy's side of an active/passive pair
// run by keepalived.
//
// Zoraxy runs as an unprivileged user, so it never writes keepalived's
// config. It writes a small settings file for this node (cluster.env) into
// a folder it owns; a root-owned helper (deploy/zoraxy-ha-apply, started by
// a systemd path unit) validates it, renders keepalived.conf from a fixed
// template, tests it and reloads keepalived, then reports the result.
//
// keepalived in turn runs two root-owned scripts as the zoraxy user: a
// health check that reads Zoraxy's heartbeat file, and a notify script that
// records the node's role (MASTER, BACKUP, FAULT) for Zoraxy to read.
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

// Node is one Zoraxy instance in the pair.
type Node struct {
	Name      string `json:"name"`      // lowercase letters, digits and dashes
	Address   string `json:"address"`   // the node's own IPv4 address on the shared VLAN
	Interface string `json:"interface"` // network interface carrying that address, e.g. ens18
	Priority  int    `json:"priority"`  // 1-254; the highest healthy node holds the floating IP
}

// Config is the cluster definition, shared by all nodes.
type Config struct {
	Enabled  bool   `json:"enabled"`
	VIP      string `json:"vip"`      // floating IPv4 address
	Prefix   int    `json:"prefix"`   // its prefix length, e.g. 24
	RouterID int    `json:"routerId"` // VRRP virtual router ID, 1-255, unique on the VLAN
	Password string `json:"password"` // optional VRRP password, up to 8 letters/digits
	Preempt  bool   `json:"preempt"`  // take the floating IP back when a higher-priority node recovers
	Nodes    []Node `json:"nodes"`
}

// DefaultConfig is the starting point shown in the UI.
func DefaultConfig() *Config {
	return &Config{Prefix: 24, RouterID: 51, Nodes: []Node{}}
}

var (
	namePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	ifacePattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,15}$`)
	passwordPattern = regexp.MustCompile(`^[A-Za-z0-9]{0,8}$`)
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
	v.Nodes = append([]Node{}, c.Nodes...)
	if !v.Enabled && v.VIP == "" && len(v.Nodes) == 0 {
		return &v, nil
	}
	vip, ok := parseIPv4(v.VIP)
	if !ok {
		return nil, errors.New("floating IP must be an IPv4 address")
	}
	if v.Prefix < 1 || v.Prefix > 32 {
		return nil, errors.New("prefix length must be 1-32")
	}
	if v.RouterID < 1 || v.RouterID > 255 {
		return nil, errors.New("router ID must be 1-255")
	}
	if !passwordPattern.MatchString(v.Password) {
		return nil, errors.New("password must be up to 8 letters or digits (VRRP limit)")
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
		if ip.Equal(vip) {
			return nil, fmt.Errorf("%s: address must differ from the floating IP", label)
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
	b.WriteString("# Written by Zoraxy (HA settings for this node). Applied by zoraxy-ha-apply.\n")
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
