package capture

/*
	Recording of HTTP requests: Begin wraps the client side, UpstreamRequest
	the upstream side. Each exchange is written as its own TCP conversation
	in HTTP/1.1 form when finished: bodies cut at the configured limit,
	HTTPS shown on port 80 with the real addresses in a comment, and
	optionally credentials redacted. Upgraded connections record the request
	and the 101 response only.
*/

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sort"
	"strings"
	"sync"
	"time"
)

type ctxKey struct{}

// message is one request or response body being recorded.
type message struct {
	body  bytes.Buffer
	total int64
	limit int
}

func (m *message) add(p []byte) {
	m.total += int64(len(p))
	if room := m.limit - m.body.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		m.body.Write(p)
	}
}

type upstreamSide struct {
	method, uri, host string
	header            http.Header
	req               message
	start             time.Time
	local, remote     Endpoint
	tls               bool
	h2                bool
	status            int
	respHeader        http.Header
	resp              message
	respStart         time.Time
	note              string
	done              bool
}

type httpExchange struct {
	c  *Capture
	mu sync.Mutex

	// client side
	clientIP   string
	client     Endpoint
	server     Endpoint
	tls        bool
	proto      string
	protoMajor int
	host       string
	method     string
	uri        string
	header     http.Header
	req        message
	start      time.Time
	status     int
	respHeader http.Header
	resp       message
	respStart  time.Time
	hijacked   bool
	recordCli  bool
	clientDone bool

	// upstream side
	up *upstreamSide
}

// teeBody copies what is read from a body into a message.
type teeBody struct {
	rc      io.ReadCloser
	ex      *httpExchange
	m       *message
	onEOF   func()
	once    sync.Once
	closeFn func()
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	if n > 0 {
		t.ex.mu.Lock()
		t.m.add(p[:n])
		t.ex.mu.Unlock()
	}
	if err != nil && t.onEOF != nil {
		t.once.Do(t.onEOF)
	}
	return n, err
}

func (t *teeBody) Close() error {
	err := t.rc.Close()
	if t.onEOF != nil {
		t.once.Do(t.onEOF)
	}
	return err
}

// recorder records the response the proxy writes to the client.
type recorder struct {
	http.ResponseWriter
	ex    *httpExchange
	wrote bool
}

func (rw *recorder) WriteHeader(code int) {
	if !rw.wrote && (code >= 200 || code == http.StatusSwitchingProtocols) {
		rw.wrote = true
		rw.ex.mu.Lock()
		rw.ex.status, rw.ex.respHeader, rw.ex.respStart = code, rw.Header().Clone(), time.Now()
		rw.ex.mu.Unlock()
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *recorder) Write(p []byte) (int, error) {
	if !rw.wrote {
		rw.WriteHeader(http.StatusOK)
	}
	rw.ex.mu.Lock()
	rw.ex.resp.add(p)
	rw.ex.mu.Unlock()
	return rw.ResponseWriter.Write(p)
}

func (rw *recorder) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rw *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("connection cannot be taken over")
	}
	rw.ex.mu.Lock()
	rw.ex.hijacked = true
	rw.ex.mu.Unlock()
	return h.Hijack()
}

// Unwrap lets http.ResponseController reach the real ResponseWriter.
func (rw *recorder) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

