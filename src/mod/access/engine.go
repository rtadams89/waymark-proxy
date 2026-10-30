package access

/*
	Evaluation of ACLs. ACLs are compiled when they or their objects change.
	The country of an address is looked up at most once, when a reached rule
	needs it. An address without a country matches "not <country>". When the
	country cannot be known, rules that need it are skipped.
*/

import (
	"net/netip"
	"strings"
	"sync/atomic"

	"imuslab.com/zoraxy/mod/netutils"
)

type hitCounter struct{ n atomic.Int64 }

// matcher is a compiled source value list (an object or a rule's value).
type matcher struct {
	prefixes  []netip.Prefix
	ranges    [][2]netip.Addr
	wildcards []string
	countries map[string]bool
	publicIP  bool
}

func (m *matcher) needsCountry() bool { return len(m.countries) > 0 }

func (m *matcher) add(typ, value string) {
	switch typ {
	case SourceIP:
		if a, err := netip.ParseAddr(value); err == nil {
			m.prefixes = append(m.prefixes, netip.PrefixFrom(a, a.BitLen()))
		}
	case SourceCIDR:
		if p, err := netip.ParsePrefix(value); err == nil {
			m.prefixes = append(m.prefixes, p.Masked())
		}
	case SourceRange:
		a, b, _ := strings.Cut(value, "-")
		lo, err1 := netip.ParseAddr(a)
		hi, err2 := netip.ParseAddr(b)
		if err1 == nil && err2 == nil {
			m.ranges = append(m.ranges, [2]netip.Addr{lo, hi})
		}
	case SourceWildcard:
		m.wildcards = append(m.wildcards, value)
	case SourceCountry:
		if m.countries == nil {
			m.countries = map[string]bool{}
		}
		m.countries[strings.ToUpper(value)] = true
	}
}

// evalCtx is one address being evaluated.
type evalCtx struct {
	ip       string
	addr     netip.Addr
	valid    bool
	c        *Controller
	country  string
	looked   bool
	known    bool // the country lookup succeeded
	publicIP string
}

func (e *evalCtx) lookupCountry() (string, bool) {
	if !e.looked {
		e.looked = true
		if e.valid && e.c != nil && e.c.countryLookup != nil {
			e.country, e.known = e.c.countryLookup(e.ip)
			e.country = strings.ToUpper(e.country)
		} else if e.valid && e.c != nil && e.c.Options != nil && e.c.Options.GeoDB != nil && e.c.Options.GeoDB.Enabled() {
			if info, err := e.c.Options.GeoDB.ResolveCountryCodeFromIP(e.ip); err == nil && info != nil {
				e.country, e.known = strings.ToUpper(info.CountryIsoCode), true
			}
		}
	}
	return e.country, e.known
}

// match reports whether the address is in m; known is false when only a
// country lookup could decide and the country is unknown.
func (m *matcher) match(e *evalCtx) (matched bool, known bool) {
	if e.valid {
		for _, p := range m.prefixes {
			if p.Contains(e.addr) {
				return true, true
			}
		}
		for _, r := range m.ranges {
			if r[0].Is4() == e.addr.Is4() && !e.addr.Less(r[0]) && !r[1].Less(e.addr) {
				return true, true
			}
		}
		for _, w := range m.wildcards {
			if netutils.MatchIpWildcard(e.ip, w) {
				return true, true
			}
		}
		if m.publicIP && e.publicIP != "" && e.ip == e.publicIP {
			return true, true
		}
	}
	if len(m.countries) > 0 {
		if !e.valid {
			return false, true
		}
		cc, ok := e.lookupCountry()
		if !ok {
			return false, false
		}
		return cc != "" && m.countries[cc], true
	}
	return false, true
}

type compiledRule struct {
	rule  Rule
	any   bool
	m     *matcher // nil for any
	label string   // for logs: "#3" plus the comment
	hits  *hitCounter
}

type compiledACL struct {
	acl         *ACL
	rules       []*compiledRule
	defaultHits *hitCounter
}

// Decision is the outcome of evaluating an ACL for an address.
type Decision struct {
	Action  string `json:"action"`
	ACLID   string `json:"aclId"`
	ACLName string `json:"aclName"`
	Rule    int    `json:"rule"` // 1-based position of the rule that matched; 0 for the default deny
	RuleID  string `json:"ruleId,omitempty"`
	Comment string `json:"comment,omitempty"`
	Country string `json:"country,omitempty"`
	Skipped []int  `json:"skipped,omitempty"` // rules skipped because the country is unknown
}

