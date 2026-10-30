package access

/*
	Access control lists: ordered rules checked top down; the first rule
	whose source matches the client's address allows, denies or drops, and
	an address no rule matches is denied. Objects are named lists of source
	values. Built in: the "Allow all" ACL and the "Private networks" object.
*/

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

const (
	AllowAllID        = "allow-all"
	PrivateNetworksID = "private-networks"
	// LegacyDefaultID is the ID of the old default access rule, resolved to
	// Allow all unless an ACL with this ID exists.
	LegacyDefaultID = "default"

	MaxRules   = 500
	MaxEntries = 5000
)

// Source types
const (
	SourceAny      = "any"
	SourceObject   = "object"
	SourceIP       = "ip"
	SourceCIDR     = "cidr"
	SourceRange    = "range"
	SourceWildcard = "wildcard"
	SourceCountry  = "country"
)

// Actions
const (
	ActionAllow = "allow"
	ActionDeny  = "deny" // HTTP: 403 page; TCP: connection closed; UDP: packet ignored
	ActionDrop  = "drop" // HTTP: connection closed or stream reset; TCP: reset; UDP: packet ignored
)

type Source struct {
	Type  string `json:"type"`
	Value string `json:"value,omitempty"` // object ID, or the address / CIDR / range / wildcard / country code
	Not   bool   `json:"not,omitempty"`
}

type Rule struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Source  Source `json:"source"`
	Action  string `json:"action"`
	Comment string `json:"comment,omitempty"`
}

type ACL struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Rules       []Rule `json:"rules"`
	Revision    int64  `json:"revision"`
	BuiltIn     bool   `json:"builtIn,omitempty"`
}

// Entry is one value of an object.
type Entry struct {
	Type    string `json:"type"` // ip, cidr, range, wildcard, country or object
	Value   string `json:"value"`
	Comment string `json:"comment,omitempty"`
}

type Object struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Entries     []Entry `json:"entries"`
	Revision    int64   `json:"revision"`
	BuiltIn     bool    `json:"builtIn,omitempty"`
}

var (
	idPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	countryPattern  = regexp.MustCompile(`^[A-Z]{2}$`)
	wildcardPattern = regexp.MustCompile(`^(\d{1,3}|\*)\.(\d{1,3}|\*)\.(\d{1,3}|\*)\.(\d{1,3}|\*)$`)
)

// ValidID reports whether id can name an ACL, rule or object.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// DetectType returns the type of a value: country, cidr, wildcard, range or ip.
func DetectType(value string) string {
	v := strings.TrimSpace(value)
	switch {
	case countryPattern.MatchString(strings.ToUpper(v)) && len(v) == 2:
		return SourceCountry
	case strings.Contains(v, "/"):
		return SourceCIDR
	case strings.Contains(v, "*"):
		return SourceWildcard
	case strings.Contains(v, "-"):
		return SourceRange
	default:
		return SourceIP
	}
}

// NormalizeValue checks a value of the given type and returns it in its
// canonical form.
func NormalizeValue(typ, value string) (string, error) {
	v := strings.TrimSpace(value)
	switch typ {
	case SourceIP:
		a, err := netip.ParseAddr(v)
		if err != nil {
			return "", fmt.Errorf("%q is not an IP address", v)
		}
		return a.Unmap().WithZone("").String(), nil
	case SourceCIDR:
		p, err := netip.ParsePrefix(v)
		if err != nil {
			return "", fmt.Errorf("%q is not a CIDR (e.g. 10.0.0.0/8)", v)
		}
		if p.Addr().Is4In6() {
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		return p.Masked().String(), nil
	case SourceRange:
		a, b, ok := strings.Cut(v, "-")
		if !ok {
			return "", fmt.Errorf("%q is not a range (e.g. 10.0.0.10-10.0.0.50)", v)
		}
		lo, err1 := netip.ParseAddr(strings.TrimSpace(a))
		hi, err2 := netip.ParseAddr(strings.TrimSpace(b))
		if err1 != nil || err2 != nil {
			return "", fmt.Errorf("%q is not a range of two IP addresses", v)
		}
		lo, hi = lo.Unmap(), hi.Unmap()
		if lo.Is4() != hi.Is4() {
			return "", fmt.Errorf("range %q mixes IPv4 and IPv6", v)
		}
		if hi.Less(lo) {
			return "", fmt.Errorf("range %q ends before it starts", v)
		}
		return lo.String() + "-" + hi.String(), nil
	case SourceWildcard:
		m := wildcardPattern.FindStringSubmatch(v)
		if m == nil {
			return "", fmt.Errorf("%q is not an IPv4 wildcard (e.g. 192.168.*.*)", v)
		}
		for _, o := range m[1:] {
			if o != "*" {
				var n int
				fmt.Sscan(o, &n)
				if n > 255 {
					return "", fmt.Errorf("%q is not an IPv4 wildcard", v)
				}
			}
		}
		return v, nil
	case SourceObject:
		if !ValidID(v) {
			return "", fmt.Errorf("%q is not an object", v)
		}
		return v, nil
	case SourceCountry:
		c := strings.ToUpper(v)
		if !countryPattern.MatchString(c) {
			return "", fmt.Errorf("%q is not a two-letter country code", v)
		}
		return c, nil
	}
	return "", fmt.Errorf("unknown type %q", typ)
}

// normalizeEntries checks an object's entries.
func normalizeEntries(entries []Entry) ([]Entry, error) {
	if len(entries) > MaxEntries {
		return nil, fmt.Errorf("an object can have at most %d entries", MaxEntries)
	}
	out := make([]Entry, 0, len(entries))
	seen := map[string]bool{}
	for i, e := range entries {
		typ := e.Type
		if typ == "" {
			typ = DetectType(e.Value)
		}
		v, err := NormalizeValue(typ, e.Value)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %v", i+1, err)
		}
		if seen[typ+":"+v] {
			continue
		}
		seen[typ+":"+v] = true
		out = append(out, Entry{Type: typ, Value: v, Comment: strings.TrimSpace(e.Comment)})
	}
	return out, nil
}