// Begin starts recording an HTTP request if a capture wants it, and returns
// the writer and request to use and a function to call when done (nil when
// not recorded).
func Begin(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, *http.Request, func()) {
	c := Active()
	if c == nil {
		return w, r, nil
	}
	clientEP := EndpointOf(stringAddr(r.RemoteAddr))
	if !c.WantsHTTP(r.Host, clientEP.IP) {
		return w, r, nil
	}
	limit := c.cfg.MaxBodyKB << 10
	ex := &httpExchange{c: c, clientIP: clientEP.IP.String(), client: clientEP, tls: r.TLS != nil, proto: r.Proto, protoMajor: r.ProtoMajor,
		host: r.Host, start: time.Now(), recordCli: c.SideClient()}
	ex.req.limit, ex.resp.limit = limit, limit
	if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		ex.server = EndpointOf(la)
	} else {
		ex.server = Endpoint{IP: net.IPv4zero, Port: 80}
	}
	c.mu.Lock()
	c.exchgs++
	c.pending[ex] = struct{}{}
	c.mu.Unlock()

	r2 := r.WithContext(context.WithValue(r.Context(), ctxKey{}, ex))
	if ex.recordCli {
		ex.method, ex.header = r.Method, r.Header.Clone()
		// The request target as the client sent it, in origin form
		ex.uri = r.RequestURI
		if !strings.HasPrefix(ex.uri, "/") && ex.uri != "*" {
			ex.uri = r.URL.RequestURI()
		}
		if r.Body != nil && r.Body != http.NoBody {
			r2.Body = &teeBody{rc: r.Body, ex: ex, m: &ex.req}
		}
		w = &recorder{ResponseWriter: w, ex: ex}
	}
	return w, r2, ex.finishClient
}

type stringAddr string

func (s stringAddr) Network() string { return "tcp" }
func (s stringAddr) String() string  { return string(s) }

func passthrough(res *http.Response, err error) *http.Response { return res }

// UpstreamRequest starts recording the upstream request if its client
// request is recorded; call the returned function with the response.
func UpstreamRequest(req *http.Request) (*http.Request, func(*http.Response, error) *http.Response) {
	ex, _ := req.Context().Value(ctxKey{}).(*httpExchange)
	if ex == nil || !ex.c.SideUpstream() || ex.c.stopped.Load() {
		return req, passthrough
	}
	up := &upstreamSide{method: req.Method, uri: req.URL.RequestURI(), host: req.Host, header: req.Header.Clone(), start: time.Now(),
		tls: req.URL.Scheme == "https"}
	if up.host == "" {
		up.host = req.URL.Host
	}
	up.req.limit, up.resp.limit = ex.req.limit, ex.resp.limit
	ex.mu.Lock()
	ex.up = up
	ex.mu.Unlock()
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		ex.mu.Lock()
		up.local, up.remote = EndpointOf(info.Conn.LocalAddr()), EndpointOf(info.Conn.RemoteAddr())
		ex.mu.Unlock()
	}}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	if req.Body != nil && req.Body != http.NoBody {
		req.Body = &teeBody{rc: req.Body, ex: ex, m: &up.req}
	}
	return req, func(res *http.Response, err error) *http.Response {
		if err != nil {
			ex.mu.Lock()
			up.note = "no response: " + err.Error()
			ex.mu.Unlock()
			ex.finishUpstream()
			return res
		}
		ex.mu.Lock()
		up.status, up.respHeader, up.respStart, up.h2 = res.StatusCode, res.Header.Clone(), time.Now(), res.ProtoMajor >= 2
		if up.remote.IP == nil {
			// Without connection details: shows the upstream host
			up.remote = Endpoint{IP: net.IPv4zero, Port: 80}
		}
		ex.mu.Unlock()
		// Upgrade responses are not wrapped
		if res.StatusCode == http.StatusSwitchingProtocols || res.Body == nil || res.Body == http.NoBody {
			if res.StatusCode == http.StatusSwitchingProtocols {
				ex.mu.Lock()
				up.note = "protocol upgraded; the tunnel after this is not captured"
				ex.mu.Unlock()
			}
			ex.finishUpstream()
			return res
		}
		res.Body = &teeBody{rc: res.Body, ex: ex, m: &up.resp, onEOF: ex.finishUpstream}
		return res
	}
}

func (ex *httpExchange) finishClient() {
	ex.mu.Lock()
	if ex.clientDone {
		ex.mu.Unlock()
		return
	}
	ex.clientDone = true
	ex.mu.Unlock()
	if ex.recordCli {
		ex.emitClient("")
	}
	ex.release()
}

