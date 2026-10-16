package geodb

import (
	"testing"
)

func TestSetEnabled(t *testing.T) {
	store, err := NewGeoDb(nil, &StoreOptions{AllowSlowIpv4LookUp: false, AllowSlowIpv6Lookup: false, ExternalGeoDBFolder: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !store.Enabled() {
		t.Fatal("enabled by default")
	}
	if cc, _ := store.ResolveCountryCodeFromIP("8.8.8.8"); cc.CountryIsoCode != "US" {
		t.Fatalf("lookup while on: %q", cc.CountryIsoCode)
	}
	store.SetEnabled(false)
	if store.Enabled() || store.dataset.Load() != nil {
		t.Fatal("dataset still loaded after turning off")
	}
	if cc, err := store.ResolveCountryCodeFromIP("8.8.8.8"); err != nil || cc.CountryIsoCode != "" {
		t.Fatalf("lookup while off: %q %v", cc.CountryIsoCode, err)
	}
	if err := store.ReloadGeoDB(); err != nil || store.dataset.Load() != nil {
		t.Fatal("reload must not load the dataset while off")
	}
	if st := store.GetUpdaterStatus(); st.IPv4EntryCount != 0 {
		t.Fatalf("status while off: %+v", st)
	}
	if err := store.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if cc, _ := store.ResolveCountryCodeFromIP("8.8.8.8"); cc.CountryIsoCode != "US" {
		t.Fatalf("lookup after turning on: %q", cc.CountryIsoCode)
	}
}

func TestStartDisabled(t *testing.T) {
	store, err := NewGeoDb(nil, &StoreOptions{Disabled: true, ExternalGeoDBFolder: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.Enabled() || store.dataset.Load() != nil {
		t.Fatal("dataset loaded although started disabled")
	}
	if cc, _ := store.ResolveCountryCodeFromIP("1.1.1.1"); cc.CountryIsoCode != "" {
		t.Fatalf("lookup: %q", cc.CountryIsoCode)
	}
}
