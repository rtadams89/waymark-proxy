package acmedns_test

import (
	"fmt"
	"testing"

	"imuslab.com/zoraxy/mod/acme/acmedns"
)

func TestACMEDNSConfigStructureReflector(t *testing.T) {
	providers := []string{
		"gandi",
		"cloudflare",
		"azuredns",
	}

	for _, provider := range providers {
		structure, err := acmedns.GetProviderConfigStructure(provider)
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}

		fmt.Println(structure)
	}
}
