package acme

/*
	dnsprofiles.go

	Reusable DNS challenge credential sets ("DNS profiles").

	Upstream Zoraxy stores DNS challenge credentials per certificate, under
	the "acme" table keys <certName>_dns_provider and <certName>_dns_credentials.
	This file adds named credential sets that can be shared by any number of
	certificates. A certificate opts in by having a binding key
	<certName>_dns_profile that holds a profile ID. Certificates without a
	binding keep using their legacy per-certificate credentials, so existing
	installs behave exactly as before.

	Secrets are write-only from the web UI's point of view: the list API
	reports which fields have a value set, but never returns the values.
	When a profile is edited, a string field submitted empty keeps its
	stored value.
*/

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"imuslab.com/zoraxy/mod/acme/acmedns"
	"imuslab.com/zoraxy/mod/utils"
)

const (
	dnsProfileTable          = "acme_dns_profiles"
	dnsProfileBindingSuffix  = "_dns_profile"
	legacyDNSProviderSuffix  = "_dns_provider"
	legacyDNSCredSuffix      = "_dns_credentials"
	acmeCertStoreDir         = "./conf/certs"
	maxDNSProfileNameLength  = 64
	dnsProfileIDLengthInByte = 8
)

// DNSProfile is a named, reusable set of DNS challenge credentials.
type DNSProfile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`    // lego provider id, e.g. "cloudflare"
	Credentials string `json:"credentials"` // JSON object, same shape as legacy per-cert credentials
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

// dnsProfileSummary is what the UI is allowed to see about a profile.
type dnsProfileSummary struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Provider  string            `json:"provider"`
	FieldsSet []string          `json:"fieldsSet"` // string fields that have a stored (hidden) value
	Settings  map[string]any    `json:"settings"`  // non-secret values (bool / number fields) for pre-filling the edit form
	UsedBy    []string          `json:"usedBy"`    // certificate names bound to this profile
	Meta      map[string]string `json:"meta,omitempty"`
	CreatedAt int64             `json:"createdAt"`
	UpdatedAt int64             `json:"updatedAt"`
}

// dnsBindingInfo describes how a certificate gets its DNS credentials.
type dnsBindingInfo struct {
	ProfileID   string `json:"profileId,omitempty"`
	ProfileName string `json:"profileName,omitempty"`
	Missing     bool   `json:"missing,omitempty"` // bound to a profile that no longer exists
	Legacy      bool   `json:"legacy"`            // has upstream-style per-certificate credentials
	LegacyProv  string `json:"legacyProvider,omitempty"`
	UseDNS      bool   `json:"useDNS"`      // from the certificate's ACME info file
	AcmeManaged bool   `json:"acmeManaged"` // has an ACME info file, so Zoraxy can renew it
}

/*
	Storage helpers
*/

func (a *ACMEHandler) ensureDNSProfileTables() {
	if !a.Database.TableExists(dnsProfileTable) {
		a.Database.NewTable(dnsProfileTable)
	}
	if !a.Database.TableExists("acme") {
		a.Database.NewTable("acme")
	}
}

func newDNSProfileID() (string, error) {
	b := make([]byte, dnsProfileIDLengthInByte)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func isValidDNSProfileID(id string) bool {
	if len(id) != dnsProfileIDLengthInByte*2 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// isValidCertName blocks path traversal and key-collision tricks in names
// that are used both as database key prefixes and as filenames.
func isValidCertName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	return filepath.Base(name) == name
}

func (a *ACMEHandler) loadDNSProfile(id string) (*DNSProfile, error) {
	if !isValidDNSProfileID(id) {
		return nil, errors.New("invalid DNS credential set id")
	}
	if !a.Database.TableExists(dnsProfileTable) || !a.Database.KeyExists(dnsProfileTable, id) {
		return nil, errors.New("DNS credential set not found")
	}
	profile := DNSProfile{}
	if err := a.Database.Read(dnsProfileTable, id, &profile); err != nil {
		return nil, err
	}
	return &profile, nil
}

func (a *ACMEHandler) listDNSProfiles() ([]*DNSProfile, error) {
	a.ensureDNSProfileTables()
	entries, err := a.Database.ListTable(dnsProfileTable)
	if err != nil {
		return nil, err
	}
	profiles := []*DNSProfile{}
	for _, kv := range entries {
		if len(kv) < 2 {
			continue
		}
		profile := DNSProfile{}
		if err := json.Unmarshal(kv[1], &profile); err != nil {
			continue
		}
		profiles = append(profiles, &profile)
	}
	sort.Slice(profiles, func(i, j int) bool {
		return strings.ToLower(profiles[i].Name) < strings.ToLower(profiles[j].Name)
	})
	return profiles, nil
}

// certFileExists reports whether a certificate with this name is still installed.
func certFileExists(certName string) bool {
	return utils.FileExists(filepath.Join(acmeCertStoreDir, certName+".pem")) ||
		utils.FileExists(filepath.Join(acmeCertStoreDir, certName+".key"))
}

// listDNSProfileBindings returns certName -> profileID for installed certificates.
// Bindings left behind by deleted certificates are skipped.
func (a *ACMEHandler) listDNSProfileBindings() (map[string]string, error) {
	a.ensureDNSProfileTables()
	entries, err := a.Database.ListTable("acme")
	if err != nil {
		return nil, err
	}
	bindings := map[string]string{}
	for _, kv := range entries {
		if len(kv) < 2 {
			continue
		}
		key := string(kv[0])
		if !strings.HasSuffix(key, dnsProfileBindingSuffix) {
			continue
		}
		certName := strings.TrimSuffix(key, dnsProfileBindingSuffix)
		if !certFileExists(certName) {
			continue
		}
		var profileID string
		if err := json.Unmarshal(kv[1], &profileID); err != nil {
			continue
		}
		bindings[certName] = profileID
	}
	return bindings, nil
}

/*
	Credential validation and merging
*/

// normalizeDNSCredentials checks a credential JSON object against the
// provider's schema and returns it re-encoded. Unknown keys are rejected so
// a typo in a field name doesn't silently produce a broken credential set.
func normalizeDNSCredentials(provider string, credentials string) (map[string]any, error) {
	schema, err := acmedns.GetProviderConfigStructure(provider)
	if err != nil {
		return nil, fmt.Errorf("unsupported DNS provider %q", provider)
	}
	creds := map[string]any{}
	if strings.TrimSpace(credentials) != "" {
		if err := json.Unmarshal([]byte(credentials), &creds); err != nil {
			return nil, errors.New("credentials must be a JSON object")
		}
	}
	for key := range creds {
		if _, ok := schema[key]; !ok {
			return nil, fmt.Errorf("field %q is not valid for provider %s", key, provider)
		}
	}
	return creds, nil
}

// mergeDNSCredentials applies an edit on top of stored credentials. Empty
// string values in the update mean "keep the stored value", which is how
// the UI edits a profile without ever seeing its secrets.
func mergeDNSCredentials(stored map[string]any, update map[string]any) map[string]any {
	merged := map[string]any{}
	for k, v := range stored {
		merged[k] = v
	}
	for k, v := range update {
		if s, ok := v.(string); ok && s == "" {
			if _, exists := stored[k]; exists {
				continue
			}
		}
		merged[k] = v
	}
	return merged
}

// summarizeDNSCredentials splits stored credentials into the names of
// string-typed fields that hold a value (treated as secret, never returned)
// and values of non-string fields (bools, numbers, durations) that are safe
// to show so the edit form can be pre-filled. The provider schema decides
// the type; anything not in the schema is treated as secret.
func summarizeDNSCredentials(provider string, credentials string) ([]string, map[string]any) {
	fieldsSet := []string{}
	settings := map[string]any{}
	creds := map[string]any{}
	if err := json.Unmarshal([]byte(credentials), &creds); err != nil {
		return fieldsSet, settings
	}
	schema, _ := acmedns.GetProviderConfigStructure(provider)
	for k, v := range creds {
		datatype, known := schema[k]
		if !known || datatype == "string" {
			if s, ok := v.(string); !ok || s != "" {
				fieldsSet = append(fieldsSet, k)
			}
			continue
		}
		settings[k] = v
	}
	sort.Strings(fieldsSet)
	return fieldsSet, settings
}

/*
	Credential resolution used by ObtainCert (issue and renew)
*/

// resolveDNSCredentials returns the provider and credential JSON to use for
// a certificate's DNS-01 challenge. A bound credential set wins; otherwise
// the legacy per-certificate keys are used.
func (a *ACMEHandler) resolveDNSCredentials(certificateName string) (provider string, credentials string, source string, err error) {
	a.ensureDNSProfileTables()

	bindingKey := certificateName + dnsProfileBindingSuffix
	if a.Database.KeyExists("acme", bindingKey) {
		var profileID string
		if err := a.Database.Read("acme", bindingKey, &profileID); err != nil {
			return "", "", "", fmt.Errorf("reading DNS credential set binding failed: %w", err)
		}
		profile, err := a.loadDNSProfile(profileID)
		if err != nil {
			return "", "", "", fmt.Errorf("certificate %s uses DNS credential set %s, which could not be loaded: %w", certificateName, profileID, err)
		}
		return profile.Provider, profile.Credentials, "credential set \"" + profile.Name + "\"", nil
	}

	if !a.Database.KeyExists("acme", certificateName+legacyDNSProviderSuffix) || !a.Database.KeyExists("acme", certificateName+legacyDNSCredSuffix) {
		return "", "", "", errors.New("DNS Provider and DNS Credential configuration required for ACME Provider (Error -2)")
	}
	if err := a.Database.Read("acme", certificateName+legacyDNSCredSuffix, &credentials); err != nil {
		return "", "", "", fmt.Errorf("reading DNS credentials failed: %w", err)
	}
	if err := a.Database.Read("acme", certificateName+legacyDNSProviderSuffix, &provider); err != nil {
		return "", "", "", fmt.Errorf("reading DNS provider failed: %w", err)
	}
	return provider, credentials, "per-certificate credentials", nil
}

// setCertUseDNS flips the DNS challenge flag in a certificate's ACME info
// file, if it has one, so renewals use the challenge type that was picked.
func setCertUseDNS(certName string, useDNS bool) error {
	infoPath := filepath.Join(acmeCertStoreDir, certName+".json")
	if !utils.FileExists(infoPath) {
		return nil
	}
	info, err := LoadCertInfoJSON(infoPath)
	if err != nil {
		return err
	}
	if info.UseDNS == useDNS {
		return nil
	}
	info.UseDNS = useDNS
	js, err := json.Marshal(info)
	if err != nil {
		return err
	}
	st, err := os.Stat(infoPath)
	if err != nil {
		return err
	}
	return os.WriteFile(infoPath, js, st.Mode().Perm())
}

/*
	HTTP handlers, all under /api/acme/dnsProfiles/
*/

// HandleListDNSProfiles returns all credential sets, without secret values.
func (a *ACMEHandler) HandleListDNSProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := a.listDNSProfiles()
	if err != nil {
		utils.SendErrorResponse(w, "failed to load DNS credential sets")
		return
	}
	bindings, err := a.listDNSProfileBindings()
	if err != nil {
		bindings = map[string]string{}
	}

	results := []*dnsProfileSummary{}
	for _, p := range profiles {
		fieldsSet, settings := summarizeDNSCredentials(p.Provider, p.Credentials)
		usedBy := []string{}
		for certName, profileID := range bindings {
			if profileID == p.ID {
				usedBy = append(usedBy, certName)
			}
		}
		sort.Strings(usedBy)
		results = append(results, &dnsProfileSummary{
			ID:        p.ID,
			Name:      p.Name,
			Provider:  p.Provider,
			FieldsSet: fieldsSet,
			Settings:  settings,
			UsedBy:    usedBy,
			CreatedAt: p.CreatedAt,
			UpdatedAt: p.UpdatedAt,
		})
	}

	js, _ := json.Marshal(results)
	utils.SendJSONResponse(w, string(js))
}

// HandleSaveDNSProfile creates a credential set, or updates one when id is given.
func (a *ACMEHandler) HandleSaveDNSProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.ensureDNSProfileTables()

	name, err := utils.PostPara(r, "name")
	name = strings.TrimSpace(name)
	if err != nil || name == "" {
		utils.SendErrorResponse(w, "name is required")
		return
	}
	if len(name) > maxDNSProfileNameLength {
		utils.SendErrorResponse(w, fmt.Sprintf("name must be %d characters or fewer", maxDNSProfileNameLength))
		return
	}
	provider, err := utils.PostPara(r, "provider")
	if err != nil || provider == "" {
		utils.SendErrorResponse(w, "provider is required")
		return
	}
	credentialsRaw, _ := utils.PostPara(r, "credentials")
	update, err := normalizeDNSCredentials(provider, credentialsRaw)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	id, _ := utils.PostPara(r, "id")
	now := time.Now().Unix()
	var profile *DNSProfile
	if id != "" {
		profile, err = a.loadDNSProfile(id)
		if err != nil {
			utils.SendErrorResponse(w, err.Error())
			return
		}
		stored := map[string]any{}
		if profile.Provider == provider {
			json.Unmarshal([]byte(profile.Credentials), &stored)
		}
		// Switching provider discards the old fields entirely
		update = mergeDNSCredentials(stored, update)
	} else {
		id, err = newDNSProfileID()
		if err != nil {
			utils.SendErrorResponse(w, "failed to generate id")
			return
		}
		profile = &DNSProfile{ID: id, CreatedAt: now}
	}

	// Reject names that would make the dropdown ambiguous
	existing, _ := a.listDNSProfiles()
	for _, p := range existing {
		if p.ID != profile.ID && strings.EqualFold(p.Name, name) {
			utils.SendErrorResponse(w, "a credential set with this name already exists")
			return
		}
	}

	credsJSON, err := json.Marshal(update)
	if err != nil {
		utils.SendErrorResponse(w, "failed to encode credentials")
		return
	}

	// Build the provider once so obviously broken input fails here rather
	// than at the next renewal. This does not contact the DNS provider.
	if _, err := GetDnsChallengeProviderByName(provider, string(credsJSON), 300); err != nil {
		utils.SendErrorResponse(w, "credentials rejected by provider: "+err.Error())
		return
	}

	profile.Name = name
	profile.Provider = provider
	profile.Credentials = string(credsJSON)
	profile.UpdatedAt = now
	if err := a.Database.Write(dnsProfileTable, profile.ID, profile); err != nil {
		utils.SendErrorResponse(w, "failed to save credential set")
		return
	}
	a.Logf("DNS credential set \""+profile.Name+"\" ("+profile.Provider+") saved", nil)

	js, _ := json.Marshal(map[string]string{"id": profile.ID})
	utils.SendJSONResponse(w, string(js))
}

// HandleDeleteDNSProfile removes a credential set that no certificate uses.
func (a *ACMEHandler) HandleDeleteDNSProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := utils.PostPara(r, "id")
	if err != nil {
		utils.SendErrorResponse(w, "id is required")
		return
	}
	profile, err := a.loadDNSProfile(id)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	bindings, err := a.listDNSProfileBindings()
	if err != nil {
		utils.SendErrorResponse(w, "failed to check which certificates use this credential set")
		return
	}
	inUse := []string{}
	for certName, profileID := range bindings {
		if profileID == id {
			inUse = append(inUse, certName)
		}
	}
	if len(inUse) > 0 {
		sort.Strings(inUse)
		utils.SendErrorResponse(w, "credential set is in use by: "+strings.Join(inUse, ", ")+". Assign those certificates to another set first.")
		return
	}
	if err := a.Database.Delete(dnsProfileTable, id); err != nil {
		utils.SendErrorResponse(w, "failed to delete credential set")
		return
	}
	a.Logf("DNS credential set \""+profile.Name+"\" deleted", nil)
	utils.SendOK(w)
}

// HandleListDNSBindings reports, per installed certificate, where its DNS
// credentials come from.
func (a *ACMEHandler) HandleListDNSBindings(w http.ResponseWriter, r *http.Request) {
	a.ensureDNSProfileTables()
	profiles, _ := a.listDNSProfiles()
	names := map[string]string{}
	for _, p := range profiles {
		names[p.ID] = p.Name
	}
	bindings, _ := a.listDNSProfileBindings()

	results := map[string]*dnsBindingInfo{}
	files, err := os.ReadDir(acmeCertStoreDir)
	if err != nil {
		utils.SendJSONResponse(w, "{}")
		return
	}
	for _, f := range files {
		if f.IsDir() || filepath.Ext(f.Name()) != ".pem" {
			continue
		}
		certName := strings.TrimSuffix(f.Name(), ".pem")
		info := &dnsBindingInfo{}
		if profileID, ok := bindings[certName]; ok {
			info.ProfileID = profileID
			if n, ok := names[profileID]; ok {
				info.ProfileName = n
			} else {
				info.Missing = true
			}
		}
		if a.Database.KeyExists("acme", certName+legacyDNSProviderSuffix) && a.Database.KeyExists("acme", certName+legacyDNSCredSuffix) {
			info.Legacy = true
			var prov string
			if a.Database.Read("acme", certName+legacyDNSProviderSuffix, &prov) == nil {
				info.LegacyProv = prov
			}
		}
		if certInfo, err := LoadCertInfoJSON(filepath.Join(acmeCertStoreDir, certName+".json")); err == nil {
			info.AcmeManaged = true
			info.UseDNS = certInfo.UseDNS
		}
		results[certName] = info
	}
	js, _ := json.Marshal(results)
	utils.SendJSONResponse(w, string(js))
}

// HandleAssignDNSProfile sets how a certificate answers ACME challenges.
// profileId = <id>   use that credential set for DNS-01
// profileId = legacy keep / revert to the per-certificate credentials for DNS-01
// profileId = http   use HTTP-01 (no DNS challenge)
func (a *ACMEHandler) HandleAssignDNSProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.ensureDNSProfileTables()

	certName, err := utils.PostPara(r, "filename")
	if err != nil || !isValidCertName(certName) {
		utils.SendErrorResponse(w, "invalid certificate name")
		return
	}
	choice, err := utils.PostPara(r, "profileId")
	if err != nil || choice == "" {
		utils.SendErrorResponse(w, "profileId is required")
		return
	}
	bindingKey := certName + dnsProfileBindingSuffix

	switch choice {
	case "http":
		a.Database.Delete("acme", bindingKey)
		if err := setCertUseDNS(certName, false); err != nil {
			utils.SendErrorResponse(w, "failed to update certificate renew settings: "+err.Error())
			return
		}
		a.Logf("Certificate "+certName+" now uses the HTTP-01 challenge", nil)
	case "legacy":
		if !a.Database.KeyExists("acme", certName+legacyDNSProviderSuffix) || !a.Database.KeyExists("acme", certName+legacyDNSCredSuffix) {
			utils.SendErrorResponse(w, "this certificate has no per-certificate DNS credentials")
			return
		}
		a.Database.Delete("acme", bindingKey)
		if err := setCertUseDNS(certName, true); err != nil {
			utils.SendErrorResponse(w, "failed to update certificate renew settings: "+err.Error())
			return
		}
		a.Logf("Certificate "+certName+" now uses its per-certificate DNS credentials", nil)
	default:
		profile, err := a.loadDNSProfile(choice)
		if err != nil {
			utils.SendErrorResponse(w, err.Error())
			return
		}
		if err := a.Database.Write("acme", bindingKey, profile.ID); err != nil {
			utils.SendErrorResponse(w, "failed to save certificate binding")
			return
		}
		if err := setCertUseDNS(certName, true); err != nil {
			utils.SendErrorResponse(w, "failed to update certificate renew settings: "+err.Error())
			return
		}
		a.Logf("Certificate "+certName+" now uses DNS credential set \""+profile.Name+"\"", nil)
	}
	utils.SendOK(w)
}

// HandleImportDNSProfileFromCert copies a certificate's per-certificate
// credentials into a new reusable credential set and binds the certificate
// to it. The per-certificate credentials are left in place as a fallback.
func (a *ACMEHandler) HandleImportDNSProfileFromCert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.ensureDNSProfileTables()

	certName, err := utils.PostPara(r, "filename")
	if err != nil || !isValidCertName(certName) {
		utils.SendErrorResponse(w, "invalid certificate name")
		return
	}
	name, err := utils.PostPara(r, "name")
	name = strings.TrimSpace(name)
	if err != nil || name == "" || len(name) > maxDNSProfileNameLength {
		utils.SendErrorResponse(w, "a name of 1-64 characters is required")
		return
	}
	existing, _ := a.listDNSProfiles()
	for _, p := range existing {
		if strings.EqualFold(p.Name, name) {
			utils.SendErrorResponse(w, "a credential set with this name already exists")
			return
		}
	}

	var provider, credentials string
	if a.Database.Read("acme", certName+legacyDNSProviderSuffix, &provider) != nil ||
		a.Database.Read("acme", certName+legacyDNSCredSuffix, &credentials) != nil {
		utils.SendErrorResponse(w, "this certificate has no per-certificate DNS credentials")
		return
	}
	if _, err := normalizeDNSCredentials(provider, credentials); err != nil {
		utils.SendErrorResponse(w, "stored credentials could not be read: "+err.Error())
		return
	}

	id, err := newDNSProfileID()
	if err != nil {
		utils.SendErrorResponse(w, "failed to generate id")
		return
	}
	now := time.Now().Unix()
	profile := &DNSProfile{ID: id, Name: name, Provider: provider, Credentials: credentials, CreatedAt: now, UpdatedAt: now}
	if err := a.Database.Write(dnsProfileTable, id, profile); err != nil {
		utils.SendErrorResponse(w, "failed to save credential set")
		return
	}
	if err := a.Database.Write("acme", certName+dnsProfileBindingSuffix, id); err != nil {
		utils.SendErrorResponse(w, "credential set created, but binding the certificate failed")
		return
	}
	a.Logf("Created DNS credential set \""+name+"\" from "+certName+"'s per-certificate credentials", nil)

	js, _ := json.Marshal(map[string]string{"id": id})
	utils.SendJSONResponse(w, string(js))
}

// HandleDeleteLegacyDNSCredentials removes a certificate's upstream-style
// per-certificate DNS credentials. Refused while the certificate still relies
// on them: DNS-01 renewal with no credential set assigned.
func (a *ACMEHandler) HandleDeleteLegacyDNSCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.ensureDNSProfileTables()

	certName, err := utils.PostPara(r, "filename")
	if err != nil || !isValidCertName(certName) {
		utils.SendErrorResponse(w, "invalid certificate name")
		return
	}
	if !a.Database.KeyExists("acme", certName+legacyDNSProviderSuffix) && !a.Database.KeyExists("acme", certName+legacyDNSCredSuffix) {
		utils.SendErrorResponse(w, "this certificate has no per-certificate DNS credentials")
		return
	}
	bound := a.Database.KeyExists("acme", certName+dnsProfileBindingSuffix)
	if !bound && certFileExists(certName) {
		if info, err := LoadCertInfoJSON(filepath.Join(acmeCertStoreDir, certName+".json")); err == nil && info.UseDNS {
			utils.SendErrorResponse(w, "this certificate still renews with its per-certificate credentials. Assign a credential set or HTTP-01 first.")
			return
		}
	}
	for _, suffix := range []string{legacyDNSProviderSuffix, legacyDNSCredSuffix, "_dns_servers"} {
		a.Database.Delete("acme", certName+suffix)
	}
	a.Logf("Deleted per-certificate DNS credentials of "+certName, nil)
	utils.SendOK(w)
}
