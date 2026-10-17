package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imuslab.com/zoraxy/mod/info/logger"
)

func TestVersionCompare(t *testing.T) {
	cases := []struct {
		a, b  string
		newer bool
	}{
		{"2.4.0", "2.3.0", true},
		{"v2.3.1", "2.3.0", true},
		{"2.10.0", "2.9.9", true},
		{"3.0.0", "2.99.99", true},
		{"2.3.0", "2.3.0", false},
		{"2.2.9", "2.3.0", false},
		{"2.4.0-rc1", "2.3.0", false}, // not a release version
		{"junk", "2.3.0", false},
	}
	for _, c := range cases {
		if got := versionNewer(c.a, c.b); got != c.newer {
			t.Errorf("versionNewer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestUpdateAllowedHost(t *testing.T) {
	ok := []string{"https://github.com/x", "https://api.github.com/x", "https://objects.githubusercontent.com/x", "https://release-assets.githubusercontent.com/x"}
	bad := []string{"http://github.com/x", "https://github.com.evil.example/x", "https://evilgithubusercontent.com/x", "https://example.com/x"}
	for _, s := range ok {
		u, _ := url.Parse(s)
		if !updateAllowedHost(u) {
			t.Errorf("%s refused", s)
		}
	}
	for _, s := range bad {
		u, _ := url.Parse(s)
		if updateAllowedHost(u) {
			t.Errorf("%s allowed", s)
		}
	}
}

// fakeBinary is a shell script that answers -version like Waymark.
func fakeBinary(version string) []byte {
	return []byte("#!/bin/sh\necho '" + PRODUCT_NAME + " " + version + " (based on Zoraxy 3.3.5)'\n")
}

// rewriteTransport sends every request to the test server, whatever host
// it names, so the GitHub URLs in the code can be served locally.
type rewriteTransport struct{ target *url.URL }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.URL.Scheme, r2.URL.Host = rt.target.Scheme, rt.target.Host
	r2.Header.Set("X-Original-Host", r.URL.Host)
	return http.DefaultTransport.RoundTrip(r2)
}

func TestUpdateInstall(t *testing.T) {
	if SystemWideLogger == nil {
		SystemWideLogger, _ = logger.NewFmtLogger()
	}
	newVer := "99.0.0"
	bin := fakeBinary(newVer)
	sum := sha256.Sum256(bin)
	tag := "v" + newVer
	repo := updateRepoPath()
	assetPath := "/" + repo + "/releases/download/" + tag + "/" + updateAssetName()
	corrupt := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/"+repo+"/releases/latest":
			json.NewEncoder(w).Encode(map[string]any{
				"tag_name": tag, "body": "notes", "assets": []map[string]any{
					{"name": updateAssetName(), "browser_download_url": PRODUCT_REPO + "/releases/download/" + tag + "/" + updateAssetName(), "size": len(bin)},
					{"name": updateAssetName() + ".sha256", "browser_download_url": PRODUCT_REPO + "/releases/download/" + tag + "/" + updateAssetName() + ".sha256"},
					{"name": "other", "browser_download_url": "https://example.com/other"},
				}})
		case r.URL.Path == assetPath+".sha256":
			w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + updateAssetName() + "\n"))
		case r.URL.Path == assetPath:
			// GitHub redirects release downloads to its CDN
			if r.Header.Get("X-Original-Host") == "github.com" {
				http.Redirect(w, r, "https://objects.githubusercontent.com"+assetPath, http.StatusFound)
				return
			}
			if corrupt {
				w.Write(append(bin, '#'))
				return
			}
			w.Write(bin)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	target, _ := url.Parse(srv.URL)
	updateTransport = rewriteTransport{target}
	defer func() { updateTransport = nil }()
	restarted := ""
	updateRestartFn = func(exe, msg string) { restarted = exe }
	defer func() { updateRestartFn = updateRestart }()

	rel, err := updateFetchLatest()
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != newVer || rel.AssetURL == "" || rel.SumURL == "" {
		t.Fatalf("release: %+v", rel)
	}

	dir := t.TempDir()
	exe := filepath.Join(dir, "waymark")
	os.WriteFile(exe, fakeBinary(DISPLAY_VERSION), 0755)

	// A download that does not match the checksum is refused and nothing changes
	corrupt = true
	if err := updateInstall(exe, rel); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("corrupt download: %v", err)
	}
	if v, _ := binaryVersion(exe); v != DISPLAY_VERSION || restarted != "" {
		t.Fatalf("binary changed after a failed update: %s", v)
	}
	corrupt = false
	if err := updateInstall(exe, rel); err != nil {
		t.Fatal(err)
	}
	if v, err := binaryVersion(exe); err != nil || v != newVer {
		t.Fatalf("installed version %q %v", v, err)
	}
	if v, _ := binaryVersion(exe + updatePrevSuffix); v != DISPLAY_VERSION {
		t.Fatalf("previous binary not kept: %q", v)
	}
	if restarted != exe {
		t.Fatal("no restart after installing")
	}
	st, ok := readUpdateState(exe)
	if !ok || st.Phase != "installed" || st.From != DISPLAY_VERSION || st.To != newVer {
		t.Fatalf("state: %+v", st)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".waymark-update-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}

	// A binary reporting another version than the release is refused
	rel2 := *rel
	rel2.Version = "99.0.1"
	if err := updateInstall(exe, &rel2); err == nil || !strings.Contains(err.Error(), "reports version") {
		t.Fatalf("version mismatch: %v", err)
	}
}

func TestBinaryVersion(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	os.WriteFile(good, fakeBinary("2.4.0"), 0755)
	if v, err := binaryVersion(good); err != nil || v != "2.4.0" {
		t.Fatalf("%q %v", v, err)
	}
	other := filepath.Join(dir, "other")
	os.WriteFile(other, []byte("#!/bin/sh\necho 'Zoraxy 3.3.5'\n"), 0755)
	if _, err := binaryVersion(other); err == nil {
		t.Fatal("non-Waymark binary accepted")
	}
	broken := filepath.Join(dir, "broken")
	os.WriteFile(broken, []byte("#!/bin/sh\nexit 3\n"), 0755)
	if _, err := binaryVersion(broken); err == nil {
		t.Fatal("failing binary accepted")
	}
}
