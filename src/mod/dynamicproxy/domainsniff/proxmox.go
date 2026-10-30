package domainsniff

import "net/http"

// IsProxmox reports whether the request carries a Proxmox VE auth cookie.

func IsProxmox(r *http.Request) bool {
	for _, cookie := range r.Cookies() {
		if cookie.Name == "PVEAuthCookie" {
			return true
		}
	}

	return false
}
