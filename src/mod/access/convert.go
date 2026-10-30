package access

/*
	Converts the access rules of 2.17 and earlier (conf/access/*.json) into
	ACLs with the same IDs. Each list becomes an object, and the rules are:
	deny the blocklist; allow Private networks (if LAN was allowed); allow
	the allowlist, or allow any without an allowlist. A permissive "default"
	rule becomes Allow all. The old files are removed.
*/

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type legacyAccessRule struct {
	ID                             string
	Name                           string
	Desc                           string
	BlacklistEnabled               bool
	WhitelistEnabled               bool
	WhitelistAllowLocalAndLoopback bool
	TrustProxyHeadersOnly          bool
	WhiteListCountryCode           *map[string]string
	WhiteListIP                    *map[string]string
	BlackListContryCode            *map[string]string
	BlackListIP                    *map[string]string
}

// LegacyConversion describes a conversion of old access rules.
type LegacyConversion struct {
	IDMap map[string]string // old access rule ID -> ACL ID, when different
	Trust map[string]bool   // old access rule ID -> it trusted forwarded headers
}

// Converted returns the conversion done at startup, or nil.
func (c *Controller) Converted() *LegacyConversion { return c.converted }

func legacyEntries(ips, countries *map[string]string) []Entry {
	out := []Entry{}
	keys := func(m *map[string]string) []string {
		ks := []string{}
		if m != nil {
			for k := range *m {
				ks = append(ks, k)
			}
		}
		sort.Strings(ks)
		return ks
	}
	for _, k := range keys(ips) {
		v := strings.TrimSpace(k)
		typ := SourceIP
		switch {
		case strings.Contains(v, "/"):
			typ = SourceCIDR
		case strings.Contains(v, "*"):
			typ = SourceWildcard
		}
		out = append(out, Entry{Type: typ, Value: v, Comment: (*ips)[k]})
	}
	for _, k := range keys(countries) {
		out = append(out, Entry{Type: SourceCountry, Value: strings.ToUpper(strings.TrimSpace(k)), Comment: (*countries)[k]})
	}
	return out
}

func (c *Controller) convertLegacy(folder string) error {
	files, _ := filepath.Glob(filepath.Join(folder, "*.json"))
	if len(files) == 0 {
		return nil
	}
	conv := &LegacyConversion{IDMap: map[string]string{}, Trust: map[string]bool{}}
	acls := []*ACL{}
	objects := map[string]*Object{}
	names := map[string]bool{}
	uniqueName := func(n string) string {
		base, i := n, 2
		for names[strings.ToLower(n)] {
			n = fmt.Sprintf("%s (%d)", base, i)
			i++
		}
		names[strings.ToLower(n)] = true
		return n
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var old legacyAccessRule
		if err := json.Unmarshal(b, &old); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		if old.ID == "" {
			old.ID = strings.TrimSuffix(filepath.Base(f), ".json")
		}
		if !ValidID(old.ID) {
			continue
		}
		conv.Trust[old.ID] = old.TrustProxyHeadersOnly
		block := legacyEntries(old.BlackListIP, old.BlackListContryCode)
		allow := legacyEntries(old.WhiteListIP, old.WhiteListCountryCode)
		blockOn := old.BlacklistEnabled && len(block) > 0
		if old.ID == LegacyDefaultID && !blockOn && !old.WhitelistEnabled {
			conv.IDMap[old.ID] = AllowAllID
			continue
		}
		name := strings.TrimSpace(old.Name)
		if name == "" {
			name = old.ID
		}
		if strings.EqualFold(name, "allow all") {
			name += " (converted)"
		}
		a := &ACL{ID: old.ID, Name: uniqueName(name), Description: strings.TrimSpace(old.Desc), Revision: 1, Rules: []Rule{}}
		addObject := func(suffix string, raw []Entry) (string, error) {
			entries := []Entry{}
			for _, e := range raw {
				if n, err := normalizeEntries([]Entry{e}); err == nil {
					entries = append(entries, n...)
				} else {
					c.Options.Logger.PrintAndLog("access", "Access rule "+a.Name+" "+suffix+": skipped "+e.Value, err)
				}
			}
			o := &Object{ID: old.ID + "-" + suffix, Name: uniqueName(a.Name + " " + suffix), Entries: entries, Revision: 1,
				Description: "Converted from the " + suffix + " of the access rule " + a.Name}
			objects[o.ID] = o
			return o.ID, nil
		}
		if blockOn {
			id, err := addObject("blocklist", block)
			if err != nil {
				return err
			}
			a.Rules = append(a.Rules, Rule{ID: "blocklist", Enabled: true, Source: Source{Type: SourceObject, Value: id}, Action: ActionDeny, Comment: "Converted blocklist"})
		}
		if old.WhitelistEnabled {
			if old.WhitelistAllowLocalAndLoopback {
				a.Rules = append(a.Rules, Rule{ID: "private", Enabled: true, Source: Source{Type: SourceObject, Value: PrivateNetworksID}, Action: ActionAllow, Comment: "Converted \"allow LAN and loopback\""})
			}
			if len(allow) > 0 {
				id, err := addObject("allowlist", allow)
				if err != nil {
					return err
				}
				a.Rules = append(a.Rules, Rule{ID: "allowlist", Enabled: true, Source: Source{Type: SourceObject, Value: id}, Action: ActionAllow, Comment: "Converted allowlist"})
			}
		} else {
			a.Rules = append(a.Rules, Rule{ID: "any", Enabled: true, Source: Source{Type: SourceAny}, Action: ActionAllow, Comment: "No allowlist: everything else is allowed"})
		}
		acls = append(acls, a)
	}

	// Writes the new files, keeping existing objects
	c.mu.Lock()
	c.objects = map[string]*Object{}
	if b, err := os.ReadFile(filepath.Join(c.Options.ConfigFolder, objectsFile)); err == nil {
		var list []*Object
		if json.Unmarshal(b, &list) == nil {
			for _, o := range list {
				if o != nil && ValidID(o.ID) {
					c.objects[o.ID] = o
				}
			}
		}
	}
	for id, o := range objects {
		c.objects[id] = o
	}
	err := c.saveObjectsLocked()
	if err == nil {
		for _, a := range acls {
			if err = c.saveACLLocked(a); err != nil {
				break
			}
		}
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	for _, f := range files {
		os.Remove(f)
	}
	os.Remove(folder) // removed only if empty
	c.converted = conv
	c.Options.Logger.PrintAndLog("access", fmt.Sprintf("Converted %d access rule(s) to ACLs in %s", len(files), c.Options.ConfigFolder), nil)
	return nil
}