// RuleLabel names the rule for logs: "ACL name #3" or "ACL name default deny".
func (d Decision) RuleLabel() string {
	name := strings.NewReplacer("[", "(", "]", ")").Replace(d.ACLName)
	if d.Rule == 0 {
		return name + " default deny"
	}
	return name + " #" + itoa(d.Rule)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// compileLocked builds the compiled form of every ACL. Call with c.mu held.
func (c *Controller) compileLocked() {
	objs := c.objectsWithBuiltIns()
	objMatchers := map[string]*matcher{}
	// Flattens each object with its nested objects into one matcher; a
	// missing object matches nothing
	var walk func(id string, m *matcher, seen map[string]bool)
	walk = func(id string, m *matcher, seen map[string]bool) {
		o := objs[id]
		if o == nil || seen[id] {
			return
		}
		seen[id] = true
		for _, e := range o.Entries {
			if e.Type == SourceObject {
				walk(e.Value, m, seen)
			} else {
				m.add(e.Type, e.Value)
			}
		}
		if id == PrivateNetworksID {
			m.publicIP = true
		}
	}
	for id := range objs {
		m := &matcher{}
		walk(id, m, map[string]bool{})
		objMatchers[id] = m
	}
	compiled := map[string]*compiledACL{}
	all := map[string]*ACL{AllowAllID: builtInAllowAll()}
	for id, a := range c.acls {
		all[id] = a
	}
	for id, a := range all {
		ca := &compiledACL{acl: a, defaultHits: c.counter(id, "")}
		for i, r := range a.Rules {
			cr := &compiledRule{rule: r, hits: c.counter(id, r.ID), label: "#" + itoa(i+1)}
			switch r.Source.Type {
			case SourceAny:
				cr.any = true
			case SourceObject:
				cr.m = objMatchers[r.Source.Value]
				if cr.m == nil {
					cr.m = &matcher{}
				}
			default:
				cr.m = &matcher{}
				cr.m.add(r.Source.Type, r.Source.Value)
			}
			ca.rules = append(ca.rules, cr)
		}
		compiled[id] = ca
	}
	c.compiled = compiled
}

func (c *Controller) counter(aclID, ruleID string) *hitCounter {
	if c.hits == nil {
		c.hits = map[string]*hitCounter{}
	}
	k := aclID + "/" + ruleID
	h := c.hits[k]
	if h == nil {
		h = &hitCounter{}
		c.hits[k] = h
	}
	return h
}

// resolveIDLocked returns the ACL to use for an ID stored on a proxy rule:
// Allow all for an empty or unknown ID.
func (c *Controller) resolveIDLocked(id string) string {
	if id == "" {
		return AllowAllID
	}
	if _, ok := c.compiled[id]; ok {
		return id
	}
	return AllowAllID
}

// Resolve returns the ACL used for an ID stored on a proxy rule.
func (c *Controller) Resolve(id string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.resolveIDLocked(id)
}

// ACLExists reports whether id names an ACL (built-in included).
func (c *Controller) ACLExists(id string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.compiled[id]
	return ok
}

// Evaluate decides what happens to a connection from ip under the ACL
// aclID and counts the matching rule.
func (c *Controller) Evaluate(aclID, ip string) Decision {
	return c.evaluate(aclID, ip, true)
}

// Test is Evaluate without counting.
func (c *Controller) Test(aclID, ip string) Decision {
	return c.evaluate(aclID, ip, false)
}

func (c *Controller) evaluate(aclID, ip string, count bool) Decision {
	if c == nil {
		return Decision{Action: ActionAllow, ACLID: AllowAllID, ACLName: "Allow all", Rule: 1}
	}
	c.mu.RLock()
	id := c.resolveIDLocked(aclID)
	ca := c.compiled[id]
	c.mu.RUnlock()
	if ca == nil {
		return Decision{Action: ActionAllow, ACLID: AllowAllID, ACLName: "Allow all", Rule: 1}
	}
	return evaluateCompiled(ca, c.newEvalCtx(ip), count)
}

func (c *Controller) newEvalCtx(ip string) *evalCtx {
	e := &evalCtx{ip: strings.TrimSpace(ip), c: c, publicIP: c.ServerPublicIP}
	if a, err := netip.ParseAddr(e.ip); err == nil {
		e.addr, e.valid = a.Unmap().WithZone(""), true
		e.ip = e.addr.String()
	}
	return e
}

func evaluateCompiled(ca *compiledACL, e *evalCtx, count bool) Decision {
	d := Decision{ACLID: ca.acl.ID, ACLName: ca.acl.Name}
	for i, cr := range ca.rules {
		if !cr.rule.Enabled {
			continue
		}
		fire := false
		if cr.any {
			fire = true
		} else {
			matched, known := cr.m.match(e)
			if !known {
				d.Skipped = append(d.Skipped, i+1)
				continue
			}
			fire = matched != cr.rule.Source.Not
		}
		if fire {
			if count {
				cr.hits.n.Add(1)
			}
			d.Action, d.Rule, d.RuleID, d.Comment = cr.rule.Action, i+1, cr.rule.ID, cr.rule.Comment
			d.Country = e.country
			return d
		}
	}
	if count {
		ca.defaultHits.n.Add(1)
	}
	d.Action = ActionDeny
	d.Country = e.country
	return d
}

// Hits returns the match counts of an ACL's rules (by rule ID) and of its
// default deny (key "").
func (c *Controller) Hits(aclID string) map[string]int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := map[string]int64{}
	ca := c.compiled[aclID]
	if ca == nil {
		return out
	}
	for _, cr := range ca.rules {
		out[cr.rule.ID] = cr.hits.n.Load()
	}
	out[""] = ca.defaultHits.n.Load()
	return out
}

// TestWith evaluates ip against an unsaved ACL and objects.
func (c *Controller) TestWith(acl *ACL, objects map[string]*Object, ip string) Decision {
	tmp := &Controller{Options: c.Options, ServerPublicIP: c.ServerPublicIP, acls: map[string]*ACL{}, objects: map[string]*Object{}, countryLookup: c.countryLookup}
	c.mu.RLock()
	for k, v := range c.acls {
		tmp.acls[k] = v
	}
	for k, v := range c.objects {
		tmp.objects[k] = v
	}
	c.mu.RUnlock()
	if acl != nil {
		tmp.acls[acl.ID] = acl
	}
	for k, v := range objects {
		tmp.objects[k] = v
	}
	tmp.compileLocked()
	id := AllowAllID
	if acl != nil {
		id = acl.ID
	}
	return evaluateCompiled(tmp.compiled[id], tmp.newEvalCtx(ip), false)
}
