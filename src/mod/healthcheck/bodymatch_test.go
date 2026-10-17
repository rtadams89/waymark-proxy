package healthcheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeBodyContains(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthy":
			w.Write([]byte(`{"status":"ok","db":"up"}`))
		case "/degraded":
			w.Write([]byte(`{"status":"error","db":"down"}`)) // still 200
		case "/big":
			w.Write([]byte(strings.Repeat("x", 70<<10) + "ok")) // beyond the read limit
		default:
			w.WriteHeader(503)
			w.Write([]byte(`"db":"up"`))
		}
	}))
	defer srv.Close()
	up := Upstream{Addr: strings.TrimPrefix(srv.URL, "http://")}
	cfg := DefaultConfig()
	cfg.TimeoutSeconds = 1
	cfg.BodyContains = `"db":"up"`

	cfg.Path = "/healthy"
	if r := DefaultProber(context.Background(), up, cfg); !r.OK || r.Detail != `status 200, contains "\"db\":\"up\""` {
		t.Errorf("healthy: %+v", r)
	}
	cfg.Path = "/degraded"
	if r := DefaultProber(context.Background(), up, cfg); r.OK || !strings.Contains(r.Detail, "status 200, but the response does not contain") {
		t.Errorf("200 with an error page must fail: %+v", r)
	}
	cfg.Path = "/broken"
	if r := DefaultProber(context.Background(), up, cfg); r.OK || r.Detail != "status 503 (expected 200-399)" {
		t.Errorf("status checked first: %+v", r)
	}
	cfg.Path, cfg.BodyContains = "/big", "ok"
	if r := DefaultProber(context.Background(), up, cfg); r.OK {
		t.Errorf("text beyond the first 64 KB must not count: %+v", r)
	}
}

func TestValidateBodyContains(t *testing.T) {
	c := DefaultConfig()
	c.BodyContains = "  ok  "
	v, err := c.Validate()
	if err != nil || v.BodyContains != "ok" {
		t.Fatalf("trim: %v %v", v, err)
	}
	for name, mod := range map[string]func(*Config){
		"HEAD":      func(c *Config) { c.Type, c.BodyContains = CheckHTTPHead, "ok" },
		"TCP":       func(c *Config) { c.Type, c.BodyContains = CheckTCP, "ok" },
		"multiline": func(c *Config) { c.BodyContains = "a\nb" },
		"too long":  func(c *Config) { c.BodyContains = strings.Repeat("a", 257) },
	} {
		c := DefaultConfig()
		mod(c)
		if _, err := c.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
