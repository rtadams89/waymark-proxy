package rewrite

import (
	"encoding/json"

	"imuslab.com/zoraxy/mod/dynamicproxy/permissionpolicy"
)

// Header injection direction type
type HeaderDirection int

const (
	HeaderDirection_ZoraxyToUpstream   HeaderDirection = 0 // Set or remove the header on requests to the backend
	HeaderDirection_ZoraxyToDownstream HeaderDirection = 1 // Set or remove the header on responses to the client
)

// User defined headers to add into a proxy endpoint
type UserDefinedHeader struct {
	Direction HeaderDirection
	Key       string
	Value     string
	IsRemove  bool // Remove this key instead of setting it
}

type HeaderRewriteOptions struct {
	UserDefinedHeaders           []*UserDefinedHeader                //Custom headers to append when proxying requests from this endpoint
	HSTSMaxAge                   int64                               // HSTS max age, 0 disables HSTS headers
	HSTSIncludeSubdomains        bool                                //Include subdomains in HSTS header
	EnablePermissionPolicyHeader bool                                //Enable injection of permission policy header
	PermissionPolicy             *permissionpolicy.PermissionsPolicy //Permission policy header
}

func (h *UserDefinedHeader) GetDirection() HeaderDirection {
	return h.Direction
}

// Copy returns a deep copy of the UserDefinedHeader
func (h *UserDefinedHeader) Copy() *UserDefinedHeader {
	result := UserDefinedHeader{}
	js, _ := json.Marshal(h)
	json.Unmarshal(js, &result)
	return &result
}