func (ex *httpExchange) finishUpstream() {
	ex.mu.Lock()
	up := ex.up
	if up == nil || up.done {
		ex.mu.Unlock()
		return
	}
	up.done = true
	ex.mu.Unlock()
	ex.emitUpstream(up, "")
	ex.release()
}

// release drops the exchange from the capture's pending list once both
// sides are written.
func (ex *httpExchange) release() {
	ex.mu.Lock()
	done := ex.clientDone && (ex.up == nil || ex.up.done)
	ex.mu.Unlock()
	if done {
		ex.c.mu.Lock()
		delete(ex.c.pending, ex)
		ex.c.mu.Unlock()
	}
}

// flushOnStop writes an exchange that was still in progress when the
// capture stopped.
func (ex *httpExchange) flushOnStop() {
	const note = "capture stopped before this exchange finished"
	ex.mu.Lock()
	cli := !ex.clientDone && ex.recordCli
	ex.clientDone = true
	up := ex.up
	upPending := up != nil && !up.done
	if upPending {
		up.done = true
	}
	ex.mu.Unlock()
	if upPending {
		ex.emitUpstream(up, note)
	}
	if cli {
		ex.emitClient(note)
	}
}

func (ex *httpExchange) emitClient(note string) {
	c := ex.c
	ex.mu.Lock()
	a, b := ex.client, ex.server
	var notes []string
	if ex.tls {
		notes = append(notes, fmt.Sprintf("decrypted from TLS on port %d (shown on port 80)", b.Port))
		b.Port = 80
	}
	if ex.protoMajor >= 2 {
		notes = append(notes, fmt.Sprintf("client used %s from port %d (shown as HTTP/1.1 on its own port)", ex.proto, a.Port))
		a.Port = c.syntheticPort()
	}
	comment := fmt.Sprintf("Client %s → Waymark %s · host %s", ex.client, ex.server, ex.host)
	if len(notes) > 0 {
		comment += " · " + strings.Join(notes, " · ")
	}
	reqBytes := encodeRequest(ex.method, ex.uri, ex.host, ex.header, &ex.req, c.cfg.Redact, note)
	var respBytes []byte
	switch {
	case ex.status != 0:
		respBytes = encodeResponse(ex.status, ex.respHeader, &ex.resp, ex.method == http.MethodHead, c.cfg.Redact, upgradeNote(ex.status, note))
	case ex.hijacked:
		comment += " · connection taken over (WebSocket or similar); not captured after the request"
	case note == "":
		comment += " · no response was written"
	}
	start, respStart := ex.start, ex.respStart
	ex.mu.Unlock()

	f := c.newTCPFlowAt(start, a, b, comment)
	f.sendAt(start, true, reqBytes)
	if respBytes != nil {
		if respStart.IsZero() {
			respStart = time.Now()
		}
		f.sendAt(respStart, false, respBytes)
	}
	f.closeAt(time.Now())
}

func upgradeNote(status int, note string) string {
	if status == http.StatusSwitchingProtocols {
		if note != "" {
			return note + "; protocol upgraded, tunnel not captured"
		}
		return "protocol upgraded; the tunnel after this is not captured"
	}
	return note
}

