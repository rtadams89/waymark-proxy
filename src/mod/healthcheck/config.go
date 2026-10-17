// Package healthcheck (custom fork) decides which upstreams of a load
// balanced proxy rule receive traffic.
//
// Each proxy rule can enable two independent mechanisms:
//
//   - Active checks probe every upstream on an interval (HTTP GET, HTTP HEAD,
//     TCP connect, optionally sending a payload and expecting a reply, UDP
//     payload and reply, or ICMP ping). An upstream is disabled after Fall
//     consecutive failures and re-enabled after Rise consecutive successes.
//   - Passive monitoring watches real proxied traffic. An upstream is disabled
//     when it produces Threshold counted errors (5xx responses, timeouts,
//     connection failures) within WindowSeconds. It comes back through the
//     active checks if those are enabled, otherwise after RecoverySeconds.
//
// Every enable/disable is logged with the reason.
package healthcheck

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type CheckType string

const (
	CheckHTTPGet  CheckType = "http-get"
	CheckHTTPHead CheckType = "http-head"
	CheckTCP      CheckType = "tcp"
	CheckICMP     CheckType = "icmp"
	CheckUDP      CheckType = "udp" // custom fork: stream proxies; send Send, expect a reply containing Expect
)

// Payload formats for Send and Expect.
const (
	PayloadText = "text" // with escapes \r \n \t \0 \\ and \xHH
	PayloadHex  = "hex"  // hex digits, spaces allowed
)

// Limits keep a typo from turning a health check into a flood or a no-op.
const (
	minIntervalSeconds = 2
	maxIntervalSeconds = 3600
	minTimeoutSeconds  = 1
	maxTimeoutSeconds  = 60
	maxRiseFall        = 20
	maxThreshold       = 1000
	maxWindowSeconds   = 3600
	minRecoverySeconds = 5
	maxRecoverySeconds = 3600
	maxPathLength      = 512
	maxBodyMatchLength = 256
	bodyReadLimit      = 64 << 10
	maxPayloadBytes    = 1024
	replyReadLimit     = 4096
)

// Config is stored on each proxy rule (ProxyEndpoint.HealthCheck).
type Config struct {
	// Active checks
	ActiveEnabled   bool      `json:"activeEnabled"`
	Type            CheckType `json:"type"`
	Path            string    `json:"path"`           // HTTP checks: request path, e.g. /healthz
	HostHeader      string    `json:"hostHeader"`     // HTTP checks: optional Host header for name-based backends
	ExpectedStatus  string    `json:"expectedStatus"` // HTTP checks: e.g. "200-399" or "200,204,301-302"
	IntervalSeconds int       `json:"intervalSeconds"`
	TimeoutSeconds  int       `json:"timeoutSeconds"`
	Rise            int       `json:"rise"` // consecutive successes to re-enable
	Fall            int       `json:"fall"` // consecutive failures to disable

	// Passive monitoring
	PassiveEnabled         bool `json:"passiveEnabled"`
	CountServerErrors      bool `json:"countServerErrors"`     // 5xx responses from the upstream
	CountTimeouts          bool `json:"countTimeouts"`         // upstream did not answer in time
	CountConnectionErrors  bool `json:"countConnectionErrors"` // refused, reset, unreachable, DNS failure
	Threshold              int  `json:"threshold"`             // counted errors ...
	WindowSeconds          int  `json:"windowSeconds"`         // ... within this many seconds
	PassiveRecoverySeconds int  `json:"passiveRecoverySeconds"`

	// HTTP GET: healthy only if the response body contains this text (empty = not checked)
	BodyContains string `json:"bodyContains"`

	// Also check the rule's virtual directory backends with the same settings
	IncludeVirtualDirs bool `json:"includeVirtualDirs"`

	// TCP and UDP checks (stream proxies): bytes to send after connecting,
	// and text the reply must contain. TCP: both optional (Expect alone
	// waits for a banner). UDP: Send is required; an empty Expect accepts
	// any reply.
	Send          string `json:"send,omitempty"`
	Expect        string `json:"expect,omitempty"`
	PayloadFormat string `json:"payloadFormat,omitempty"` // text (default) or hex
}

