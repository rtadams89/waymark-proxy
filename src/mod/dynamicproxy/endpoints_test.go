package dynamicproxy

import "testing"

// TestVirtualDirectoryEndpointHasSameTarget tests the target comparison.
func TestVirtualDirectoryEndpointHasSameTarget(t *testing.T) {
	vdir := &VirtualDirectoryEndpoint{
		MatchingPath:        "/outpost.goauthentik.io",
		Domain:              "10.0.0.6:9000/outpost.goauthentik.io",
		RequireTLS:          false,
		SkipCertValidations: false,
	}

	if !vdir.HasSameTarget("10.0.0.6:9000/outpost.goauthentik.io", false, false) {
		t.Errorf("identical target should match")
	}
	if vdir.HasSameTarget("10.0.0.6:9000/other", false, false) {
		t.Errorf("different domain must not match")
	}
	if vdir.HasSameTarget("10.0.0.6:9000/outpost.goauthentik.io", true, false) {
		t.Errorf("different RequireTLS must not match")
	}
	if vdir.HasSameTarget("10.0.0.6:9000/outpost.goauthentik.io", false, true) {
		t.Errorf("different SkipCertValidations must not match")
	}
}

// TestClassifyBulkVdir tests the per-host action for apply and remove,
// including conflicts.
func TestClassifyBulkVdir(t *testing.T) {
	const domain = "10.0.0.6:9000/outpost.goauthentik.io"
	identical := &VirtualDirectoryEndpoint{MatchingPath: "/outpost.goauthentik.io", Domain: domain}
	customized := &VirtualDirectoryEndpoint{MatchingPath: "/outpost.goauthentik.io", Domain: "10.0.0.6:9000/something-else"}

	cases := []struct {
		name          string
		ensurePresent bool
		existing      *VirtualDirectoryEndpoint
		want          BulkVdirAction
	}{
		{"apply: missing -> create", true, nil, BulkVdirCreate},
		{"apply: identical -> skip", true, identical, BulkVdirSkip},
		{"apply: customized -> conflict", true, customized, BulkVdirConflict},
		{"remove: absent -> noop", false, nil, BulkVdirNoop},
		{"remove: identical -> remove", false, identical, BulkVdirRemove},
		{"remove: customized -> conflict", false, customized, BulkVdirConflict},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyBulkVdir(tc.ensurePresent, tc.existing, domain, false, false); got != tc.want {
				t.Errorf("ClassifyBulkVdir(ensurePresent=%v) = %q, want %q", tc.ensurePresent, got, tc.want)
			}
		})
	}
}
