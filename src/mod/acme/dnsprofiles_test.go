package acme

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imuslab.com/zoraxy/mod/database"
	"imuslab.com/zoraxy/mod/database/dbinc"
	"imuslab.com/zoraxy/mod/info/logger"
)

// newTestACMEHandler returns a handler backed by a throwaway database, with
// the working directory moved to a temp dir so ./conf/certs is isolated.
func newTestACMEHandler(t *testing.T) *ACMEHandler {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(acmeCertStoreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := database.NewDatabase(filepath.Join(dir, "sys.db"), dbinc.BackendBoltDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	l, err := logger.NewFmtLogger()
	if err != nil {
		t.Fatal(err)
	}
	return &ACMEHandler{Database: db, Logger: l}
}

func installFakeCert(t *testing.T, certName string, info *CertificateInfoJSON) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(acmeCertStoreDir, certName+".pem"), []byte("pem"), 0o644); err != nil {
		t.Fatal(err)
	}
	if info != nil {
		js, _ := json.Marshal(info)
		if err := os.WriteFile(filepath.Join(acmeCertStoreDir, certName+".json"), js, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func postForm(t *testing.T, h http.HandlerFunc, form url.Values) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h(rec, req)
	out := map[string]any{}
	body := strings.TrimSpace(rec.Body.String())
	if body != "" && body != `"OK"` {
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("response is not JSON: %q", body)
		}
	}
	return out
}

func getBody(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Body.String()
}

func TestNormalizeDNSCredentials(t *testing.T) {
	if _, err := normalizeDNSCredentials("not-a-provider", `{}`); err == nil {
		t.Error("unknown provider should be rejected")
	}
	if _, err := normalizeDNSCredentials("cloudflare", `{"AuthTokn":"x"}`); err == nil {
		t.Error("misspelled field should be rejected")
	}
	if _, err := normalizeDNSCredentials("cloudflare", `not json`); err == nil {
		t.Error("non-JSON credentials should be rejected")
	}
	creds, err := normalizeDNSCredentials("cloudflare", `{"AuthToken":"abc","ZoneToken":"abc"}`)
	if err != nil || creds["AuthToken"] != "abc" {
		t.Errorf("valid credentials rejected: %v", err)
	}
}

func TestMergeKeepsSecretsWhenFieldLeftBlank(t *testing.T) {
	stored := map[string]any{"AuthToken": "secret", "ZoneToken": "zone", "TTL": float64(120)}
	merged := mergeDNSCredentials(stored, map[string]any{"AuthToken": "", "ZoneToken": "new", "TTL": float64(300)})
	if merged["AuthToken"] != "secret" {
		t.Errorf("blank field should keep stored secret, got %v", merged["AuthToken"])
	}
	if merged["ZoneToken"] != "new" || merged["TTL"] != float64(300) {
		t.Errorf("filled fields should be updated, got %v", merged)
	}
}

func TestSummarizeNeverExposesStringValues(t *testing.T) {
	// cloudflare: AuthToken/AuthEmail are strings, the timeouts are durations,
	// and Mystery is not in the schema at all (so it must be treated as secret)
	fields, settings := summarizeDNSCredentials("cloudflare", `{"AuthToken":"secret","AuthEmail":"","PollingInterval":"2","PropagationTimeout":"600","Mystery":"x"}`)
	if strings.Join(fields, ",") != "AuthToken,Mystery" {
		t.Errorf("fieldsSet = %v, want [AuthToken Mystery]", fields)
	}
	if settings["PollingInterval"] != "2" || settings["PropagationTimeout"] != "600" {
		t.Errorf("non-secret settings missing: %v", settings)
	}
	for k := range settings {
		if k == "AuthToken" || k == "AuthEmail" || k == "Mystery" {
			t.Errorf("settings exposed string field %q", k)
		}
	}
}

func TestIsValidCertName(t *testing.T) {
	for _, bad := range []string{"", "../sys", "a/b", `a\b`, "..", strings.Repeat("a", 256)} {
		if isValidCertName(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
	for _, good := range []string{"example.com", "_.example.com", "dev.example.com"} {
		if !isValidCertName(good) {
			t.Errorf("%q should be accepted", good)
		}
	}
}

func TestResolvePrefersBoundProfileOverLegacy(t *testing.T) {
	a := newTestACMEHandler(t)
	a.ensureDNSProfileTables()
	a.Database.Write("acme", "example.com"+legacyDNSProviderSuffix, "cloudflare")
	a.Database.Write("acme", "example.com"+legacyDNSCredSuffix, `{"AuthToken":"legacy"}`)

	prov, creds, _, err := a.resolveDNSCredentials("example.com")
	if err != nil || prov != "cloudflare" || !strings.Contains(creds, "legacy") {
		t.Fatalf("legacy fallback failed: %v %v %v", prov, creds, err)
	}

	profile := &DNSProfile{ID: "0123456789abcdef", Name: "CF", Provider: "cloudflare", Credentials: `{"AuthToken":"shared"}`}
	a.Database.Write(dnsProfileTable, profile.ID, profile)
	a.Database.Write("acme", "example.com"+dnsProfileBindingSuffix, profile.ID)

	_, creds, source, err := a.resolveDNSCredentials("example.com")
	if err != nil || !strings.Contains(creds, "shared") || !strings.Contains(source, "CF") {
		t.Fatalf("bound profile not used: %v %v %v", creds, source, err)
	}

	// A binding to a deleted profile must fail loudly, not silently fall back
	a.Database.Delete(dnsProfileTable, profile.ID)
	if _, _, _, err := a.resolveDNSCredentials("example.com"); err == nil {
		t.Error("expected an error for a binding to a missing profile")
	}

	if _, _, _, err := a.resolveDNSCredentials("nothing.example.com"); err == nil {
		t.Error("expected an error when no credentials exist")
	}
}

func TestProfileLifecycle(t *testing.T) {
	a := newTestACMEHandler(t)
	installFakeCert(t, "_.example.com", &CertificateInfoJSON{AcmeName: "Let's Encrypt", UseDNS: false})

	// Create
	res := postForm(t, a.HandleSaveDNSProfile, url.Values{
		"name":        {"Home Cloudflare"},
		"provider":    {"cloudflare"},
		"credentials": {`{"AuthToken":"tok-123","ZoneToken":"tok-123"}`},
	})
	id, _ := res["id"].(string)
	if !isValidDNSProfileID(id) {
		t.Fatalf("save did not return an id: %v", res)
	}

	// Duplicate names are refused
	res = postForm(t, a.HandleSaveDNSProfile, url.Values{"name": {"home cloudflare"}, "provider": {"cloudflare"}, "credentials": {`{}`}})
	if res["error"] == nil {
		t.Error("duplicate name should be refused")
	}

	// The list must never contain the secret
	list := getBody(t, a.HandleListDNSProfiles)
	if strings.Contains(list, "tok-123") {
		t.Fatalf("list leaked a secret: %s", list)
	}
	if !strings.Contains(list, "AuthToken") {
		t.Errorf("list should report which fields are set: %s", list)
	}

	// Edit with blank secrets keeps them
	postForm(t, a.HandleSaveDNSProfile, url.Values{
		"id": {id}, "name": {"Home CF"}, "provider": {"cloudflare"},
		"credentials": {`{"AuthToken":"","ZoneToken":""}`},
	})
	p, err := a.loadDNSProfile(id)
	if err != nil || p.Name != "Home CF" || !strings.Contains(p.Credentials, "tok-123") {
		t.Fatalf("edit lost data: %+v %v", p, err)
	}

	// Assign to the certificate: binding written and renewals switched to DNS-01
	res = postForm(t, a.HandleAssignDNSProfile, url.Values{"filename": {"_.example.com"}, "profileId": {id}})
	if res["error"] != nil {
		t.Fatalf("assign failed: %v", res)
	}
	info, _ := LoadCertInfoJSON(filepath.Join(acmeCertStoreDir, "_.example.com.json"))
	if !info.UseDNS {
		t.Error("assigning a credential set should enable DNS-01 for renewals")
	}
	bindings := getBody(t, a.HandleListDNSBindings)
	if !strings.Contains(bindings, `"profileName":"Home CF"`) {
		t.Errorf("bindings missing profile: %s", bindings)
	}

	// Path traversal in the certificate name is refused
	res = postForm(t, a.HandleAssignDNSProfile, url.Values{"filename": {"../../sys"}, "profileId": {id}})
	if res["error"] == nil {
		t.Error("traversal in filename should be refused")
	}

	// Cannot delete while in use
	res = postForm(t, a.HandleDeleteDNSProfile, url.Values{"id": {id}})
	if res["error"] == nil {
		t.Error("deleting an in-use credential set should be refused")
	}

	// Switch the cert back to HTTP-01, then delete succeeds
	postForm(t, a.HandleAssignDNSProfile, url.Values{"filename": {"_.example.com"}, "profileId": {"http"}})
	info, _ = LoadCertInfoJSON(filepath.Join(acmeCertStoreDir, "_.example.com.json"))
	if info.UseDNS {
		t.Error("switching to http should disable DNS-01")
	}
	res = postForm(t, a.HandleDeleteDNSProfile, url.Values{"id": {id}})
	if res["error"] != nil {
		t.Fatalf("delete failed: %v", res)
	}
}

func TestStaleBindingDoesNotBlockDelete(t *testing.T) {
	a := newTestACMEHandler(t)
	res := postForm(t, a.HandleSaveDNSProfile, url.Values{"name": {"CF"}, "provider": {"cloudflare"}, "credentials": {`{"AuthToken":"x"}`}})
	id := res["id"].(string)
	// Binding left behind by a certificate that was since deleted
	a.Database.Write("acme", "gone.example.com"+dnsProfileBindingSuffix, id)
	res = postForm(t, a.HandleDeleteDNSProfile, url.Values{"id": {id}})
	if res["error"] != nil {
		t.Errorf("stale binding blocked delete: %v", res)
	}
}

func TestImportFromCert(t *testing.T) {
	a := newTestACMEHandler(t)
	installFakeCert(t, "example.com", &CertificateInfoJSON{UseDNS: true})
	a.ensureDNSProfileTables()
	a.Database.Write("acme", "example.com"+legacyDNSProviderSuffix, "cloudflare")
	a.Database.Write("acme", "example.com"+legacyDNSCredSuffix, `{"AuthToken":"legacy-tok"}`)

	res := postForm(t, a.HandleImportDNSProfileFromCert, url.Values{"filename": {"example.com"}, "name": {"Imported"}})
	id, _ := res["id"].(string)
	if id == "" {
		t.Fatalf("import failed: %v", res)
	}
	_, creds, source, err := a.resolveDNSCredentials("example.com")
	if err != nil || !strings.Contains(creds, "legacy-tok") || !strings.Contains(source, "Imported") {
		t.Errorf("imported set not bound: %v %v %v", creds, source, err)
	}
}

func TestDeleteLegacyCredentials(t *testing.T) {
	a := newTestACMEHandler(t)
	installFakeCert(t, "example.com", &CertificateInfoJSON{UseDNS: true})
	a.ensureDNSProfileTables()
	a.Database.Write("acme", "example.com"+legacyDNSProviderSuffix, "cloudflare")
	a.Database.Write("acme", "example.com"+legacyDNSCredSuffix, `{"AuthToken":"legacy-tok"}`)
	a.Database.Write("acme", "example.com_dns_servers", "")

	// Still in use: renews via DNS-01 with no credential set
	res := postForm(t, a.HandleDeleteLegacyDNSCredentials, url.Values{"filename": {"example.com"}})
	if res["error"] == nil || !a.Database.KeyExists("acme", "example.com"+legacyDNSCredSuffix) {
		t.Fatalf("delete of in-use credentials should be refused: %v", res)
	}

	// Move it to a credential set, then delete succeeds
	res = postForm(t, a.HandleImportDNSProfileFromCert, url.Values{"filename": {"example.com"}, "name": {"CF"}})
	if res["id"] == nil {
		t.Fatalf("import failed: %v", res)
	}
	res = postForm(t, a.HandleDeleteLegacyDNSCredentials, url.Values{"filename": {"example.com"}})
	if res["error"] != nil {
		t.Fatalf("delete refused: %v", res)
	}
	for _, s := range []string{legacyDNSProviderSuffix, legacyDNSCredSuffix, "_dns_servers"} {
		if a.Database.KeyExists("acme", "example.com"+s) {
			t.Errorf("key %s not removed", s)
		}
	}
	// Renewals still resolve through the credential set
	if _, creds, _, err := a.resolveDNSCredentials("example.com"); err != nil || !strings.Contains(creds, "legacy-tok") {
		t.Errorf("credential set no longer resolves: %v %v", creds, err)
	}
	// Nothing left to delete; traversal refused
	if res = postForm(t, a.HandleDeleteLegacyDNSCredentials, url.Values{"filename": {"example.com"}}); res["error"] == nil {
		t.Error("second delete should report nothing to delete")
	}
	if res = postForm(t, a.HandleDeleteLegacyDNSCredentials, url.Values{"filename": {"../sys"}}); res["error"] == nil {
		t.Error("traversal should be refused")
	}

	// HTTP-01 certificate: per-certificate credentials not in use, deletable
	installFakeCert(t, "http.example.com", &CertificateInfoJSON{UseDNS: false})
	a.Database.Write("acme", "http.example.com"+legacyDNSProviderSuffix, "cloudflare")
	a.Database.Write("acme", "http.example.com"+legacyDNSCredSuffix, `{}`)
	if res = postForm(t, a.HandleDeleteLegacyDNSCredentials, url.Values{"filename": {"http.example.com"}}); res["error"] != nil {
		t.Errorf("HTTP-01 cert credentials should be deletable: %v", res)
	}
}