// DefaultConfig is what the UI starts from. Both mechanisms are off until
// the user enables them, so existing rules behave exactly as before.
func DefaultConfig() *Config {
	return &Config{
		ActiveEnabled:          false,
		Type:                   CheckHTTPGet,
		Path:                   "/",
		ExpectedStatus:         "200-399",
		IntervalSeconds:        10,
		TimeoutSeconds:         3,
		Rise:                   2,
		Fall:                   3,
		PassiveEnabled:         false,
		CountServerErrors:      true,
		CountTimeouts:          true,
		CountConnectionErrors:  true,
		Threshold:              5,
		WindowSeconds:          30,
		PassiveRecoverySeconds: 30,
	}
}

// Enabled reports whether this rule's upstreams are managed by this package.
func (c *Config) Enabled() bool {
	return c != nil && (c.ActiveEnabled || c.PassiveEnabled)
}

// Validate checks a config submitted from the UI. It returns a normalized
// copy, or an error describing the first problem found.
func (c *Config) Validate() (*Config, error) {
	if c == nil {
		return DefaultConfig(), nil
	}
	n := *c
	n.Path = strings.TrimSpace(n.Path)
	n.HostHeader = strings.TrimSpace(n.HostHeader)
	n.ExpectedStatus = strings.TrimSpace(n.ExpectedStatus)
	n.BodyContains = strings.TrimSpace(n.BodyContains)

	switch n.Type {
	case CheckHTTPGet, CheckHTTPHead, CheckTCP, CheckICMP, CheckUDP:
	case "":
		n.Type = CheckHTTPGet
	default:
		return nil, fmt.Errorf("unknown check type %q", n.Type)
	}

	if n.isHTTP() {
		if n.Path == "" {
			n.Path = "/"
		}
		if !strings.HasPrefix(n.Path, "/") {
			n.Path = "/" + n.Path
		}
		if len(n.Path) > maxPathLength || strings.ContainsAny(n.Path, " \r\n\t") {
			return nil, errors.New("path must be a URL path without spaces, at most 512 characters")
		}
		if strings.ContainsAny(n.HostHeader, " \r\n\t/") {
			return nil, errors.New("host header must be a plain hostname, optionally with a port")
		}
		if n.ExpectedStatus == "" {
			n.ExpectedStatus = "200-399"
		}
		if _, err := ParseStatusRanges(n.ExpectedStatus); err != nil {
			return nil, err
		}
	}
	if n.BodyContains != "" {
		if n.Type != CheckHTTPGet {
			return nil, errors.New("matching the response body needs the HTTP GET check type")
		}
		if len(n.BodyContains) > maxBodyMatchLength || strings.ContainsAny(n.BodyContains, "\r\n") {
			return nil, fmt.Errorf("body text to match must be a single line of at most %d characters", maxBodyMatchLength)
		}
	}

	if n.Type == CheckTCP || n.Type == CheckUDP {
		switch n.PayloadFormat {
		case "":
			n.PayloadFormat = PayloadText
		case PayloadText, PayloadHex:
		default:
			return nil, errors.New("payload format must be text or hex")
		}
		send, err := DecodePayload(n.Send, n.PayloadFormat)
		if err != nil {
			return nil, fmt.Errorf("data to send: %v", err)
		}
		if _, err := DecodePayload(n.Expect, n.PayloadFormat); err != nil {
			return nil, fmt.Errorf("expected reply: %v", err)
		}
		if n.Type == CheckUDP && len(send) == 0 {
			return nil, errors.New("a UDP check needs data to send (UDP has no connection to test)")
		}
	} else {
		n.Send, n.Expect, n.PayloadFormat = "", "", ""
	}

	if err := inRange("interval", n.IntervalSeconds, minIntervalSeconds, maxIntervalSeconds); err != nil {
		return nil, err
	}
	if err := inRange("timeout", n.TimeoutSeconds, minTimeoutSeconds, maxTimeoutSeconds); err != nil {
		return nil, err
	}
	if n.TimeoutSeconds >= n.IntervalSeconds {
		return nil, errors.New("timeout must be shorter than the check interval")
	}
	if err := inRange("rise", n.Rise, 1, maxRiseFall); err != nil {
		return nil, err
	}
	if err := inRange("fall", n.Fall, 1, maxRiseFall); err != nil {
		return nil, err
	}

	if n.PassiveEnabled {
		if !n.CountServerErrors && !n.CountTimeouts && !n.CountConnectionErrors {
			return nil, errors.New("passive monitoring needs at least one error type to count")
		}
	}
	if err := inRange("error threshold", n.Threshold, 1, maxThreshold); err != nil {
		return nil, err
	}
	if err := inRange("error window", n.WindowSeconds, 1, maxWindowSeconds); err != nil {
		return nil, err
	}
	if err := inRange("passive recovery time", n.PassiveRecoverySeconds, minRecoverySeconds, maxRecoverySeconds); err != nil {
		return nil, err
	}
	return &n, nil
}

