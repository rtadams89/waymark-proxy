package dynamicproxy

import (
	"crypto/tls"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"imuslab.com/zoraxy/mod/access"
)

func TestTLSProfiles(t *testing.T) {
	cases := []struct {
		p      TLSProfile
		min    uint16
		suites bool
	}{
		{TLSProfileModern, tls.VersionTLS13, false},
		{TLSProfileIntermediate, tls.VersionTLS12, true},
		{TLSProfileCompatible, tls.VersionTLS12, false},
		{TLSProfileLegacy, tls.VersionTLS10, false},
	}
	for _, c := range cases {
		if !c.p.IsValid() {
			t.Fatalf("%s is not valid", c.p)
		}
		cfg := &tls.Config{}
		c.p.Apply(cfg)
		if cfg.MinVersion != c.min {
			t.Errorf("%s: MinVersion %x, want %x", c.p, cfg.MinVersion, c.min)
		}
		if (cfg.CipherSuites != nil) != c.suites {
			t.Errorf("%s: CipherSuites %v", c.p, cfg.CipherSuites)
		}
	}
	for _, bad := range []TLSProfile{"", "default", "MODERN", "old"} {
		if bad.IsValid() {
			t.Errorf("%q is valid", bad)
		}
		cfg := &tls.Config{}
		bad.Apply(cfg)
		if cfg.MinVersion != tls.VersionTLS12 || len(cfg.CipherSuites) != len(intermediateSuites) {
			t.Errorf("%q was not treated as intermediate", bad)
		}
	}
}

func TestIntermediateSuitesAreStrong(t *testing.T) {
	secure := map[uint16]bool{}
	for _, s := range tls.CipherSuites() {
		secure[s.ID] = true
	}
	for _, id := range TLSProfileIntermediate.CipherSuites() {
		name := tls.CipherSuiteName(id)
		if !secure[id] {
			t.Errorf("%s is not one of Go's secure suites", name)
		}
		if !strings.HasPrefix(name, "TLS_ECDHE_") || strings.Contains(name, "CBC") || strings.HasSuffix(name, "_SHA") {
			t.Errorf("%s is not an ECDHE AEAD suite", name)
		}
	}
	s := TLSProfileIntermediate.CipherSuites()
	s[0] = 0
	if slices.Contains(TLSProfileIntermediate.CipherSuites(), 0) {
		t.Error("changing the returned list changed the profile")
	}
}

func TestRouterOptionStaysJSONEncodable(t *testing.T) {
	o := RouterOption{TLSProfile: TLSProfileModern, TLSProfileFor: func(string) (TLSProfile, string, bool) { return "", "", false },
		AccessController: &access.Controller{ExtraObjectUsers: func(string) []string { return nil }}}
	if _, err := json.Marshal(o); err != nil {
		t.Fatal(err)
	}
}
