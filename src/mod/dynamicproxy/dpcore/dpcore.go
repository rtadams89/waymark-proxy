package dpcore

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"imuslab.com/zoraxy/mod/capture"
	"imuslab.com/zoraxy/mod/dynamicproxy/domainsniff"
	"imuslab.com/zoraxy/mod/dynamicproxy/permissionpolicy"
)

// Tunnel settings for HTTP protocol upgrades
const (
	upgradeTunnelTimeout           = 300 * time.Second //Deadline of an upgraded connection tunnel
	upgradeTunnelRefreshOnActivity = true              // the deadline is an idle timeout
)

// ReverseProxy proxies HTTP requests to a backend and its responses back,
// including HTTPS tunnels via http.Hijacker.
type ReverseProxy struct {
	// Timeout of the proxy server
	Timeout time.Duration

	// Director modifies the request into the one sent with Transport; it must
	// not access the request after returning.
	Director func(*http.Request)

	// Transport performs the proxy requests.
	Transport http.RoundTripper

	// FlushInterval is the interval for flushing the response body to the
	// client; zero disables periodic flushing.
	FlushInterval time.Duration

	// ErrorLog logs proxy errors; nil logs to the standard logger.
	ErrorLog *log.Logger

	// ModifyResponse optionally modifies the backend response; an error
	// results in 502.
	ModifyResponse func(*http.Response) error

	//Prepender is an optional prepend text for URL rewrite
	Prepender string

	Verbal bool

	// useRequestHostAsSNI derives the upstream TLS SNI from the request host
	// for upstreams addressed by IP.
	useRequestHostAsSNI bool

	// useClientHostAsSNI verifies the upstream certificate against, and sends
	// as SNI, the host the client requested. sniTransports caches one
	// transport per server name and HTTP version.
	useClientHostAsSNI bool
	sniTransports      sync.Map // transportKey -> *http.Transport
	sniTransportCount  atomic.Int32
}

type ResponseRewriteRuleSet struct {
	/* Basic Rewrite Rulesets */
	ProxyDomain       string
	OriginalHost      string
	UseTLS            bool
	NoCache           bool
	PathPrefix        string // virtual directory prefix; / is rewritten to it
	UpstreamHeaders   [][]string
	DownstreamHeaders [][]string

	/* Advance Usecase Options */
	HostHeaderOverwrite            string // overrides the request "Host" header
	NoRemoveHopByHop               bool   // keeps hop-by-hop headers
	NoRemoveUserAgentHeader        bool   // keeps the User-Agent header of the server response
	DisableChunkedTransferEncoding bool
	ForceHTTP11                    bool // HTTP/1.1 to the upstream
	AllowConnect                   bool // allows CONNECT tunnels to ProxyDomain
	AllowUpgrade                   bool // forwards HTTP protocol upgrades

	/* System Information Payload */
	DevelopmentMode bool   // adds development mode information to requests
	Version         string // version in the User-Agent and X-Proxy-By headers

	/* HTTP/3 (QUIC) */
	AltSvc string // Alt-Svc header value; empty for none
}

type DpcoreOptions struct {
	IgnoreTLSVerification   bool          // disables upstream TLS verification
	FlushInterval           time.Duration // flush interval of normal responses; streams flush immediately
	MaxConcurrentConnection int           // maximum concurrent connections to the upstream
	ResponseHeaderTimeout   int64         // milliseconds; 0 for the default
	DevelopmentMode         bool
	UpstreamTLSServerName   string // TLS SNI and verification name for HTTPS upstreams; empty to derive from the upstream address
	TLSNameMode             string // TLSNameAuto, TLSNameUpstream or TLSNameProxyHost
}

// TLS name modes: the name an HTTPS upstream's certificate is checked
// against and sent as SNI.
const (
	TLSNameAuto      = ""          // the "Host" header override if set, else the upstream's host name; IP upstreams use the request host
	TLSNameUpstream  = "upstream"  // the host name (or IP address) in the upstream address
	TLSNameProxyHost = "proxyhost" // the host name the client requested from Waymark
)

// maxSNITransports is the size of the per-name transport cache; beyond it a
// transport is created per request.
const maxSNITransports = 64

type transportKey struct {
	serverName string
	http11     bool
}

// sniServerName returns host without port, or "" if empty or an IP literal.
func sniServerName(host string) string {
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" || net.ParseIP(host) != nil {
		return ""
	}
	return host
}

