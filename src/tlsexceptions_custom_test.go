package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imuslab.com/zoraxy/mod/access"
	"imuslab.com/zoraxy/mod/database"
	"imuslab.com/zoraxy/mod/dynamicproxy"
	"imuslab.com/zoraxy/mod/info/logger"
)

func withTestAccessController(t *testing.T) *access.Controller {
	t.Helper()
	dir := t.TempDir()
	db, err := database.NewDatabase(filepath.Join(dir, "sys.db"), database.GetRecommendedBackendType())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	lg, _ := logger.NewFmtLogger()
	if SystemWideLogger == nil {
		SystemWideLogger = lg
	}
	tp := filepath.Join(dir, "trusted_proxies.json")
	os.WriteFile(tp, []byte("[]"), 0644)
	c, err := access.NewAccessController(&access.Options{Logger: *lg, Database: db, ConfigFolder: filepath.Join(dir, "acl"), TrustedProxiesFile: tp})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	old := accessController
	accessController = c
	t.Cleanup(func() { accessController = old; tlsExceptions.Store(nil) })
	return c
}

func TestNormalizeTLSExceptions(t *testing.T) {
	c := withTestAccessController(t)
	in := []tlsProfileException{
		{ID: "a", Enabled: true, Source: access.Source{Type: access.SourceCIDR, Value: "10.20.0.9/24"}, Profile: dynamicproxy.TLSProfileCompatible, Comment: "  Old printers "},
		{ID: "a", Enabled: true, Source: access.Source{Type: access.SourceObject, Value: access.PrivateNetworksID, Not: true}, Profile: dynamicproxy.TLSProfileModern},
	}
	out, err := normalizeTLSExceptions(in, c.NormalizeSource)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Source.Value != "10.20.0.0/24" || out[0].Comment != "Old printers" {
		t.Errorf("not normalized: %+v", out[0])
	}
	if out[1].ID == "a" || out[1].ID == "" {
		t.Errorf("a duplicate ID was kept: %q", out[1].ID)
	}
	bad := [][]tlsProfileException{
		{{Source: access.Source{Type: access.SourceAny}, Profile: dynamicproxy.TLSProfileModern}},
		{{Source: access.Source{Type: access.SourceIP, Value: "x"}, Profile: dynamicproxy.TLSProfileModern}},
		{{Source: access.Source{Type: access.SourceObject, Value: "missing"}, Profile: dynamicproxy.TLSProfileModern}},
		{{Source: access.Source{Type: access.SourceIP, Value: "10.0.0.1"}, Profile: "old"}},
		{{Source: access.Source{Type: access.SourceIP, Value: "10.0.0.1"}, Profile: dynamicproxy.TLSProfileModern, Comment: strings.Repeat("x", 201)}},
	}
	for _, b := range bad {
		if _, err := normalizeTLSExceptions(b, c.NormalizeSource); err == nil {
			t.Errorf("%+v was accepted", b)
		}
	}
	many := make([]tlsProfileException, maxTLSExceptions+1)
	if _, err := normalizeTLSExceptions(many, c.NormalizeSource); err == nil {
		t.Error("too many exceptions were accepted")
	}
}

func TestChooseTLSProfileFirstMatchWins(t *testing.T) {
	c := withTestAccessController(t)
	list, err := normalizeTLSExceptions([]tlsProfileException{
		{Enabled: false, Source: access.Source{Type: access.SourceIP, Value: "10.20.0.5"}, Profile: dynamicproxy.TLSProfileModern},
		{Enabled: true, Source: access.Source{Type: access.SourceCIDR, Value: "10.20.0.0/24"}, Profile: dynamicproxy.TLSProfileLegacy, Comment: "Old printers"},
		{Enabled: true, Source: access.Source{Type: access.SourceObject, Value: access.PrivateNetworksID, Not: true}, Profile: dynamicproxy.TLSProfileModern},
	}, c.NormalizeSource)
	if err != nil {
		t.Fatal(err)
	}
	if p, _, ok := chooseTLSProfile("10.20.0.5"); ok {
		t.Fatalf("no list loaded, but got %s", p)
	}
	tlsExceptions.Store(&list)
	cases := []struct {
		ip   string
		want dynamicproxy.TLSProfile
		name string
		ok   bool
	}{
		{"10.20.0.5", dynamicproxy.TLSProfileLegacy, "Old printers", true},
		{"8.8.8.8", dynamicproxy.TLSProfileModern, "#3 not Private networks", true},
		{"192.168.1.10", "", "", false},
	}
	for _, tc := range cases {
		p, name, ok := chooseTLSProfile(tc.ip)
		if p != tc.want || name != tc.name || ok != tc.ok {
			t.Errorf("%s: got %q %q %v, want %q %q %v", tc.ip, p, name, ok, tc.want, tc.name, tc.ok)
		}
	}
	o, _ := c.PrepareObject(&access.Object{Name: "Printers", Entries: []access.Entry{{Type: access.SourceIP, Value: "10.0.0.9"}}})
	c.SaveObject(o)
	list = append(list, tlsProfileException{Enabled: true, Source: access.Source{Type: access.SourceObject, Value: o.ID}, Profile: dynamicproxy.TLSProfileCompatible})
	tlsExceptions.Store(&list)
	c.ExtraObjectUsers = tlsExceptionObjectUsers
	if err := c.DeleteObject(o.ID); err == nil || !strings.Contains(err.Error(), "TLS profile exceptions") {
		t.Fatalf("an object used by an exception was deleted: %v", err)
	}
}