func (ex *httpExchange) emitUpstream(up *upstreamSide, note string) {
	c := ex.c
	ex.mu.Lock()
	a, b := up.local, up.remote
	if a.IP == nil {
		a = Endpoint{IP: net.IPv4zero, Port: c.syntheticPort()}
	}
	if b.IP == nil {
		b = Endpoint{IP: net.IPv4zero, Port: 80}
	}
	realRemote := b
	var notes []string
	if up.tls {
		notes = append(notes, fmt.Sprintf("decrypted from TLS on port %d (shown on port 80)", b.Port))
		b.Port = 80
	}
	if up.h2 {
		notes = append(notes, "destination used HTTP/2 (shown as HTTP/1.1 on its own port)")
		a.Port = c.syntheticPort()
	}
	comment := fmt.Sprintf("Waymark %s → destination %s · for client %s", up.local, realRemote, ex.clientIP)
	if len(notes) > 0 {
		comment += " · " + strings.Join(notes, " · ")
	}
	if up.note != "" && up.status == 0 {
		comment += " · " + up.note
	}
	reqBytes := encodeRequest(up.method, up.uri, up.host, up.header, &up.req, c.cfg.Redact, note)
	var respBytes []byte
	if up.status != 0 {
		n := note
		if up.note != "" {
			n = strings.TrimPrefix(strings.Join([]string{note, up.note}, "; "), "; ")
		}
		respBytes = encodeResponse(up.status, up.respHeader, &up.resp, up.method == http.MethodHead, c.cfg.Redact, n)
	}
	start, respStart := up.start, up.respStart
	ex.mu.Unlock()

	f := c.newTCPFlowAt(start, a, b, comment)
	f.sendAt(start, true, reqBytes)
	if respBytes != nil {
		f.sendAt(respStart, false, respBytes)
	}
	f.closeAt(time.Now())
}

/* HTTP/1.1 encoding */

var redactedHeaders = map[string]bool{"Authorization": true, "Proxy-Authorization": true, "Cookie": true, "Set-Cookie": true}

// skipHeaders are replaced by the recorder's own framing.
var skipHeaders = map[string]bool{"Content-Length": true, "Transfer-Encoding": true, "Host": true}

func cleanValue(v string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, v)
}

func writeHeaders(b *bytes.Buffer, h http.Header, redact bool) {
	keys := make([]string, 0, len(h))
	for k := range h {
		if !skipHeaders[http.CanonicalHeaderKey(k)] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range h[k] {
			if redact && redactedHeaders[http.CanonicalHeaderKey(k)] {
				v = "[redacted]"
			}
			b.WriteString(cleanValue(k) + ": " + cleanValue(v) + "\r\n")
		}
	}
}

func bodyNotes(b *bytes.Buffer, m *message, note string) {
	if m.total > int64(m.body.Len()) {
		fmt.Fprintf(b, "X-Waymark-Capture: body cut short, first %d of %d bytes kept\r\n", m.body.Len(), m.total)
	}
	if note != "" {
		b.WriteString("X-Waymark-Capture-Note: " + cleanValue(note) + "\r\n")
	}
}

func encodeRequest(method, uri, host string, h http.Header, m *message, redact bool, note string) []byte {
	var b bytes.Buffer
	if method == "" {
		method = http.MethodGet
	}
	if uri == "" {
		uri = "/"
	}
	b.WriteString(cleanValue(method) + " " + cleanValue(uri) + " HTTP/1.1\r\n")
	b.WriteString("Host: " + cleanValue(host) + "\r\n")
	writeHeaders(&b, h, redact)
	if m.total > 0 || method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		fmt.Fprintf(&b, "Content-Length: %d\r\n", m.body.Len())
	}
	bodyNotes(&b, m, note)
	b.WriteString("\r\n")
	b.Write(m.body.Bytes())
	return b.Bytes()
}

func encodeResponse(status int, h http.Header, m *message, head bool, redact bool, note string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\r\n", status, http.StatusText(status))
	writeHeaders(&b, h, redact)
	noBody := status < 200 || status == http.StatusNoContent || status == http.StatusNotModified
	switch {
	case head:
		if cl := h.Get("Content-Length"); cl != "" {
			b.WriteString("Content-Length: " + cleanValue(cl) + "\r\n")
		}
	case !noBody:
		fmt.Fprintf(&b, "Content-Length: %d\r\n", m.body.Len())
	}
	bodyNotes(&b, m, note)
	b.WriteString("\r\n")
	if !head && !noBody {
		b.Write(m.body.Bytes())
	}
	return b.Bytes()
}
