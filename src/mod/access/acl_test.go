package access

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imuslab.com/zoraxy/mod/database"
	"imuslab.com/zoraxy/mod/database/dbinc"
	"imuslab.com/zoraxy/mod/info/logger"
	"imuslab.com/zoraxy/mod/netutils"
)

var testCountries = map[string]string{"1.1.1.1": "AU", "8.8.8.8": "US", "5.5.5.5": "DE", "9.9.9.9": "CN"}

func newACLController(t *testing.T) *Controller {
	t.Helper()
	dir := t.TempDir()
	db, err := database.NewDatabase(filepath.Join(dir, "sys.db"), dbinc.BackendBoltDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	l, _ := logger.NewFmtLogger()
	c := &Controller{Options: &Options{Logger: *l, ConfigFolder: filepath.Join(dir, "acl"), Database: db}, acls: map[string]*ACL{}, objects: map[string]*Object{}}
	os.MkdirAll(c.Options.ConfigFolder, 0775)
	c.countryLookup = func(ip string) (string, bool) { return testCountries[ip], true }
	c.ServerPublicIP = "203.0.113.7"
	c.mu.Lock()
	c.compileLocked()
	c.mu.Unlock()
	return c
}

func saveRules(t *testing.T, c *Controller, a *ACL, rules ...Rule) *ACL {
	t.Helper()
	a.Rules = rules
	p, err := c.PrepareACL(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SaveACL(p); err != nil {
		t.Fatal(err)
	}
	got, _ := c.GetACL(a.ID)
	return got
}

func r(action, typ, value string, not bool) Rule {
	return Rule{Enabled: true, Action: action, Source: Source{Type: typ, Value: value, Not: not}}
}

func TestACLTopDownAndDefaultDeny(t *testing.T) {
	c := newACLController(t)
	a, err := c.CreateACL("Office", "")
	if err != nil {
		t.Fatal(err)
	}
	if d := c.Evaluate(a.ID, "10.0.0.1"); d.Action != ActionDeny || d.Rule != 0 {
		t.Fatalf("empty ACL: %+v", d)
	}
	a = saveRules(t, c, a,
		r(ActionDeny, SourceIP, "10.0.0.5", false),
		r(ActionAllow, SourceCIDR, "10.0.0.0/24", false),
		r(ActionDrop, SourceRange, "192.168.1.10-192.168.1.20", false),
		r(ActionAllow, SourceWildcard, "192.168.*.*", false),
	)
	cases := map[string][2]any{
		"10.0.0.5":        {ActionDeny, 1},
		"10.0.0.6":        {ActionAllow, 2},
		"192.168.1.15":    {ActionDrop, 3},
		"192.168.2.1":     {ActionAllow, 4},
		"172.16.0.1":      {ActionDeny, 0},
		"::ffff:10.0.0.6": {ActionAllow, 2},
	}
	for ip, want := range cases {
		d := c.Evaluate(a.ID, ip)
		if d.Action != want[0] || d.Rule != want[1] {
			t.Errorf("%s: %s #%d, want %v #%v", ip, d.Action, d.Rule, want[0], want[1])
		}
	}
	// Disabled rules are passed over
	a.Rules[0].Enabled = false
	a = saveRules(t, c, a, a.Rules...)
	if d := c.Evaluate(a.ID, "10.0.0.5"); d.Action != ActionAllow || d.Rule != 2 {
		t.Fatalf("disabled rule: %+v", d)
	}
	hits := c.Hits(a.ID)
	if hits[a.Rules[1].ID] == 0 || hits[""] == 0 {
		t.Fatalf("hit counts: %v", hits)
	}
}

func TestACLNotAndCountries(t *testing.T) {
	c := newACLController(t)
	a, _ := c.CreateACL("Geo", "")
	a = saveRules(t, c, a,
		r(ActionAllow, SourceObject, PrivateNetworksID, false),
		r(ActionDeny, SourceCountry, "US", true), // not US: denied
		r(ActionAllow, SourceAny, "", false),
	)
	for ip, want := range map[string]string{"8.8.8.8": ActionAllow, "1.1.1.1": ActionDeny, "192.168.1.2": ActionAllow, "203.0.113.7": ActionAllow, "198.51.100.1": ActionDeny} {
		if d := c.Evaluate(a.ID, ip); d.Action != want {
			t.Errorf("%s: %s (rule %d), want %s", ip, d.Action, d.Rule, want)
		}
	}
	// Geo-IP off: the country rule is skipped, not "everyone is not US"
	c.countryLookup = func(string) (string, bool) { return "", false }
	d := c.Evaluate(a.ID, "1.1.1.1")
	if d.Action != ActionAllow || d.Rule != 3 || len(d.Skipped) != 1 || d.Skipped[0] != 2 {
		t.Fatalf("Geo-IP off: %+v", d)
	}
}

func TestACLValidation(t *testing.T) {
	c := newACLController(t)
	a, _ := c.CreateACL("V", "")
	bad := [][]Rule{
		{r("permit", SourceAny, "", false)},
		{r(ActionAllow, SourceAny, "", true)},
		{r(ActionAllow, SourceObject, "missing", false)},
		{r(ActionAllow, SourceIP, "10.0.0", false)},
		{r(ActionAllow, SourceRange, "10.0.0.9-10.0.0.1", false)},
		{r(ActionAllow, SourceCountry, "USA", false)},
		{r(ActionAllow, SourceWildcard, "10.*.300.1", false)},
	}
	for i, rules := range bad {
		a.Rules = rules
		if _, err := c.PrepareACL(a); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	// Revisions: a stale save is refused
	a1 := saveRules(t, c, a, r(ActionAllow, SourceAny, "", false))
	a.Rules = []Rule{}
	if _, err := c.PrepareACL(a); err != ErrConflict {
		t.Fatalf("stale revision: %v", err)
	}
	if _, err := c.PrepareACL(&ACL{ID: AllowAllID, Name: "x"}); err == nil {
		t.Fatal("built-in ACL changed")
	}
	if a1.Revision != 2 {
		t.Fatalf("revision %d", a1.Revision)
	}
	// Normalisation
	a1.Rules = []Rule{r(ActionAllow, SourceCIDR, "10.1.2.3/8", false), r(ActionAllow, SourceCountry, "de", false)}
	p, err := c.PrepareACL(a1)
	if err != nil || p.Rules[0].Source.Value != "10.0.0.0/8" || p.Rules[1].Source.Value != "DE" || p.Rules[0].ID == "" {
		t.Fatalf("normalised: %+v %v", p, err)
	}
}

func TestObjectsAndReload(t *testing.T) {
	c := newACLController(t)
	o, err := c.PrepareObject(&Object{Name: "Partners", Entries: []Entry{{Value: "10.9.0.0/16"}, {Value: "fr", Comment: "France"}, {Value: "10.9.0.0/16"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Entries) != 2 || o.Entries[1].Type != SourceCountry || o.Entries[1].Value != "FR" {
		t.Fatalf("entries: %+v", o.Entries)
	}
	if err := c.SaveObject(o); err != nil {
		t.Fatal(err)
	}
	a, _ := c.CreateACL("P", "")
	saveRules(t, c, a, r(ActionAllow, SourceObject, o.ID, false))
	if d := c.Evaluate(a.ID, "10.9.4.4"); d.Action != ActionAllow {
		t.Fatalf("object match: %+v", d)
	}
	if err := c.DeleteObject(o.ID); err == nil || !strings.Contains(err.Error(), "P") {
		t.Fatalf("delete of a used object: %v", err)
	}
	// Changing the object changes the ACL's result
	o.Entries = []Entry{{Value: "10.8.0.0/16"}}
	o2, err := c.PrepareObject(o)
	if err != nil {
		t.Fatal(err)
	}
	c.SaveObject(o2)
	if d := c.Evaluate(a.ID, "10.9.4.4"); d.Action != ActionDeny {
		t.Fatalf("after object change: %+v", d)
	}
	// Files survive a reload
	if err := c.Reload(); err != nil {
		t.Fatal(err)
	}
	if d := c.Evaluate(a.ID, "10.8.1.1"); d.Action != ActionAllow {
		t.Fatalf("after reload: %+v", d)
	}
	if len(c.ListACLs()) != 2 || len(c.ListObjects()) != 2 {
		t.Fatalf("lists: %d ACLs, %d objects", len(c.ListACLs()), len(c.ListObjects()))
	}
	// Unknown and empty IDs use Allow all
	if d := c.Evaluate("", "1.2.3.4"); d.Action != ActionAllow || d.ACLID != AllowAllID {
		t.Fatalf("no ACL: %+v", d)
	}
	if d := c.Evaluate(LegacyDefaultID, "1.2.3.4"); d.ACLID != AllowAllID {
		t.Fatalf("old default: %+v", d)
	}
}

func TestTestWith(t *testing.T) {
	c := newACLController(t)
	a, _ := c.CreateACL("T", "")
	a = saveRules(t, c, a, r(ActionAllow, SourceAny, "", false))
	cand := copyACL(a)
	cand.Rules = []Rule{r(ActionDeny, SourceIP, "10.0.0.1", false), r(ActionAllow, SourceAny, "", false)}
	if d := c.TestWith(cand, nil, "10.0.0.1"); d.Action != ActionDeny {
		t.Fatalf("candidate: %+v", d)
	}
	if d := c.Evaluate(a.ID, "10.0.0.1"); d.Action != ActionAllow {
		t.Fatal("candidate leaked into the saved ACL")
	}
}

/* Conversion: the converted ACL lets in exactly the addresses the old access rule did */

// oldAllowed is the decision of the access rules of 2.17 and earlier.
func oldAllowed(old legacyAccessRule, ip, publicIP string) bool {
	cc := testCountries[ip]
	inIPs := func(m *map[string]string) bool {
		if m == nil {
			return false
		}
		if _, ok := (*m)[ip]; ok {
			return true
		}
		for k := range *m {
			if netutils.MatchIpWildcard(ip, k) || netutils.MatchIpCIDR(ip, k) {
				return true
			}
		}
		return false
	}
	inCC := func(m *map[string]string) bool {
		if m == nil {
			return false
		}
		_, ok := (*m)[strings.ToLower(cc)]
		if !ok {
			_, ok = (*m)[cc]
		}
		return ok && cc != ""
	}
	if old.BlacklistEnabled && (inCC(old.BlackListContryCode) || inIPs(old.BlackListIP)) {
		return false
	}
	if !old.WhitelistEnabled {
		return true
	}
	if inCC(old.WhiteListCountryCode) || inIPs(old.WhiteListIP) {
		return true
	}
	if old.WhitelistAllowLocalAndLoopback {
		p := net.ParseIP(ip)
		if ip == publicIP || ip == "127.0.0.1" || ip == "::1" {
			return true
		}
		for _, cidr := range privateRanges {
			_, n, _ := net.ParseCIDR(cidr)
			if n.Contains(p) {
				return true
			}
		}
	}
	return false
}

func TestConversionMatchesOldAccessRules(t *testing.T) {
	m := func(kv ...string) *map[string]string {
		out := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			out[kv[i]] = kv[i+1]
		}
		return &out
	}
	rules := []legacyAccessRule{
		{ID: "default", Name: "Default"}, // restricts nothing: Allow all
		{ID: "r-block", Name: "Block", BlacklistEnabled: true, BlackListIP: m("10.0.0.5", "bad", "192.168.7.*", "lab", "172.20.0.0/16", "net"), BlackListContryCode: m("cn", "")},
		{ID: "r-allow", Name: "Allow", WhitelistEnabled: true, WhiteListIP: m("10.0.0.0/24", "office"), WhiteListCountryCode: m("au", "")},
		{ID: "r-local", Name: "Local", WhitelistEnabled: true, WhitelistAllowLocalAndLoopback: true, TrustProxyHeadersOnly: true},
		{ID: "r-both", Name: "Both", BlacklistEnabled: true, WhitelistEnabled: true, BlackListIP: m("10.0.0.7", ""), WhiteListIP: m("10.0.0.0/24", ""), WhitelistAllowLocalAndLoopback: true},
		{ID: "r-emptyallow", Name: "Nobody", WhitelistEnabled: true},
		{ID: "r-off", Name: "Off", BlacklistEnabled: false, BlackListIP: m("10.0.0.5", "")},
	}
	c := newACLController(t)
	legacy := filepath.Join(t.TempDir(), "access")
	os.MkdirAll(legacy, 0775)
	for _, old := range rules {
		b, _ := json.Marshal(old)
		os.WriteFile(filepath.Join(legacy, old.ID+".json"), b, 0644)
	}
	if err := c.convertLegacy(legacy); err != nil {
		t.Fatal(err)
	}
	if err := c.load(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("old access rule folder not removed")
	}
	conv := c.Converted()
	if conv.IDMap["default"] != AllowAllID || !conv.Trust["r-local"] || conv.Trust["r-block"] {
		t.Fatalf("conversion record: %+v", conv)
	}
	addrs := []string{"10.0.0.5", "10.0.0.6", "10.0.0.7", "192.168.7.9", "192.168.8.1", "172.20.3.4", "172.21.0.1",
		"8.8.8.8", "1.1.1.1", "9.9.9.9", "5.5.5.5", "203.0.113.7", "127.0.0.1", "::1", "fe80::1", "2001:db8::1"}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 200; i++ {
		addrs = append(addrs, fmt.Sprintf("%d.%d.%d.%d", rng.Intn(256), rng.Intn(256), rng.Intn(256), rng.Intn(256)))
	}
	for _, old := range rules {
		id := old.ID
		if n, ok := conv.IDMap[id]; ok {
			id = n
		}
		for _, ip := range addrs {
			want := oldAllowed(old, ip, c.ServerPublicIP)
			d := c.Evaluate(id, ip)
			if (d.Action == ActionAllow) != want {
				t.Errorf("%s / %s: new %s (rule %d), old allowed=%v", old.Name, ip, d.Action, d.Rule, want)
			}
		}
	}
}

func saveObject(t *testing.T, c *Controller, o *Object) *Object {
	t.Helper()
	p, err := c.PrepareObject(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SaveObject(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNestedObjects(t *testing.T) {
	c := newACLController(t)
	inner := saveObject(t, c, &Object{Name: "Inner", Entries: []Entry{{Value: "100.70.0.0/16"}, {Value: "DE"}}})
	middle := saveObject(t, c, &Object{Name: "Middle", Entries: []Entry{{Value: "192.0.2.9"}, {Type: SourceObject, Value: inner.ID}}})
	outer := saveObject(t, c, &Object{Name: "Outer", Entries: []Entry{{Type: SourceObject, Value: middle.ID}, {Type: SourceObject, Value: PrivateNetworksID}}})
	a, _ := c.CreateACL("Nested", "")
	saveRules(t, c, a, r(ActionAllow, SourceObject, outer.ID, false))
	for ip, want := range map[string]string{
		"100.70.2.3":  ActionAllow, // two levels down
		"192.0.2.9":   ActionAllow, // one level down
		"5.5.5.5":     ActionAllow, // country, two levels down
		"192.168.1.1": ActionAllow, // nested built-in Private networks
		"203.0.113.7": ActionAllow, // server's public address through Private networks
		"8.8.8.8":     ActionDeny,
	} {
		if d := c.Evaluate(a.ID, ip); d.Action != want {
			t.Errorf("%s: %+v, want %s", ip, d, want)
		}
	}
	if got := c.ACLsUsingCountries(); len(got) != 1 || got[0] != "Nested" {
		t.Errorf("ACLsUsingCountries = %v", got)
	}
	if !c.ObjectUsesCountries(outer.ID) || c.ObjectUsesCountries(PrivateNetworksID) {
		t.Error("ObjectUsesCountries does not follow nesting")
	}
	if p := c.ObjectParents(inner.ID); len(p) != 1 || p[0] != "Middle" {
		t.Errorf("ObjectParents = %v", p)
	}

	// Loops, self and missing objects are refused
	loop := *inner
	loop.Entries = append(append([]Entry{}, inner.Entries...), Entry{Type: SourceObject, Value: outer.ID})
	if _, err := c.PrepareObject(&loop); err == nil || !strings.Contains(err.Error(), "already contains") {
		t.Errorf("loop: %v", err)
	}
	self := *inner
	self.Entries = []Entry{{Type: SourceObject, Value: inner.ID}}
	if _, err := c.PrepareObject(&self); err == nil {
		t.Error("self reference accepted")
	}
	if _, err := c.PrepareObject(&Object{Name: "Bad", Entries: []Entry{{Type: SourceObject, Value: "nope"}}}); err == nil {
		t.Error("missing nested object accepted")
	}

	// An object inside another cannot be deleted
	if err := c.DeleteObject(inner.ID); err == nil || !strings.Contains(err.Error(), "inside Middle") {
		t.Errorf("delete nested: %v", err)
	}

	// Changing the inner object changes the ACL
	in2, _ := c.GetObject(inner.ID)
	in2.Entries = []Entry{{Value: "100.80.0.0/16"}}
	saveObject(t, c, in2)
	if d := c.Evaluate(a.ID, "100.70.2.3"); d.Action != ActionDeny {
		t.Errorf("after inner change: %+v", d)
	}
	if d := c.Evaluate(a.ID, "100.80.1.1"); d.Action != ActionAllow {
		t.Errorf("after inner change: %+v", d)
	}

	// A loop that got in anyway (hand-edited file) does not hang compiling
	c.mu.Lock()
	c.objects[inner.ID].Entries = append(c.objects[inner.ID].Entries, Entry{Type: SourceObject, Value: outer.ID})
	c.compileLocked()
	c.mu.Unlock()
	if d := c.Evaluate(a.ID, "100.80.1.1"); d.Action != ActionAllow {
		t.Errorf("with loop: %+v", d)
	}

	// TestWith sees a nested change
	cand, _ := c.GetObject(inner.ID)
	cand.Entries = nil
	if d := c.TestWith(nil, map[string]*Object{inner.ID: cand}, "100.80.1.1"); d.Action == ActionAllow && d.ACLID != AllowAllID {
		t.Errorf("TestWith: %+v", d)
	}
	acl, _ := c.GetACL(a.ID)
	if d := c.TestWith(acl, map[string]*Object{inner.ID: cand}, "100.80.1.1"); d.Action != ActionDeny {
		t.Errorf("TestWith with nested change: %+v", d)
	}
}
