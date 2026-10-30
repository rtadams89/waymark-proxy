package logger

// Where the two log streams go: system (Waymark's own messages, also always
// printed to standard output) and traffic (one line per HTTP request). Each
// stream can be written to the log file, sent to remote syslog servers, both,
// or neither. Settings live in conf/log_outputs.json.

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"imuslab.com/zoraxy/mod/utils"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Stream names.
const (
	StreamSystem  = "system"
	StreamTraffic = "traffic"
)

// MaxSyslogDestinations is how many syslog servers can be configured.
const MaxSyslogDestinations = 8

// LogOutputs chooses where each log stream goes.
type LogOutputs struct {
	SystemToFile  bool                `json:"systemToFile"`
	TrafficToFile bool                `json:"trafficToFile"`
	Syslog        []SyslogDestination `json:"syslog"`
}

// SyslogDestination is one remote syslog server.
type SyslogDestination struct {
	Name     string `json:"name"`     // label shown in the UI
	Enabled  bool   `json:"enabled"`  // off: kept but not used
	Host     string `json:"host"`     // IP address or host name
	Port     int    `json:"port"`     // 1-65535
	Protocol string `json:"protocol"` // udp, tcp, tls
	Framing  string `json:"framing"`  // tcp and tls: octet (RFC 6587 octet counting) or newline
	Format   string `json:"format"`   // rfc5424, rfc3164, json5424 (RFC 5424 header, JSON message), json (JSON lines, no header)
	Facility string `json:"facility"` // kern, user, daemon, auth, syslog, local0-local7, ...
	System   bool   `json:"system"`   // send system messages
	Traffic  bool   `json:"traffic"`  // send traffic (HTTP request) lines

	// TLS only
	TLSVerify bool   `json:"tlsVerify"` // check the server certificate (default on)
	TLSCA     string `json:"tlsCa"`     // PEM CA certificate(s); empty: the system's trusted CAs
	TLSName   string `json:"tlsName"`   // name to check the certificate for; empty: Host
}

// DefaultLogOutputs writes both streams to the file and sends nothing to syslog.
func DefaultLogOutputs() *LogOutputs {
	return &LogOutputs{SystemToFile: true, TrafficToFile: true, Syslog: []SyslogDestination{}}
}

// Facility codes (RFC 5424 section 6.2.1).
var syslogFacilities = map[string]int{
	"kern": 0, "user": 1, "mail": 2, "daemon": 3, "auth": 4, "syslog": 5, "lpr": 6, "news": 7,
	"uucp": 8, "cron": 9, "authpriv": 10, "ftp": 11,
	"local0": 16, "local1": 17, "local2": 18, "local3": 19, "local4": 20, "local5": 21, "local6": 22, "local7": 23,
}

var (
	syslogHostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?)*\.?$`)
	syslogNamePattern = regexp.MustCompile(`^[A-Za-z0-9 ._()/+-]{0,32}$`)
)

// Validate checks the settings and fills in defaults. Returns an error
// naming the first problem.
func (o *LogOutputs) Validate() error {
	if o.Syslog == nil {
		o.Syslog = []SyslogDestination{}
	}
	if len(o.Syslog) > MaxSyslogDestinations {
		return fmt.Errorf("at most %d syslog servers", MaxSyslogDestinations)
	}
	for i := range o.Syslog {
		d := &o.Syslog[i]
		label := fmt.Sprintf("syslog server %d", i+1)
		if err := d.validate(); err != nil {
			if d.Name != "" {
				label = "syslog server \"" + d.Name + "\""
			}
			return fmt.Errorf("%s: %v", label, err)
		}
	}
	return nil
}

func (d *SyslogDestination) validate() error {
	d.Name = strings.TrimSpace(d.Name)
	d.Host = strings.TrimSpace(d.Host)
	d.TLSName = strings.TrimSpace(d.TLSName)
	d.TLSCA = strings.TrimSpace(d.TLSCA)
	if !syslogNamePattern.MatchString(d.Name) {
		return errors.New("name: up to 32 letters, digits, spaces and . _ ( ) / + -")
	}
	if d.Host == "" {
		return errors.New("enter the server's IP address or host name")
	}
	if net.ParseIP(d.Host) == nil && (len(d.Host) > 253 || !syslogHostPattern.MatchString(d.Host)) {
		return errors.New("host must be an IP address or a host name")
	}
	d.Protocol = strings.ToLower(d.Protocol)
	switch d.Protocol {
	case "udp", "tcp", "tls":
	default:
		return errors.New("protocol must be UDP, TCP or TLS")
	}
	if d.Port == 0 {
		d.Port = map[string]int{"udp": 514, "tcp": 514, "tls": 6514}[d.Protocol]
	}
	if d.Port < 1 || d.Port > 65535 {
		return errors.New("port must be 1-65535")
	}
	switch d.Framing {
	case "":
		d.Framing = "newline"
		if d.Protocol == "tls" {
			d.Framing = "octet" // RFC 5425
		}
	case "octet", "newline":
	default:
		return errors.New("framing must be octet counting or newline")
	}
	switch d.Format {
	case "":
		d.Format = "rfc5424"
	case "rfc5424", "rfc3164", "json5424", "json":
	default:
		return errors.New("unknown message format")
	}
	if d.Facility == "" {
		d.Facility = "local0"
	}
	if _, ok := syslogFacilities[d.Facility]; !ok {
		return errors.New("unknown facility")
	}
	if d.TLSName != "" && (len(d.TLSName) > 253 || !syslogHostPattern.MatchString(d.TLSName)) && net.ParseIP(d.TLSName) == nil {
		return errors.New("certificate name must be a host name or IP address")
	}
	if d.TLSCA != "" {
		if len(d.TLSCA) > 64<<10 {
			return errors.New("CA certificate is too large")
		}
		if !x509.NewCertPool().AppendCertsFromPEM([]byte(d.TLSCA)) {
			return errors.New("CA certificate must be in PEM format (-----BEGIN CERTIFICATE-----)")
		}
	}
	if d.Protocol != "tls" {
		d.TLSVerify, d.TLSCA, d.TLSName = false, "", ""
	}
	return nil
}

// Address is host:port.
func (d *SyslogDestination) Address() string {
	return net.JoinHostPort(d.Host, fmt.Sprint(d.Port))
}

// LoadLogOutputs reads path; a missing file gives the defaults.
func LoadLogOutputs(path string) (*LogOutputs, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultLogOutputs(), nil
	}
	if err != nil {
		return DefaultLogOutputs(), err
	}
	o := DefaultLogOutputs()
	if err := json.Unmarshal(b, o); err != nil {
		return DefaultLogOutputs(), fmt.Errorf("%s is unreadable: %v", path, err)
	}
	if err := o.Validate(); err != nil {
		return DefaultLogOutputs(), fmt.Errorf("%s: %v", path, err)
	}
	return o, nil
}

// SaveLogOutputs writes path (through a temporary file).
func SaveLogOutputs(path string, o *LogOutputs) error {
	b, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0775); err != nil {
		return err
	}
	return utils.WriteFileAtomic(path, b, 0640)
}
