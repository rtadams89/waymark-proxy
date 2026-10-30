package dynamicproxy

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"imuslab.com/zoraxy/mod/access"
	"imuslab.com/zoraxy/mod/database"
	"imuslab.com/zoraxy/mod/info/logger"
)

// rlTrust: whether each test's proxy rule trusts forwarded addresses
var rlTrust = map[string]bool{}

func newTestRateLimitRouter(t *testing.T, trustProxyHeaders bool, trustedProxies ...string) *Router {
	t.Helper()
	tmpDir := t.TempDir()

	sysdb, err := database.NewDatabase(filepath.Join(tmpDir, "sysdb"), database.GetRecommendedBackendType())
	if err != nil {
		t.Fatalf("unable to create test database: %v", err)
	}
	t.Cleanup(func() { sysdb.Close() })

	testLogger, err := logger.NewFmtLogger()
	if err != nil {
		t.Fatalf("unable to create test logger: %v", err)
	}

	accessController, err := access.NewAccessController(&access.Options{
		Logger:             *testLogger,
		ConfigFolder:       filepath.Join(tmpDir, "acl"),
		TrustedProxiesFile: filepath.Join(tmpDir, "trusted_proxies.json"),
		Database:           sysdb,
	})
	if err != nil {
		t.Fatalf("unable to create access controller: %v", err)
	}

	rlTrust[t.Name()] = trustProxyHeaders // the proxy rule's "trust forwarded addresses"
	for _, proxy := range trustedProxies {
		accessController.AddTrustedProxy(proxy, "test")
	}

	return &Router{
		Option:           &RouterOption{AccessController: accessController},
		rateLimitCounter: RequestCountPerIpTable{},
	}
}

func newTestRequest(remoteAddr string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "https://host.example.com/", nil)
	r.RemoteAddr = remoteAddr
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// A header-supplied client IP without a port is still counted.
func TestHandleRateLimit_BareHeaderIPStillCounts(t *testing.T) {
	for _, headerName := range []string{"CF-Connecting-IP", "Fastly-Client-IP", "X-Real-Ip", "X-Forwarded-For"} {
		t.Run(headerName, func(t *testing.T) {
			router := newTestRateLimitRouter(t, true, "104.16.0.0/13")
			pe := &ProxyEndpoint{RequireRateLimit: true, RateLimit: 3, TrustProxyHeaders: rlTrust[t.Name()]}

			blocked := 0
			for i := 0; i < 10; i++ {
				w := httptest.NewRecorder()
				r := newTestRequest("104.16.5.1:443", map[string]string{headerName: "203.0.113.99"})
				if err := router.handleRateLimit(w, r, pe); err != nil {
					blocked++
					if w.Code != 429 {
						t.Errorf("expected status 429 on block, got %d", w.Code)
					}
				}
			}

			if blocked == 0 {
				t.Fatalf("%s: rate limit never fired, limiter failed open", headerName)
			}
			if got := router.rateLimitCounter.GetCount("203.0.113.99"); got != 10 {
				t.Errorf("%s: expected 10 requests counted against 203.0.113.99, got %d", headerName, got)
			}
		})
	}
}

// Each visitor behind the CDN gets their own counter.
func TestHandleRateLimit_CountsPerRealClient(t *testing.T) {
	router := newTestRateLimitRouter(t, true, "104.16.0.0/13")
	pe := &ProxyEndpoint{RequireRateLimit: true, RateLimit: 3, TrustProxyHeaders: rlTrust[t.Name()]}

	// A limit of 3 lets 2 requests through before the third is rejected
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		r := newTestRequest("104.16.5.1:443", map[string]string{"CF-Connecting-IP": "203.0.113.1"})
		if err := router.handleRateLimit(w, r, pe); err != nil {
			t.Fatalf("first client blocked too early at request %d", i)
		}
	}

	//A different client through the same edge node must not be affected
	w := httptest.NewRecorder()
	r := newTestRequest("104.16.5.1:443", map[string]string{"CF-Connecting-IP": "203.0.113.2"})
	if err := router.handleRateLimit(w, r, pe); err != nil {
		t.Fatalf("second client should not share the first client's counter")
	}
}

// A spoofed header from an untrusted peer is ignored by the limiter.
func TestHandleRateLimit_SpoofedHeaderFromUntrustedPeerIgnored(t *testing.T) {
	router := newTestRateLimitRouter(t, true) //no trusted proxies configured
	pe := &ProxyEndpoint{RequireRateLimit: true, RateLimit: 3, TrustProxyHeaders: rlTrust[t.Name()]}

	blocked := 0
	for i := 0; i < 10; i++ {
		w := httptest.NewRecorder()
		//Every request claims a different origin IP
		r := newTestRequest("198.51.100.7:51000", map[string]string{
			"CF-Connecting-IP": "203.0.113." + string(rune('0'+i)),
		})
		if err := router.handleRateLimit(w, r, pe); err != nil {
			blocked++
		}
	}

	if blocked == 0 {
		t.Fatal("spoofed CF-Connecting-IP allowed a client to bypass the rate limit")
	}
	if got := router.rateLimitCounter.GetCount("198.51.100.7"); got != 10 {
		t.Errorf("expected all 10 requests counted against the real peer, got %d", got)
	}
}

// The direct peer is counted when no proxy header is involved.
func TestHandleRateLimit_DirectConnection(t *testing.T) {
	router := newTestRateLimitRouter(t, false)
	pe := &ProxyEndpoint{RequireRateLimit: true, RateLimit: 3, TrustProxyHeaders: rlTrust[t.Name()]}

	blocked := 0
	for i := 0; i < 10; i++ {
		w := httptest.NewRecorder()
		r := newTestRequest("198.51.100.20:44321", nil)
		if err := router.handleRateLimit(w, r, pe); err != nil {
			blocked++
		}
	}

	if blocked != 8 {
		t.Errorf("expected 8 of 10 requests blocked at limit 3, got %d", blocked)
	}
}
