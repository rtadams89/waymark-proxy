package acmedns_test

import (
	"fmt"
	"testing"

	"github.com/rtadams89/waymark-proxy/mod/acme/acmedns"
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