func NewDynamicProxyCore(target *url.URL, prepender string, dpcOptions *DpcoreOptions) *ReverseProxy {
	targetQuery := target.RawQuery
	director := func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path, req.URL.RawPath = joinURLPath(target, req.URL)
		if targetQuery == "" || req.URL.RawQuery == "" {
			req.URL.RawQuery = targetQuery + req.URL.RawQuery
		} else {
			req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
		}
	}

	thisTransporter := newBaseTransport(dpcOptions)

	// SNI for HTTPS backends
	useRequestHostAsSNI := false
	useClientHostAsSNI := false
	if strings.EqualFold(target.Scheme, "https") && dpcOptions.TLSNameMode == TLSNameUpstream {
		// The name in the upstream address; an IP is checked against the
		// certificate's IP addresses
		thisTransporter.TLSClientConfig = &tls.Config{ServerName: target.Hostname(), InsecureSkipVerify: dpcOptions.IgnoreTLSVerification}
	} else if strings.EqualFold(target.Scheme, "https") && dpcOptions.TLSNameMode == TLSNameProxyHost {
		// The name the client requested, set per request
		thisTransporter.TLSClientConfig = &tls.Config{InsecureSkipVerify: dpcOptions.IgnoreTLSVerification}
		useClientHostAsSNI = true
	} else if strings.EqualFold(target.Scheme, "https") {
		// A configured server name takes precedence over the upstream address
		serverName := sniServerName(dpcOptions.UpstreamTLSServerName)
		if serverName == "" {
			serverName = target.Hostname()
		}
		cfg := &tls.Config{ServerName: serverName}
		if dpcOptions.IgnoreTLSVerification {
			cfg.InsecureSkipVerify = true
		}
		// An IP server name derives the SNI from the request host
		if net.ParseIP(serverName) != nil {
			cfg.ServerName = ""
			useRequestHostAsSNI = true
		}
		thisTransporter.TLSClientConfig = cfg
	}

	return &ReverseProxy{
		Director:            director,
		Prepender:           prepender,
		FlushInterval:       dpcOptions.FlushInterval,
		Verbal:              dpcOptions.DevelopmentMode,
		Transport:           thisTransporter,
		useRequestHostAsSNI: useRequestHostAsSNI,
		useClientHostAsSNI:  useClientHostAsSNI,
	}
}

