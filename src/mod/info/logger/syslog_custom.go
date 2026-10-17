package logger

/*
	syslog_custom.go (custom fork)

	Sends log messages to remote syslog servers (see LogOutputs).

	Each enabled server has its own queue and connection, so a slow or
	unreachable server never holds up the proxy or the other servers:
	messages are queued (up to syslogQueueSize per server) and dropped,
	and counted, when the queue is full. TCP and TLS connections are
	re-established with a growing delay (1 to 30 seconds) after a failure,
	and the message being sent is retried once on the new connection.

	Formats:
	- rfc5424:  <PRI>1 TIMESTAMP HOST waymark PID MSGID - MESSAGE
	- rfc3164:  <PRI>Mmm dd hh:mm:ss HOST waymark[PID]: MESSAGE
	- json5424: an RFC 5424 header followed by the message as a JSON object
	- json:     the JSON object alone (for collectors that take JSON lines)
	MSGID is the stream ("system" or "traffic"). MESSAGE is the same text
	as in the log file, without the timestamp.
*/

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	syslogQueueSize    = 4096
	syslogDialTimeout  = 5 * time.Second
	syslogWriteTimeout = 5 * time.Second
	syslogMaxMessage   = 64 << 10
	syslogAppName      = "waymark"
)

// Severities used (RFC 5424 section 6.2.1).
const (
	SeverityError = 3
	SeverityInfo  = 6
)

// LogEvent is one log message, with its fields for the JSON formats.
type LogEvent struct {
	Stream   string
	Time     time.Time
	Severity int
	Text     string // the log file line without its timestamp

	// system
	Module  string
	Message string
	Error   string

	// traffic
	Class     string
	Origin    string
	Client    string
	UserAgent string
	Method    string
	URI       string
	Status    int
	Upstream  string
}

var (
	syslogHostname = func() string {
		h, err := os.Hostname()
		if err != nil || h == "" {
			return "-"
		}
		return h
	}()
	syslogPID = strconv.Itoa(os.Getpid())
)

func (e *LogEvent) jsonFields(host string) map[string]any {
	m := map[string]any{
		"time":     e.Time.Format(time.RFC3339Nano),
		"host":     host,
		"app":      syslogAppName,
		"stream":   e.Stream,
		"severity": map[int]string{SeverityError: "error", SeverityInfo: "info"}[e.Severity],
	}
	if e.Stream == StreamTraffic {
		m["class"], m["origin"], m["client"], m["userAgent"] = e.Class, e.Origin, e.Client, e.UserAgent
		m["method"], m["uri"], m["status"] = e.Method, e.URI, e.Status
		if e.Upstream != "" {
			m["upstream"] = e.Upstream
		}
	} else {
		m["module"], m["message"] = e.Module, e.Message
		if e.Error != "" {
			m["error"] = e.Error
		}
	}
	return m
}

// oneLine replaces line breaks and other control characters with spaces.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// formatSyslog builds the message for d (without framing).
func formatSyslog(d *SyslogDestination, e *LogEvent, host string) []byte {
	pri := "<" + strconv.Itoa(syslogFacilities[d.Facility]*8+e.Severity) + ">"
	var out string
	switch d.Format {
	case "rfc3164":
		// BSD syslog: local time, no year
		out = pri + e.Time.Format(time.Stamp) + " " + host + " " + syslogAppName + "[" + syslogPID + "]: " + oneLine(e.Text)
	case "json":
		b, _ := json.Marshal(e.jsonFields(host))
		out = string(b)
	case "json5424":
		b, _ := json.Marshal(e.jsonFields(host))
		out = pri + "1 " + e.Time.Format("2006-01-02T15:04:05.000000Z07:00") + " " + host + " " + syslogAppName + " " + syslogPID + " " + e.Stream + " - " + string(b)
	default: // rfc5424
		out = pri + "1 " + e.Time.Format("2006-01-02T15:04:05.000000Z07:00") + " " + host + " " + syslogAppName + " " + syslogPID + " " + e.Stream + " - " + oneLine(e.Text)
	}
	if len(out) > syslogMaxMessage {
		out = out[:syslogMaxMessage]
	}
	return []byte(out)
}

