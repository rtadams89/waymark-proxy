package acme

/*
	DNS challenge credential sets, shared by any number of certificates. A
	certificate's binding key <certName>_dns_profile in the "acme" table
	holds a set's ID. The list API returns which fields are set but not
	their values; an empty string field in an edit keeps the stored value.
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
	acmeCertStoreDir         = "./conf/certs"
	maxDNSProfileNameLength  = 64
	dnsProfileIDLengthInByte = 8
)

// DNSProfile is a named, reusable set of DNS challenge credentials.
type DNSProfile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`    // lego provider id, e.g. "cloudflare"
	Credentials string `json:"credentials"` // JSON object of lego provider settings
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

// dnsProfileSummary is what the UI is allowed to see about a profile.
type dnsProfileSummary struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Provider  string            `json:"provider"`
	FieldsSet []string          `json:"fieldsSet"` // string fields that have a stored value
	Settings  map[string]any    `json:"settings"`  // non-string values
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
	UseDNS      bool   `json:"useDNS"`
	AcmeManaged bool   `json:"acmeManaged"` // has an ACME info file
}

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

// isValidCertName reports whether name is safe as a database key prefix and
// file name.
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

// listDNSProfileBindings returns certName -> profileID for installed
// certificates.
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

// normalizeDNSCredentials checks a credential JSON object against the
// provider's schema, rejecting unknown keys.
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

// mergeDNSCredentials applies update to stored; empty string values keep
// the stored value.
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

// summarizeDNSCredentials returns the names of string fields with a value
// (and fields not in the schema), and the values of non-string fields.
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

// resolveDNSCredentials returns the provider and credential JSON of the
// credential set bound to a certificate.
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

	return "", "", "", errors.New("no DNS credential set is assigned to certificate " + certificateName + ": pick one with its challenge settings button on Certificates > Installed Certificates")
}

// setCertUseDNS sets the DNS challenge flag in a certificate's ACME info
// file, if it has one.
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
	return utils.WriteFileAtomic(infoPath, js, st.Mode().Perm())
}

/* HTTP handlers under /api/acme/dnsProfiles/ */

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
		update = mergeDNSCredentials(stored, update)
	} else {
		id, err = newDNSProfileID()
		if err != nil {
			utils.SendErrorResponse(w, "failed to generate id")
			return
		}
		profile = &DNSProfile{ID: id, CreatedAt: now}
	}

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

	// Builds the provider to validate the credentials
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