// transportFor returns base, or a cached copy of it for a TLS server name
// and/or HTTP/1.1 only, up to maxSNITransports copies.
func (p *ReverseProxy) transportFor(base *http.Transport, serverName string, http11 bool) *http.Transport {
	if serverName == "" && !http11 {
		return base
	}
	key := transportKey{serverName: serverName, http11: http11}
	if v, ok := p.sniTransports.Load(key); ok {
		return v.(*http.Transport)
	}
	trc := base.Clone()
	if http11 {
		// A non-nil empty TLSNextProto disables HTTP/2
		trc.TLSNextProto = make(map[string]func(authority string, c *tls.Conn) http.RoundTripper)
		trc.ForceAttemptHTTP2 = false
	}
	if serverName != "" {
		cfg := &tls.Config{}
		if trc.TLSClientConfig != nil {
			cfg = trc.TLSClientConfig.Clone()
		}
		cfg.ServerName = serverName
		trc.TLSClientConfig = cfg
	}
	if p.sniTransportCount.Load() >= maxSNITransports {
		// Not cached
		trc.DisableKeepAlives = true
		return trc
	}
	if v, loaded := p.sniTransports.LoadOrStore(key, trc); loaded {
		return v.(*http.Transport)
	}
	p.sniTransportCount.Add(1)
	return trc
}
func joinURLPath(a, b *url.URL) (path, rawpath string) {
	apath, bpath := a.EscapedPath(), b.EscapedPath()
	aslash, bslash := strings.HasSuffix(apath, "/"), strings.HasPrefix(bpath, "/")

	switch {
	case aslash && bslash:
		return a.Path + b.Path[1:], apath + bpath[1:]
	case !aslash && !bslash:
		return a.Path + "/" + b.Path, apath + "/" + bpath
	default:
		return a.Path + b.Path, apath + bpath
	}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// Hop-by-hop headers, removed when sent to the backend.
var hopHeaders = []string{
	"Connection",
	"Proxy-Connection", // non-standard
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te", // canonical form of "TE"
	"Trailer",
	"Transfer-Encoding",
}

// copyResponse copies the response body from src to dst with the given
// flush interval.
func (p *ReverseProxy) copyResponse(dst http.ResponseWriter, src io.Reader, flushInterval time.Duration) error {
	var w io.Writer = dst
	if flushInterval != 0 {
		mlw := &maxLatencyWriter{
			dst:     dst,
			flush:   http.NewResponseController(dst).Flush,
			latency: flushInterval,
		}

		defer mlw.stop()
		// Flushes the headers after flushInterval even without body writes
		mlw.flushPending = true
		mlw.t = time.AfterFunc(flushInterval, mlw.delayedFlush)
		w = mlw
	}

	bp := copyBufPool.Get().(*[]byte)
	_, err := p.copyBuffer(w, src, *bp)
	copyBufPool.Put(bp)
	return err
}

// copyBufPool holds the 64 KB buffers copyResponse copies bodies with.
var copyBufPool = sync.Pool{New: func() any {
	b := make([]byte, 64*1024)
	return &b
}}

// copyBuffer copies src to dst using buf (64 KB if empty).
func (p *ReverseProxy) copyBuffer(dst io.Writer, src io.Reader, buf []byte) (int64, error) {
	if len(buf) == 0 {
		buf = make([]byte, 64*1024)
	}

	var written int64
	for {
		nr, rerr := src.Read(buf)
		if rerr != nil && rerr != io.EOF && rerr != context.Canceled {
			p.logf("dpcore read error during body copy: %v", rerr)
		}

		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			if nw > 0 {
				written += int64(nw)
			}

			if werr != nil {
				return written, werr
			}

			if nr != nw {
				return written, io.ErrShortWrite
			}
		}

		if rerr != nil {
			if rerr == io.EOF {
				rerr = nil
			}
			return written, rerr
		}
	}
}