// frame adds TCP/TLS framing (RFC 6587): octet counting or a newline.
func frame(d *SyslogDestination, msg []byte) []byte {
	if d.Protocol == "udp" {
		return msg
	}
	if d.Framing == "octet" {
		return append([]byte(strconv.Itoa(len(msg))+" "), msg...)
	}
	return append(msg, '\n')
}

// dialSyslog connects to d.
func dialSyslog(d *SyslogDestination) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: syslogDialTimeout, KeepAlive: 30 * time.Second}
	switch d.Protocol {
	case "udp":
		return dialer.Dial("udp", d.Address())
	case "tcp":
		return dialer.Dial("tcp", d.Address())
	case "tls":
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: d.Host}
		if d.TLSName != "" {
			cfg.ServerName = d.TLSName
		}
		if d.TLSCA != "" {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM([]byte(d.TLSCA))
			cfg.RootCAs = pool
		}
		if !d.TLSVerify {
			cfg.InsecureSkipVerify = true //Chosen in the settings for servers with self-signed certificates
		}
		conn, err := tls.DialWithDialer(dialer, "tcp", d.Address(), cfg)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
	return nil, errors.New("unknown protocol")
}

// syslogSink delivers messages to one server.
type syslogSink struct {
	dest  SyslogDestination
	queue chan *LogEvent
	stop  chan struct{}
	done  chan struct{}

	mu       sync.Mutex
	state    string // connecting, connected, error
	lastErr  string
	errTime  int64
	lastSent int64
	sent     atomic.Uint64
	dropped  atomic.Uint64
}

func newSyslogSink(d SyslogDestination) *syslogSink {
	s := &syslogSink{dest: d, queue: make(chan *LogEvent, syslogQueueSize), stop: make(chan struct{}), done: make(chan struct{}), state: "connecting"}
	go s.run()
	return s
}

func (s *syslogSink) wants(stream string) bool {
	return (stream == StreamSystem && s.dest.System) || (stream == StreamTraffic && s.dest.Traffic)
}

func (s *syslogSink) enqueue(e *LogEvent) {
	select {
	case s.queue <- e:
	default:
		s.dropped.Add(1)
	}
}

func (s *syslogSink) setState(state string, err error) {
	s.mu.Lock()
	s.state = state
	if err != nil {
		s.lastErr, s.errTime = err.Error(), time.Now().Unix()
	}
	s.mu.Unlock()
}

func (s *syslogSink) run() {
	defer close(s.done)
	var conn net.Conn
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	backoff := time.Second
	connect := func() bool {
		for {
			c, err := dialSyslog(&s.dest)
			if err == nil {
				conn = c
				backoff = time.Second
				s.setState("connected", nil)
				return true
			}
			s.setState("error", fmt.Errorf("cannot connect to %s: %v", s.dest.Address(), err))
			select {
			case <-s.stop:
				return false
			case <-time.After(backoff):
			}
			if backoff *= 2; backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
	if !connect() {
		return
	}
	for {
		var e *LogEvent
		select {
		case <-s.stop:
			return
		case e = <-s.queue:
		}
		msg := frame(&s.dest, formatSyslog(&s.dest, e, syslogHostname))
		for attempt := 0; attempt < 2; attempt++ {
			conn.SetWriteDeadline(time.Now().Add(syslogWriteTimeout))
			_, err := conn.Write(msg)
			if err == nil {
				s.sent.Add(1)
				s.mu.Lock()
				s.lastSent, s.state = time.Now().Unix(), "connected"
				s.mu.Unlock()
				break
			}
			s.setState("error", fmt.Errorf("sending to %s failed: %v", s.dest.Address(), err))
			if s.dest.Protocol == "udp" {
				// Connectionless: a rejected datagram (ICMP unreachable) is just lost
				s.dropped.Add(1)
				break
			}
			conn.Close()
			conn = nil
			if !connect() {
				return
			}
			if attempt == 1 {
				s.dropped.Add(1)
			}
		}
	}
}

func (s *syslogSink) close() {
	close(s.stop)
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
}

// SyslogStatus is one server's delivery state, for the UI.
type SyslogStatus struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Protocol string `json:"protocol"`
	State    string `json:"state"` // connecting, connected (for UDP: sending), error
	LastErr  string `json:"lastError"`
	ErrTime  int64  `json:"errorTime"`
	LastSent int64  `json:"lastSent"`
	Sent     uint64 `json:"sent"`
	Dropped  uint64 `json:"dropped"`
	Queued   int    `json:"queued"`
}

func (s *syslogSink) status() SyslogStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SyslogStatus{
		Name: s.dest.Name, Address: s.dest.Address(), Protocol: s.dest.Protocol,
		State: s.state, LastErr: s.lastErr, ErrTime: s.errTime, LastSent: s.lastSent,
		Sent: s.sent.Load(), Dropped: s.dropped.Load(), Queued: len(s.queue),
	}
}

