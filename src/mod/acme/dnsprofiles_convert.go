package acme

/*
	dnsprofiles_convert.go

	Waymark keeps DNS challenge credentials only in credential sets
	(dnsprofiles.go). Upstream Zoraxy, and Waymark before 2.0.0, could also
	store them per certificate, under the "acme" table keys
	<certName>_dns_provider, <certName>_dns_credentials and
	<certName>_dns_servers.

	At startup, ConvertPerCertificateDNSCredentials turns any such
	credentials into credential sets, so certificates keep renewing:
	- a certificate that is already bound to a set just loses its old keys;
	- otherwise its credentials become a set (certificates with identical
	  credentials share one set, and an existing identical set is reused)
	  and the certificate is bound to it;
	- keys of certificates that are no longer installed, or that renew with
	  HTTP-01, are deleted.
	Set IDs are derived from the credentials, so both nodes of an HA pair
	produce the same sets before config sync runs.
*/

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var perCertDNSKeySuffixes = []string{"_dns_provider", "_dns_credentials", "_dns_servers"}

// ConvertPerCertificateDNSCredentials moves per-certificate DNS credentials
// into credential sets. It is safe to run on every start.
func (a *ACMEHandler) ConvertPerCertificateDNSCredentials() {
	a.ensureDNSProfileTables()
	entries, err := a.Database.ListTable("acme")
	if err != nil {
		return
	}
	certNames := map[string]bool{}
	for _, kv := range entries {
		if len(kv) < 1 {
			continue
		}
		key := string(kv[0])
		for _, suffix := range perCertDNSKeySuffixes {
			if strings.HasSuffix(key, suffix) {
				certNames[strings.TrimSuffix(key, suffix)] = true
			}
		}
	}
	if len(certNames) == 0 {
		return
	}
	names := make([]string, 0, len(certNames))
	for n := range certNames {
		names = append(names, n)
	}
	sort.Strings(names)

	// provider + canonical credentials -> set ID, for reuse
	existing := map[string]string{}
	usedNames := map[string]bool{}
	profiles, _ := a.listDNSProfiles()
	for _, p := range profiles {
		usedNames[strings.ToLower(p.Name)] = true
		if canon, ok := canonicalDNSCredentials(p.Provider, p.Credentials); ok {
			existing[p.Provider+"\n"+canon] = p.ID
		}
	}

	for _, certName := range names {
		removeKeys := true
		bound := a.Database.KeyExists("acme", certName+dnsProfileBindingSuffix)
		if !bound && isValidCertName(certName) && certFileExists(certName) && certRenewsWithDNS(certName) {
			var provider, credentials string
			if a.Database.Read("acme", certName+"_dns_provider", &provider) == nil &&
				a.Database.Read("acme", certName+"_dns_credentials", &credentials) == nil {
				id, err := a.convertedDNSProfileFor(certName, provider, credentials, existing, usedNames)
				if err != nil {
					a.Logf("Could not turn the per-certificate DNS credentials of "+certName+" into a credential set; they are kept until it gets one", err)
					removeKeys = false
				} else if err := a.Database.Write("acme", certName+dnsProfileBindingSuffix, id); err != nil {
					a.Logf("Could not assign a DNS credential set to "+certName, err)
					removeKeys = false
				}
			}
		}
		if removeKeys {
			for _, suffix := range perCertDNSKeySuffixes {
				a.Database.Delete("acme", certName+suffix)
			}
		}
	}
}

// convertedDNSProfileFor returns the ID of a credential set holding these
// credentials, creating one when needed.
func (a *ACMEHandler) convertedDNSProfileFor(certName, provider, credentials string, existing map[string]string, usedNames map[string]bool) (string, error) {
	canon, err := canonicalDNSCredentialsErr(provider, credentials)
	if err != nil {
		return "", err
	}
	key := provider + "\n" + canon
	if id, ok := existing[key]; ok {
		a.Logf("Certificate "+certName+" now uses the DNS credential set with the same credentials it had", nil)
		return id, nil
	}

	sum := sha256.Sum256([]byte(key))
	id := hex.EncodeToString(sum[:dnsProfileIDLengthInByte])
	name := certName
	if len(name) > maxDNSProfileNameLength {
		name = name[:maxDNSProfileNameLength]
	}
	for i := 2; usedNames[strings.ToLower(name)]; i++ {
		suffix := " (" + strconv.Itoa(i) + ")"
		base := certName
		if len(base)+len(suffix) > maxDNSProfileNameLength {
			base = base[:maxDNSProfileNameLength-len(suffix)]
		}
		name = base + suffix
	}
	now := time.Now().Unix()
	profile := &DNSProfile{ID: id, Name: name, Provider: provider, Credentials: canon, CreatedAt: now, UpdatedAt: now}
	if err := a.Database.Write(dnsProfileTable, id, profile); err != nil {
		return "", err
	}
	existing[key] = id
	usedNames[strings.ToLower(name)] = true
	a.Logf("Turned the per-certificate DNS credentials of "+certName+" into the credential set \""+name+"\"", nil)
	return id, nil
}

func canonicalDNSCredentialsErr(provider, credentials string) (string, error) {
	normalized, err := normalizeDNSCredentials(provider, credentials)
	if err != nil {
		return "", err
	}
	js, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(js), nil
}

func canonicalDNSCredentials(provider, credentials string) (string, bool) {
	c, err := canonicalDNSCredentialsErr(provider, credentials)
	return c, err == nil
}

func certRenewsWithDNS(certName string) bool {
	info, err := LoadCertInfoJSON(filepath.Join(acmeCertStoreDir, certName+".json"))
	return err == nil && info.UseDNS
}
