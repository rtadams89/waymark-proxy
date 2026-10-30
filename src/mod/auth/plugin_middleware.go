// Handles the API-Key based authentication for plugins

package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	PLUGIN_API_PREFIX = "/plugin"
)

type PluginMiddlewareOptions struct {
	DeniedHandler http.HandlerFunc //Thing(s) to do when request is rejected
	ApiKeyManager *APIKeyManager
	TargetMux     *http.ServeMux
}

// PluginAuthMiddleware provides authentication middleware for plugin API requests
type PluginAuthMiddleware struct {
	option    PluginMiddlewareOptions
	endpoints map[string]http.HandlerFunc
}

// NewPluginAuthMiddleware creates a new plugin authentication middleware
func NewPluginAuthMiddleware(option PluginMiddlewareOptions) *PluginAuthMiddleware {
	return &PluginAuthMiddleware{
		option:    option,
		endpoints: make(map[string]http.HandlerFunc),
	}
}

func (m *PluginAuthMiddleware) HandleAuthCheck(w http.ResponseWriter, r *http.Request, handler http.HandlerFunc) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		m.option.DeniedHandler(w, r)
		return
	}

	if !strings.HasPrefix(authHeader, "Bearer ") {
		m.option.DeniedHandler(w, r)
		return
	}

	apiKey := strings.TrimPrefix(authHeader, "Bearer ")

	_, err := m.option.ApiKeyManager.ValidateAPIKeyForEndpoint(r.URL.Path, r.Method, apiKey)
	if err != nil {
		m.option.DeniedHandler(w, r)
		return
	}

	handler(w, r)
}

// HandleFunc registers handler under PLUGIN_API_PREFIX with plugin API key
// authentication.
func (m *PluginAuthMiddleware) HandleFunc(endpoint string, handler http.HandlerFunc) error {
	if !strings.HasPrefix(endpoint, PLUGIN_API_PREFIX) {
		endpoint = PLUGIN_API_PREFIX + endpoint
	}

	if _, exist := m.endpoints[endpoint]; exist {
		fmt.Println("WARNING! Duplicated registering of plugin api endpoint: " + endpoint)
		return errors.New("endpoint register duplicated")
	}

	m.endpoints[endpoint] = handler

	wrappedHandler := func(w http.ResponseWriter, r *http.Request) {
		m.HandleAuthCheck(w, r, handler)
	}

	if m.option.TargetMux == nil {
		http.HandleFunc(endpoint, wrappedHandler)
	} else {
		m.option.TargetMux.HandleFunc(endpoint, wrappedHandler)
	}

	return nil
}