func (p *ReverseProxy) logf(format string, args ...interface{}) {
	if p.ErrorLog != nil {
		p.ErrorLog.Printf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

func newBaseTransport(opts *DpcoreOptions) *http.Transport {
	optimalConcurrentConnection := 256
	if opts.MaxConcurrentConnection > 0 {
		optimalConcurrentConnection = opts.MaxConcurrentConnection
	}

	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          optimalConcurrentConnection * 2,
		MaxIdleConnsPerHost:   optimalConcurrentConnection,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
		DisableKeepAlives:     false,
	}

	if opts.ResponseHeaderTimeout > 0 {
		tr.ResponseHeaderTimeout = time.Duration(opts.ResponseHeaderTimeout) * time.Millisecond
	}

	if opts.IgnoreTLSVerification {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	return tr
}

func (p *ReverseProxy) ProxyHTTP(rw http.ResponseWriter, req *http.Request, rrr *ResponseRewriteRuleSet) (int, error) {
	transport := p.Transport

	outreq := req.Clone(req.Context())
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	outreq = outreq.WithContext(ctx)
	outreq.Body = stickyEOFBody(outreq.Body)

	p.Director(outreq)
	outreq.Close = false

	// Keeps the upstream host only for TLS targets with an external domain name
	if rrr.HostHeaderOverwrite != "" {
		outreq.Host = rrr.HostHeaderOverwrite
	} else if !(rrr.UseTLS && isExternalDomainName(rrr.ProxyDomain)) {
		outreq.Host = rrr.OriginalHost
	}

	outreq.Header = make(http.Header)
	copyHeader(outreq.Header, req.Header)

	// Upgrade type, read before hop-by-hop headers are removed
	reqUpType := upgradeType(outreq.Header)
	if reqUpType != "" {
		if !isPrintableASCII(reqUpType) {
			return http.StatusBadRequest, errors.New("client requested an invalid upgrade protocol")
		}
		if !rrr.AllowUpgrade {
			http.Error(rw, "Protocol upgrades are not enabled for this endpoint", http.StatusForbidden)
			return http.StatusForbidden, nil
		}
	}

	// Removes hop-by-hop headers; always for HTTP/3 requests
	if !rrr.NoRemoveHopByHop || req.ProtoMajor == 3 {
		removeHeaders(outreq.Header)
	}

	if rrr.NoCache {
		setNoCacheHeader(outreq.Header)
	}

	if reqUpType != "" {
		restoreUpgradeHeaders(outreq.Header, reqUpType)
	}

	addXForwardedForHeader(outreq)

	injectUserDefinedHeaders(outreq.Header, rrr.UpstreamHeaders)

	// After the user-defined headers
	rewriteUserAgent(outreq.Header, "Waymark/"+rrr.Version)

	if rrr.DisableChunkedTransferEncoding || domainsniff.IsProxmox(req) {
		outreq.TransferEncoding = []string{"identity"}
	}

	// A body of unknown length is checked for being empty
	if outreq.ContentLength == -1 && outreq.Body != nil && outreq.Body != http.NoBody {
		buf := make([]byte, 1)
		n, err := outreq.Body.Read(buf)
		switch {
		case n == 0 && err == io.EOF:
			outreq.Body = http.NoBody
			outreq.ContentLength = 0
			outreq.GetBody = nil
		case n > 0:
			outreq.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf[:n]), outreq.Body))
		}
	}

	if tr, ok := transport.(*http.Transport); ok {
		// Upgrade requests use HTTP/1.1 upstream
		http11 := rrr.ForceHTTP11 || reqUpType != ""
		serverName := ""
		if p.useClientHostAsSNI {
			// TLS name mode "proxyhost"
			serverName = sniServerName(rrr.OriginalHost)
		} else if p.useRequestHostAsSNI {
			// SNI from the request host for IP-addressed upstreams
			serverName = sniServerName(outreq.Host)
		}
		transport = p.transportFor(tr, serverName, http11)
	}

	outreq, captured := capture.UpstreamRequest(outreq)
	res, err := transport.RoundTrip(outreq)
	res = captured(res, err)
	if err != nil {
		if p.Verbal {
			p.logf("http: proxy error: %v", err)
		}
		return http.StatusBadGateway, err
	}

	// Upgraded connections are tunneled
	if res.StatusCode == http.StatusSwitchingProtocols {
		return p.handleUpgradeResponse(rw, reqUpType, res)
	}

	// Removes hop-by-hop headers; always for HTTP/3 requests
	if !rrr.NoRemoveHopByHop || req.ProtoMajor == 3 {
		removeHeaders(res.Header)
	}

	if rrr.NoCache {
		setNoCacheHeader(res.Header)
	}

	if _, ok := res.Header["User-Agent"]; ok && !rrr.NoRemoveUserAgentHeader {
		res.Header.Del("User-Agent")
	}

	if p.ModifyResponse != nil {
		if err := p.ModifyResponse(res); err != nil {
			if p.Verbal {
				p.logf("http: proxy error: %v", err)
			}

			return http.StatusBadGateway, err
		}
	}

	if rrr.DevelopmentMode {
		res.Header.Set("x-proxy-by", "waymark/"+rrr.Version)
	}

	// Location header rewrite
	if res.Header.Get("Location") != "" {
		locationRewrite := res.Header.Get("Location")
		originLocation := res.Header.Get("Location")
		res.Header.Set("zr-origin-location", originLocation)
		decodedOriginLocation, err := url.PathUnescape(originLocation)
		if err == nil {
			originLocation = decodedOriginLocation
		}
		if strings.HasPrefix(originLocation, "http://") || strings.HasPrefix(originLocation, "https://") {
			// Absolute URL: replaces the upstream host with the requested host
			lr, err := replaceLocationHost(locationRewrite, rrr, req.TLS != nil)
			if err == nil {
				locationRewrite = lr
			}
		} else if strings.HasPrefix(originLocation, "/") && rrr.PathPrefix != "" {
			// Relative to the virtual directory prefix
			trimmedPrefix := strings.TrimSuffix(rrr.PathPrefix, "/")
			// Keeps a Location that already starts with the prefix
			if trimmedPrefix != "" && (strings.HasPrefix(originLocation, trimmedPrefix+"/") || originLocation == trimmedPrefix) {
				locationRewrite = originLocation
			} else {
				// Prepends the prefix, e.g. /bar -> /proxy/bar
				locationRewrite = trimmedPrefix + originLocation
			}
		} else {
		}

		res.Header.Set("Location", locationRewrite)
	}

	injectUserDefinedHeaders(res.Header, rrr.DownstreamHeaders)

	copyHeader(rw.Header(), res.Header)

	permissionpolicy.InjectPermissionPolicyHeader(rw, nil)

	// Announces the response trailers
	if len(res.Trailer) > 0 {
		trailerKeys := make([]string, 0, len(res.Trailer))
		for k := range res.Trailer {
			trailerKeys = append(trailerKeys, k)
		}
		rw.Header().Add("Trailer", strings.Join(trailerKeys, ", "))
	}

	if rrr.AltSvc != "" {
		rw.Header().Set("Alt-Svc", rrr.AltSvc)
	}

	rw.WriteHeader(res.StatusCode)
	if len(res.Trailer) > 0 {
		// Forces chunked encoding when there are trailers
		if fl, ok := rw.(http.Flusher); ok {
			fl.Flush()
		}
	}

	flushInterval := p.getFlushInterval(req, res)
	p.copyResponse(rw, res.Body, flushInterval)

	// Closes the body to populate res.Trailer
	res.Body.Close()
	copyHeader(rw.Header(), res.Trailer)

	return res.StatusCode, nil
}

