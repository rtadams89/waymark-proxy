package dynamicproxy

import (
	"crypto/tls"
	"testing"
)

// Changing a listener setting must not start a stopped proxy (it used to:
// the setters called Restart, which starts the proxy when it is stopped).
func TestListenerSettingsKeepStoppedProxyStopped(t *testing.T) {
	// With a root set, a call to Restart would get as far as starting the
	// listeners (on a random port) or fail the test on the missing parts
	r := &Router{Option: &RouterOption{Port: 0}, Root: &ProxyEndpoint{}}
	r.UpdateTLSSetting(true)
	r.SetTlsMinVersion(tls.VersionTLS13)
	r.UpdatePort80ListenerState(true)
	r.UpdateHttpToHttpsRedirectSetting(true)
	if r.Running || r.server != nil || r.tlsRedirectStop != nil {
		t.Fatal("a listener setting started the stopped proxy")
	}
	if !r.Option.UseTls || r.Option.MinTLSVersion != tls.VersionTLS13 || !r.Option.ListenOnPort80 || !r.Option.ForceHttpsRedirect {
		t.Fatal("the settings were not stored")
	}
}