/* Logger integration */

// outputState is the active outputs and their syslog senders.
type outputState struct {
	cfg   *LogOutputs
	sinks []*syslogSink
}

// ApplyOutputs switches to o: which streams are written to the file, and
// which syslog servers get which streams. Senders for the previous settings
// are stopped (messages still queued for them are discarded).
func (l *Logger) ApplyOutputs(o *LogOutputs) {
	st := &outputState{cfg: o}
	for _, d := range o.Syslog {
		if d.Enabled && (d.System || d.Traffic) {
			st.sinks = append(st.sinks, newSyslogSink(d))
		}
	}
	old := l.outputs.Swap(st)
	if old != nil {
		for _, s := range old.sinks {
			go s.close()
		}
	}
}

// Outputs returns the settings in use (defaults if none were applied).
func (l *Logger) Outputs() *LogOutputs {
	if st := l.outputs.Load(); st != nil {
		return st.cfg
	}
	return DefaultLogOutputs()
}

func (l *Logger) toFile(stream string) bool {
	st := l.outputs.Load()
	if st == nil {
		return true
	}
	if stream == StreamTraffic {
		return st.cfg.TrafficToFile
	}
	return st.cfg.SystemToFile
}

// toSyslog queues e for every server that takes its stream.
func (l *Logger) toSyslog(e *LogEvent) {
	st := l.outputs.Load()
	if st == nil {
		return
	}
	for _, s := range st.sinks {
		if s.wants(e.Stream) {
			s.enqueue(e)
		}
	}
}

// SyslogStatuses reports each active server's delivery state.
func (l *Logger) SyslogStatuses() []SyslogStatus {
	out := []SyslogStatus{}
	if st := l.outputs.Load(); st != nil {
		for _, s := range st.sinks {
			out = append(out, s.status())
		}
	}
	return out
}

// TestSyslog connects to d and sends one test message. For UDP there is no
// delivery confirmation: success means the message was sent.
func TestSyslog(d SyslogDestination) error {
	if err := d.validate(); err != nil {
		return err
	}
	conn, err := dialSyslog(&d)
	if err != nil {
		return fmt.Errorf("cannot connect to %s: %v", d.Address(), err)
	}
	defer conn.Close()
	e := &LogEvent{Stream: StreamSystem, Time: time.Now(), Severity: SeverityInfo, Module: "logger",
		Message: "Test message from Waymark on " + syslogHostname}
	e.Text = "[" + e.Module + "] [system:info] " + e.Message
	conn.SetWriteDeadline(time.Now().Add(syslogWriteTimeout))
	if _, err := conn.Write(frame(&d, formatSyslog(&d, e, syslogHostname))); err != nil {
		return fmt.Errorf("sending to %s failed: %v", d.Address(), err)
	}
	if d.Protocol == "udp" {
		// A closed port often answers with ICMP unreachable; give it a moment to show
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		buf := make([]byte, 1)
		if _, err := conn.Read(buf); err != nil {
			var ne net.Error
			if !(errors.As(err, &ne) && ne.Timeout()) {
				return fmt.Errorf("%s rejected the message: %v", d.Address(), err)
			}
		}
	}
	return nil
}
