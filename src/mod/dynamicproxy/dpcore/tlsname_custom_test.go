package dpcore_test

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"imuslab.com/zoraxy/mod/dynamicproxy/dpcore"
)

// Custom fork: the "check certificate name against" modes. The upstream is
// reached as "localhost" and serves a certificate for either that name or the
// public host name, with verification on.
func TestTLSNameModes(t *testing.T) {
	const publicHost = "www.example.test"
	cases := []struct {
		name     string
		certName string
		mode     string
		client   string // host name the client asked for
		hostOver string // "Host" header override (TLSNameAuto uses it)
		wantOK   bool
		wantSNI  string
	}{
		{"upstream: cert for upstream name", "localhost", dpcore.TLSNameUpstream, publicHost, "", true, "localhost"},
		{"upstream: cert for public name fails", publicHost, dpcore.TLSNameUpstream, publicHost, "", false, ""},
		{"upstream: Host override is not used", "localhost", dpcore.TLSNameUpstream, publicHost, publicHost, true, "localhost"},
		{"proxyhost: cert for public name", publicHost, dpcore.TLSNameProxyHost, publicHost, "", true, publicHost},
		{"proxyhost: cert for upstream name fails", "localhost", dpcore.TLSNameProxyHost, publicHost, "", false, ""},
		{"proxyhost: other client host fails", publicHost, dpcore.TLSNameProxyHost, "other.example.test", "", false, ""},
		{"auto: upstream name", "localhost", dpcore.TLSNameAuto, publicHost, "", true, "localhost"},
		{"auto: Host override wins", publicHost, dpcore.TLSNameAuto, publicHost, publicHost, true, publicHost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			gotSNI := ""
			cert, pool := newTestCert(t, tc.certName)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			srv.TLS = &tls.Config{
				Certificates: []tls.Certificate{cert},
				GetConfigForClient: func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
					mu.Lock()
					gotSNI = chi.ServerName
					mu.Unlock()
					return nil, nil
				},
			}
			srv.StartTLS()
			defer srv.Close()
			_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
			u, _ := url.Parse("https://localhost:" + port)

			p := dpcore.NewDynamicProxyCore(u, "", &dpcore.DpcoreOptions{
				UpstreamTLSServerName: tc.hostOver,
				TLSNameMode:           tc.mode,
			})
			p.Transport.(*http.Transport).TLSClientConfig.RootCAs = pool

			// Twice: the second request must reuse the cached per-name transport
			for i := 0; i < 2; i++ {
				req := httptest.NewRequest(http.MethodGet, "http://"+tc.client+"/", nil)
				status, err := p.ProxyHTTP(httptest.NewRecorder(), req, &dpcore.ResponseRewriteRuleSet{
					UseTLS:              true,
					ProxyDomain:         u.Host,
					OriginalHost:        tc.client,
					HostHeaderOverwrite: tc.hostOver,
				})
				ok := err == nil && status == http.StatusOK
				if ok != tc.wantOK {
					t.Fatalf("request %d: ok=%v (status %d, err %v), want %v", i, ok, status, err, tc.wantOK)
				}
			}
			if tc.wantOK {
				mu.Lock()
				defer mu.Unlock()
				if gotSNI != tc.wantSNI {
					t.Fatalf("SNI %q, want %q", gotSNI, tc.wantSNI)
				}
			}
		})
	}
}
