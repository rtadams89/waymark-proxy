package main

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imuslab.com/zoraxy/mod/auth"
	"imuslab.com/zoraxy/mod/database"
	"imuslab.com/zoraxy/mod/info/logger"
)

func TestSetupCodeFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		c := newSetupCode()
		if len(c) != 14 || c[4] != '-' || c[9] != '-' {
			t.Fatalf("bad format %q", c)
		}
		for _, r := range strings.ReplaceAll(c, "-", "") {
			if !strings.ContainsRune(setupCodeAlphabet, r) {
				t.Fatalf("%q has %q", c, r)
			}
		}
		if seen[c] {
			t.Fatalf("repeated code %q", c)
		}
		seen[c] = true
	}
}

func TestSetupCodeMatches(t *testing.T) {
	want := "AB01-CD1E-FGH0"
	for _, g := range []string{"AB01-CD1E-FGH0", "ab01cd1efgh0", " ab01 cd1e fgh0 ", "ABO1-CDIE-FGHO", "abol-cdle-fgh0"} {
		if !setupCodeMatches(want, g) {
			t.Errorf("%q should match", g)
		}
	}
	for _, g := range []string{"", "AB01-CD1E-FGH", "AB01-CD1E-FGH1", "AB01-CD1E-FGH00"} {
		if setupCodeMatches(want, g) {
			t.Errorf("%q should not match", g)
		}
	}
	if setupCodeMatches("", "") {
		t.Error("an empty code must never match")
	}
}

func newTestAuth(t *testing.T) {
	t.Helper()
	if SystemWideLogger == nil {
		SystemWideLogger, _ = logger.NewFmtLogger()
	}
	db, err := database.NewDatabase(filepath.Join(t.TempDir(), "sys.db"), database.GetRecommendedBackendType())
	if err != nil {
		t.Fatal(err)
	}
	oldDB, oldAgent := sysdb, authAgent
	sysdb = db
	authAgent = auth.NewAuthenticationAgent("test", []byte("0123456789abcdef0123456789abcdef"), db, true, SystemWideLogger, nil)
	t.Cleanup(func() {
		db.Close()
		sysdb, authAgent = oldDB, oldAgent
		adminSetup.code = ""
	})
}

func postRegister(form url.Values) map[string]any {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	HandleAdminRegister(rec, req)
	body := rec.Body.String()
	if body == `"OK"` {
		return map[string]any{"ok": true}
	}
	return map[string]any{"body": body}
}

func TestAdminRegisterNeedsSetupCode(t *testing.T) {
	newTestAuth(t)
	initAdminSetupCode()
	code := adminSetup.code
	if code == "" {
		t.Fatal("no setup code without an admin account")
	}

	for _, form := range []url.Values{
		{"username": {"admin"}, "password": {"pw"}},
		{"username": {"admin"}, "password": {"pw"}, "setupcode": {"0000-0000-0000"}},
	} {
		if r := postRegister(form); r["ok"] == true {
			t.Fatalf("registered without the right code: %v", form)
		}
	}
	if n := len(auth.ListUsernames(sysdb)); n != 0 {
		t.Fatalf("%d accounts created", n)
	}

	if r := postRegister(url.Values{"username": {"bad/name"}, "password": {"pw"}, "setupcode": {code}}); r["ok"] == true {
		t.Fatal("accepted a bad username")
	}
	if r := postRegister(url.Values{"username": {"admin"}, "password": {"pw"}, "setupcode": {strings.ToLower(code)}}); r["ok"] != true {
		t.Fatalf("right code refused: %v", r)
	}
	if !authAgent.ValidateUsernameAndPassword("admin", "pw") {
		t.Fatal("the account was not created")
	}
	if adminSetup.code != "" {
		t.Fatal("the setup code was not cleared")
	}

	// A second account cannot be registered, even with the old code
	if r := postRegister(url.Values{"username": {"other"}, "password": {"pw"}, "setupcode": {code}}); r["ok"] == true {
		t.Fatal("registered a second account")
	}

	// Waymark started with an account: no code
	initAdminSetupCode()
	if adminSetup.code != "" {
		t.Fatal("a setup code was made with an account present")
	}
}

func TestAdminRegisterOnlyPost(t *testing.T) {
	newTestAuth(t)
	initAdminSetupCode()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/register?username=a&password=b&setupcode="+url.QueryEscape(adminSetup.code), nil)
	rec := httptest.NewRecorder()
	HandleAdminRegister(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || len(auth.ListUsernames(sysdb)) != 0 {
		t.Fatalf("GET registered or was not refused (%d)", rec.Code)
	}
}

func readerFor(input string) *passwordReader {
	return &passwordReader{in: bufio.NewReader(strings.NewReader(input)), out: io.Discard}
}

func TestPromptAdminPassword(t *testing.T) {
	u, p, err := promptAdminPassword(readerFor("\nsecret\nsecret\n"), []string{"ryan"})
	if err != nil || u != "ryan" || p != "secret" {
		t.Fatalf("default account: %q %q %v", u, p, err)
	}
	u, p, err = promptAdminPassword(readerFor("new\nsecret\nsecret"), []string{"a", "b"})
	if err != nil || u != "new" || p != "secret" {
		t.Fatalf("new account without trailing newline: %q %q %v", u, p, err)
	}
	for name, in := range map[string]string{
		"no default with two accounts": "\nsecret\nsecret\n",
		"mismatch":                     "ryan\nsecret\nother\n",
		"empty password":               "ryan\n\n\n",
		"end of input":                 "ryan\n",
	} {
		if _, _, err := promptAdminPassword(readerFor(in), []string{"a", "b"}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestACMEFileModes(t *testing.T) {
	if SystemWideLogger == nil {
		SystemWideLogger, _ = logger.NewFmtLogger()
	}
	cases := map[string]os.FileMode{
		"0600": 0o600, "0400": 0o400, "0640": 0o640, "0440": 0o440,
		"0644": 0o600, "0666": 0o600, "0660": 0o600, "0700": 0o600, "0000": 0o600, "0040": 0o600, "nope": 0o600,
	}
	for raw, want := range cases {
		if got := parseACMEFileMode(raw, 0o600, "acmekeymode", acmeKeyModeAllowed); got != want {
			t.Errorf("key mode %s: got %#o, want %#o", raw, got, want)
		}
	}
	pub := map[string]os.FileMode{"0644": 0o644, "0640": 0o640, "0600": 0o600, "0444": 0o444, "0664": 0o644, "0666": 0o644, "0755": 0o644}
	for raw, want := range pub {
		if got := parseACMEFileMode(raw, 0o644, "acmepublicmode", acmePublicModeAllowed); got != want {
			t.Errorf("public mode %s: got %#o, want %#o", raw, got, want)
		}
	}
}
