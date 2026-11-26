package websocketproxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestSkipTLSValidationLeavesDefaultDialerVerifying(t *testing.T) {
	upgrader := websocket.Upgrader{}
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			c.Close()
		}
	}))
	defer backend.Close()
	u, _ := url.Parse(strings.Replace(backend.URL, "https://", "wss://", 1))

	proxy := httptest.NewServer(NewProxy(u, Options{SkipTLSValidation: true}))
	defer proxy.Close()
	conn, _, err := websocket.DefaultDialer.Dial(strings.Replace(proxy.URL, "http://", "ws://", 1), nil)
	if err != nil {
		t.Fatalf("skip-verify connection failed: %v", err)
	}
	conn.Close()

	if cfg := websocket.DefaultDialer.TLSClientConfig; cfg != nil && cfg.InsecureSkipVerify {
		t.Fatal("the shared default dialer was changed to skip certificate checks")
	}
}
