package access

import (
	"strings"
	"testing"
)

func TestSourceMatches(t *testing.T) {
	c := newACLController(t)
	o, err := c.PrepareObject(&Object{Name: "Old devices", Entries: []Entry{{Type: SourceCIDR, Value: "10.20.0.0/24"}, {Type: SourceObject, Value: PrivateNetworksID}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SaveObject(o); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		src  Source
		ip   string
		want bool
	}{
		{Source{Type: SourceIP, Value: "10.0.0.5"}, "10.0.0.5", true},
		{Source{Type: SourceIP, Value: "10.0.0.5"}, "10.0.0.6", false},
		{Source{Type: SourceCIDR, Value: "10.20.0.0/24"}, "10.20.0.99", true},
		{Source{Type: SourceRange, Value: "10.0.0.10-10.0.0.20"}, "10.0.0.15", true},
		{Source{Type: SourceWildcard, Value: "192.168.*.*"}, "192.168.4.4", true},
		{Source{Type: SourceCountry, Value: "US"}, "8.8.8.8", true},
		{Source{Type: SourceCountry, Value: "US"}, "1.1.1.1", false},
		{Source{Type: SourceObject, Value: o.ID}, "10.20.0.7", true},
		{Source{Type: SourceObject, Value: o.ID}, "172.16.1.1", true},
		{Source{Type: SourceObject, Value: o.ID}, "8.8.8.8", false},
		{Source{Type: SourceObject, Value: PrivateNetworksID, Not: true}, "8.8.8.8", true},
		{Source{Type: SourceObject, Value: PrivateNetworksID, Not: true}, "192.168.1.1", false},
		{Source{Type: SourceObject, Value: "missing"}, "10.0.0.1", false},
		{Source{Type: SourceCIDR, Value: "10.0.0.0/8"}, "::ffff:10.1.2.3", true},
	}
	for _, tc := range cases {
		got, known := c.SourceMatches(tc.src, tc.ip)
		if !known || got != tc.want {
			t.Errorf("%+v %s: got %v (known %v), want %v", tc.src, tc.ip, got, known, tc.want)
		}
	}

	o2, _ := c.GetObject(o.ID)
	o2.Entries = []Entry{{Type: SourceIP, Value: "10.9.9.9"}}
	p, err := c.PrepareObject(o2)
	if err != nil {
		t.Fatal(err)
	}
	c.SaveObject(p)
	if m, _ := c.SourceMatches(Source{Type: SourceObject, Value: o.ID}, "10.9.9.9"); !m {
		t.Error("the edited object was not used")
	}

	c.countryLookup = func(string) (string, bool) { return "", false }
	if m, known := c.SourceMatches(Source{Type: SourceCountry, Value: "US", Not: true}, "8.8.8.8"); m || known {
		t.Errorf("unknown country: matched %v, known %v", m, known)
	}
}

func TestNormalizeSourceAndObjectInUse(t *testing.T) {
	c := newACLController(t)
	if s, err := c.NormalizeSource(Source{Type: SourceCIDR, Value: " 10.1.2.3/8 "}); err != nil || s.Value != "10.0.0.0/8" {
		t.Fatalf("CIDR not normalized: %+v %v", s, err)
	}
	for _, bad := range []Source{{Type: SourceAny}, {Type: SourceIP, Value: "nope"}, {Type: SourceObject, Value: "missing"}, {Type: "weird", Value: "x"}} {
		if _, err := c.NormalizeSource(bad); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	o, _ := c.PrepareObject(&Object{Name: "Printers", Entries: []Entry{{Type: SourceIP, Value: "10.0.0.9"}}})
	c.SaveObject(o)
	c.ExtraObjectUsers = func(id string) []string {
		if id == o.ID {
			return []string{"TLS profile exceptions"}
		}
		return nil
	}
	if err := c.DeleteObject(o.ID); err == nil || !strings.Contains(err.Error(), "TLS profile exceptions") {
		t.Fatalf("an object in use by another setting was deleted: %v", err)
	}
	c.ExtraObjectUsers = nil
	if err := c.DeleteObject(o.ID); err != nil {
		t.Fatal(err)
	}
	if c.ObjectName(o.ID) != o.ID || c.ObjectName(PrivateNetworksID) != "Private networks" {
		t.Errorf("ObjectName: %q %q", c.ObjectName(o.ID), c.ObjectName(PrivateNetworksID))
	}
}