func (p *ReverseProxy) ProxyHTTPS(rw http.ResponseWriter, req *http.Request, rrr *ResponseRewriteRuleSet) (int, error) {
	// Validates the CONNECT target before hijacking
	connectTarget := req.URL.Host
	if !strings.Contains(connectTarget, ":") {
		connectTarget += ":443"
	}
	allowedHost := rrr.ProxyDomain
	if !strings.Contains(allowedHost, ":") {
		if rrr.UseTLS {
			allowedHost += ":443"
		} else {
			allowedHost += ":80"
		}
	}
	if connectTarget != allowedHost {
		http.Error(rw, "Forbidden", http.StatusForbidden)
		return http.StatusForbidden, errors.New("CONNECT to " + connectTarget + " denied: target must match the configured destination")
	}

	hij, ok := rw.(http.Hijacker)
	if !ok {
		p.logf("http server does not support hijacker")
		return http.StatusNotImplemented, errors.New("http server does not support hijacker")
	}

	clientConn, _, err := hij.Hijack()
	if err != nil {
		if p.Verbal {
			p.logf("http: proxy error: %v", err)
		}
		return http.StatusInternalServerError, err
	}

	host := req.URL.Host
	if !strings.Contains(host, ":") {
		host += ":443"
	}
	serverName := ""
	if serverName == "" {
		serverName = req.URL.Hostname()
	}

	tlsConfig := &tls.Config{
		ServerName: serverName,
	}
	proxyConn, err := tls.Dial("tcp", host, tlsConfig)
	if err != nil {
		if p.Verbal {
			p.logf("http: proxy error: %v", err)
		}
		clientConn.Close()
		return http.StatusInternalServerError, err
	}

	// Sets the connection deadlines (default 5 minutes)
	deadline := time.Now()
	if p.Timeout == 0 {
		deadline = deadline.Add(time.Minute * 5)
	} else {
		deadline = deadline.Add(p.Timeout)
	}

	err = clientConn.SetDeadline(deadline)
	if err != nil {
		if p.Verbal {
			p.logf("http: proxy error: %v", err)
		}
		return http.StatusGatewayTimeout, err
	}

	err = proxyConn.SetDeadline(deadline)
	if err != nil {
		if p.Verbal {
			p.logf("http: proxy error: %v", err)
		}

		return http.StatusGatewayTimeout, err
	}

	_, err = clientConn.Write([]byte("HTTP/1.0 200 OK\r\n\r\n"))
	if err != nil {
		if p.Verbal {
			p.logf("http: proxy error: %v", err)
		}

		return http.StatusInternalServerError, err
	}

	go func() {
		io.Copy(clientConn, proxyConn)
		clientConn.Close()
		proxyConn.Close()
	}()

	io.Copy(proxyConn, clientConn)
	proxyConn.Close()
	clientConn.Close()

	return http.StatusOK, nil
}

func (p *ReverseProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request, rrr *ResponseRewriteRuleSet) (int, error) {
	if req.Method == "CONNECT" {
		if !rrr.AllowConnect {
			http.Error(rw, "Method Not Allowed", http.StatusMethodNotAllowed)
			return http.StatusMethodNotAllowed, nil
		}
		return p.ProxyHTTPS(rw, req, rrr)
	}
	return p.ProxyHTTP(rw, req, rrr)
}