func (c *Config) isHTTP() bool {
	return c.Type == CheckHTTPGet || c.Type == CheckHTTPHead
}

// Describe is a short human label for logs and the UI, e.g. "HTTP HEAD /healthz".
func (c *Config) Describe() string {
	switch c.Type {
	case CheckHTTPGet:
		return "HTTP GET " + c.Path
	case CheckHTTPHead:
		return "HTTP HEAD " + c.Path
	case CheckTCP:
		if c.Send != "" || c.Expect != "" {
			return "TCP send/expect"
		}
		return "TCP connect"
	case CheckUDP:
		return "UDP send/expect"
	case CheckICMP:
		return "ICMP ping"
	}
	return string(c.Type)
}

// DecodePayload turns Send or Expect into bytes. Text understands \r \n
// \t \0 \\ and \xHH; hex takes pairs of hex digits (spaces ignored).
func DecodePayload(s string, format string) ([]byte, error) {
	var out []byte
	if format == PayloadHex {
		clean := strings.Map(func(r rune) rune {
			if r == ' ' || r == ':' || r == '\n' || r == '\t' {
				return -1
			}
			return r
		}, s)
		if len(clean)%2 != 0 {
			return nil, errors.New("hex needs pairs of digits")
		}
		for i := 0; i < len(clean); i += 2 {
			v, err := strconv.ParseUint(clean[i:i+2], 16, 8)
			if err != nil {
				return nil, fmt.Errorf("%q is not a hex byte", clean[i:i+2])
			}
			out = append(out, byte(v))
		}
	} else {
		for i := 0; i < len(s); i++ {
			ch := s[i]
			if ch != '\\' {
				out = append(out, ch)
				continue
			}
			if i+1 >= len(s) {
				return nil, errors.New("text ends with a lone backslash")
			}
			i++
			switch s[i] {
			case 'r':
				out = append(out, '\r')
			case 'n':
				out = append(out, '\n')
			case 't':
				out = append(out, '\t')
			case '0':
				out = append(out, 0)
			case '\\':
				out = append(out, '\\')
			case 'x':
				if i+2 >= len(s) {
					return nil, errors.New("\\x needs two hex digits")
				}
				v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
				if err != nil {
					return nil, errors.New("\\x needs two hex digits")
				}
				out = append(out, byte(v))
				i += 2
			default:
				return nil, fmt.Errorf("unknown escape \\%c (use \\r \\n \\t \\0 \\\\ or \\xHH)", s[i])
			}
		}
	}
	if len(out) > maxPayloadBytes {
		return nil, fmt.Errorf("at most %d bytes", maxPayloadBytes)
	}
	return out, nil
}

func inRange(name string, v, lo, hi int) error {
	if v < lo || v > hi {
		return fmt.Errorf("%s must be between %d and %d", name, lo, hi)
	}
	return nil
}

// StatusRange is an inclusive range of HTTP status codes.
type StatusRange struct{ Lo, Hi int }

// ParseStatusRanges parses "200-399" or "200,204,301-302".
func ParseStatusRanges(s string) ([]StatusRange, error) {
	var out []StatusRange
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi := part, part
		if i := strings.IndexByte(part, '-'); i >= 0 {
			lo, hi = strings.TrimSpace(part[:i]), strings.TrimSpace(part[i+1:])
		}
		l, err1 := strconv.Atoi(lo)
		h, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil || l < 100 || h > 599 || l > h {
			return nil, fmt.Errorf("invalid expected status %q (use codes 100-599, e.g. 200-399 or 200,204)", part)
		}
		out = append(out, StatusRange{l, h})
	}
	if len(out) == 0 {
		return nil, errors.New("expected status must list at least one code")
	}
	return out, nil
}

func statusMatches(code int, ranges []StatusRange) bool {
	for _, r := range ranges {
		if code >= r.Lo && code <= r.Hi {
			return true
		}
	}
	return false
}