// normalizeRules checks an ACL's rules; objectExists says whether an
// object ID can be used.
func normalizeRules(rules []Rule, objectExists func(string) bool, newID func() string) ([]Rule, error) {
	if len(rules) > MaxRules {
		return nil, fmt.Errorf("an ACL can have at most %d rules", MaxRules)
	}
	out := make([]Rule, 0, len(rules))
	ids := map[string]bool{}
	for i, r := range rules {
		label := fmt.Sprintf("rule %d", i+1)
		if r.ID == "" || !ValidID(r.ID) || ids[r.ID] {
			r.ID = newID()
		}
		ids[r.ID] = true
		switch r.Action {
		case ActionAllow, ActionDeny, ActionDrop:
		default:
			return nil, fmt.Errorf("%s: action must be allow, deny or drop", label)
		}
		s := r.Source
		switch s.Type {
		case SourceAny:
			if s.Not {
				return nil, fmt.Errorf("%s: \"not any\" would never match", label)
			}
			s.Value = ""
		case SourceObject:
			if !objectExists(s.Value) {
				return nil, fmt.Errorf("%s: object %q does not exist", label, s.Value)
			}
		case SourceIP, SourceCIDR, SourceRange, SourceWildcard, SourceCountry:
			v, err := NormalizeValue(s.Type, s.Value)
			if err != nil {
				return nil, fmt.Errorf("%s: %v", label, err)
			}
			s.Value = v
		default:
			return nil, fmt.Errorf("%s: unknown source type %q", label, s.Type)
		}
		r.Source = s
		r.Comment = strings.TrimSpace(r.Comment)
		if len(r.Comment) > 200 {
			return nil, fmt.Errorf("%s: comment is longer than 200 characters", label)
		}
		out = append(out, r)
	}
	return out, nil
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("name is empty")
	}
	if len(name) > 64 {
		return "", errors.New("name is longer than 64 characters")
	}
	return name, nil
}

// builtInAllowAll is the read-only "Allow all" ACL.
func builtInAllowAll() *ACL {
	return &ACL{
		ID: AllowAllID, Name: "Allow all", BuiltIn: true,
		Description: "Every address is allowed. Used by proxy rules without an ACL of their own; it cannot be changed.",
		Rules:       []Rule{{ID: "any", Enabled: true, Source: Source{Type: SourceAny}, Action: ActionAllow}},
	}
}

// privateRanges are the ranges of the "Private networks" object.
var privateRanges = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "127.0.0.0/8",
	"::1/128", "fc00::/7", "fe80::/10",
}

func builtInPrivateNetworks() *Object {
	o := &Object{ID: PrivateNetworksID, Name: "Private networks", BuiltIn: true,
		Description: "Private (RFC 1918, IPv6 ULA), loopback and link-local addresses, and this server's own public address. It cannot be changed."}
	for _, c := range privateRanges {
		o.Entries = append(o.Entries, Entry{Type: SourceCIDR, Value: c})
	}
	return o
}
