package dynamicproxy

/*
	maintenance_custom.go (custom fork)

	Per-rule maintenance mode. While on, every request to the rule
	(including its virtual directories) gets a 503 maintenance page with an
	optional message and Retry-After, before health checks, the cache or
	any upstream is involved. Health checks keep running, so the rule's
	health is visible when maintenance ends.

	The page can be replaced by <webroot>/templates/maintenance.html.
*/

import (
	_ "embed"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/maintenance.html
var page_maintenance []byte

// MaintenanceConfig is stored on the rule (ProxyEndpoint.Maintenance).
type MaintenanceConfig struct {
	Enabled           bool   `json:"enabled"`
	Message           string `json:"message"`
	RetryAfterMinutes int    `json:"retryAfterMinutes"` // 0 = not announced
	Since             int64  `json:"since"`             // unix time maintenance was turned on
}

// DefaultMaintenanceMessage is shown when no message is set.
const DefaultMaintenanceMessage = "This site is down for planned maintenance and will be back shortly."

// On reports whether maintenance mode is on (nil-safe).
func (m *MaintenanceConfig) On() bool {
	return m != nil && m.Enabled
}

// RenderMaintenancePage builds the maintenance page for a rule.
func (router *Router) RenderMaintenancePage(host string, m *MaintenanceConfig) []byte {
	tpl := page_maintenance
	if router != nil && router.Option != nil && router.Option.WebDirectory != "" {
		if custom, err := os.ReadFile(filepath.Join(router.Option.WebDirectory, "templates", "maintenance.html")); err == nil {
			tpl = custom
		}
	}
	message := DefaultMaintenanceMessage
	retry := ""
	if m != nil {
		if strings.TrimSpace(m.Message) != "" {
			message = m.Message
		}
		if m.RetryAfterMinutes > 0 {
			retry = "Expected back " + backIn(m) + "."
		}
	}
	escaped := strings.ReplaceAll(html.EscapeString(message), "\n", "<br>")
	out := strings.NewReplacer(
		"{{message}}", escaped,
		"{{retry}}", html.EscapeString(retry),
		"{{host}}", html.EscapeString(host),
	).Replace(string(tpl))
	return []byte(out)
}

// backIn describes when the rule is expected back, from Since + RetryAfterMinutes.
func backIn(m *MaintenanceConfig) string {
	if m.Since == 0 {
		return "in about " + minutesText(m.RetryAfterMinutes)
	}
	left := time.Until(time.Unix(m.Since, 0).Add(time.Duration(m.RetryAfterMinutes) * time.Minute))
	if left <= time.Minute {
		return "any minute now"
	}
	return "in about " + minutesText(int((left+time.Minute-1)/time.Minute))
}

func minutesText(n int) string {
	if n >= 120 && n%60 == 0 {
		return strconv.Itoa(n/60) + " hours"
	}
	if n == 60 {
		return "an hour"
	}
	if n == 1 {
		return "a minute"
	}
	return strconv.Itoa(n) + " minutes"
}

// retryAfterSeconds is the remaining announced time, at least 60 seconds.
func retryAfterSeconds(m *MaintenanceConfig) int {
	if m.RetryAfterMinutes <= 0 {
		return 0
	}
	end := time.Unix(m.Since, 0).Add(time.Duration(m.RetryAfterMinutes) * time.Minute)
	left := int(time.Until(end).Seconds())
	if m.Since == 0 {
		left = m.RetryAfterMinutes * 60
	}
	if left < 60 {
		left = 60
	}
	return left
}

// serveMaintenance answers the request with the maintenance page if the
// rule is in maintenance mode.
func (router *Router) serveMaintenance(w http.ResponseWriter, r *http.Request, ep *ProxyEndpoint) bool {
	if ep == nil || !ep.Maintenance.On() {
		return false
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	if s := retryAfterSeconds(ep.Maintenance); s > 0 {
		h.Set("Retry-After", strconv.Itoa(s))
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	if r.Method != http.MethodHead {
		w.Write(router.RenderMaintenancePage(r.Host, ep.Maintenance))
	}
	router.logRequest(r, false, http.StatusServiceUnavailable, "host-http", r.Host, "maintenance", ep)
	return true
}
