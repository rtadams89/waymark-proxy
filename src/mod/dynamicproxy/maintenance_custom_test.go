package dynamicproxy

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMaintenancePage(t *testing.T) {
	router := &Router{Option: &RouterOption{WebDirectory: t.TempDir()}}
	m := &MaintenanceConfig{Enabled: true, Message: "Upgrading <Proxmox>\nBack soon", RetryAfterMinutes: 30, Since: time.Now().Unix()}
	page := string(router.RenderMaintenancePage("pve.home.test", m))
	for _, want := range []string{"Upgrading &lt;Proxmox&gt;<br>Back soon", "Expected back in about 30 minutes.", "pve.home.test", "Down for maintenance"} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(page, "<Proxmox>") {
		t.Error("message not escaped")
	}
	// Default message, no retry
	page = string(router.RenderMaintenancePage("h", &MaintenanceConfig{Enabled: true}))
	if !strings.Contains(page, DefaultMaintenanceMessage) || strings.Contains(page, "Expected back") {
		t.Error("default message / no retry text")
	}
	// Custom template
	os.MkdirAll(filepath.Join(router.Option.WebDirectory, "templates"), 0755)
	os.WriteFile(filepath.Join(router.Option.WebDirectory, "templates", "maintenance.html"), []byte("CUSTOM {{host}}: {{message}}"), 0644)
	if got := string(router.RenderMaintenancePage("x.test", m)); got != "CUSTOM x.test: Upgrading &lt;Proxmox&gt;<br>Back soon" {
		t.Errorf("custom template: %q", got)
	}
}

func TestRetryAfter(t *testing.T) {
	if s := retryAfterSeconds(&MaintenanceConfig{RetryAfterMinutes: 0}); s != 0 {
		t.Errorf("no retry: %d", s)
	}
	m := &MaintenanceConfig{RetryAfterMinutes: 10, Since: time.Now().Add(-4 * time.Minute).Unix()}
	if s := retryAfterSeconds(m); s < 355 || s > 361 {
		t.Errorf("remaining time: %d", s)
	}
	m.Since = time.Now().Add(-2 * time.Hour).Unix()
	if s := retryAfterSeconds(m); s != 60 {
		t.Errorf("overdue floors at 60s: %d", s)
	}
	if backIn(m) != "any minute now" {
		t.Errorf("overdue text: %s", backIn(m))
	}
}

func TestServeMaintenance(t *testing.T) {
	router := &Router{Option: &RouterOption{}}
	ep := &ProxyEndpoint{RootOrMatchingDomain: "a.test", DisableLogging: true, DisableStatisticCollection: true}
	w := httptest.NewRecorder()
	if router.serveMaintenance(w, httptest.NewRequest("GET", "http://a.test/", nil), ep) {
		t.Fatal("served while off")
	}
	ep.Maintenance = &MaintenanceConfig{Enabled: true, RetryAfterMinutes: 5, Since: time.Now().Unix()}
	if !router.serveMaintenance(w, httptest.NewRequest("GET", "http://a.test/x", nil), ep) {
		t.Fatal("not served while on")
	}
	if w.Code != 503 || w.Header().Get("Retry-After") == "" || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "a.test") {
		t.Errorf("response: %d %v", w.Code, w.Header())
	}
}
