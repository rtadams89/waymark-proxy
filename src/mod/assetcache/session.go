package assetcache

import (
	"bytes"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// CacheStatusHeader is set on static file responses of cached rules: HIT,
// REVALIDATED, MISS (fetched and stored) or BYPASS (fetched, not storable).
const CacheStatusHeader = "X-Waymark-Cache"

// BeginOptions are request details from the proxy.
type BeginOptions struct {
	// The Authorization header holds the proxy's own basic auth credentials
	AuthorizationIsZoraxys bool
}

// Session handles one request on a rule with caching on: it serves the
// request from memory, or wraps the client's ResponseWriter to store the
// upstream's response.
type Session struct {
	store    *Store
	cfg      *Config
	rule     string
	baseKey  string
	extMatch bool

	w  http.ResponseWriter
	r  *http.Request // the client's request
	up *http.Request // the request sent to the upstream

	served       bool
	servedStatus int

	revalidate  *entry // stored copy being revalidated with a conditional request
	wroteHeader bool
	status      int
	intercept   bool // the upstream answered the conditional request with 304
	interceptH  http.Header
	static      bool
	capture     bool
	buf         bytes.Buffer
	upstreamErr error
}

// Begin starts handling r for a rule; nil when the request is not a cache
// candidate.
func (s *Store) Begin(w http.ResponseWriter, r *http.Request, rule string, cfg *Config, opts BeginOptions) *Session {
	if s == nil || !cfg.On() || r.URL == nil {
		return nil
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return nil
	}
	if r.Header.Get("Upgrade") != "" || requestNoStore(r) {
		return nil
	}
	if r.Header.Get("Authorization") != "" && !opts.AuthorizationIsZoraxys {
		return nil // per-user credentials for the upstream
	}
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(r.URL.Path)), ".")
	extMatch := ext != "" && cfg.hasExtension(ext)
	if ext != "" && !extMatch {
		return nil // not a cached file type
	}
	if ext == "" && !cfg.MatchContentTypes {
		return nil
	}

	rule = normalizeRule(rule)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	baseKey := rule + "\n" + scheme + "\n" + strings.ToLower(r.Host) + "\n" + r.URL.RequestURI()
	sess := &Session{store: s, cfg: cfg, rule: rule, baseKey: baseKey, extMatch: extMatch, w: w, r: r}

	stored := s.lookup(baseKey, r)
	if stored != nil && !requestWantsFresh(r) && s.now().Before(stored.expires) {
		sess.served = true
		sess.servedStatus = s.serve(w, r, stored, "HIT", func(st *RuleStats) { st.Hits++ })
		return sess
	}
	if r.Method == http.MethodHead || r.Header.Get("Range") != "" {
		return nil
	}

	up := r.Clone(r.Context())
	if extMatch || stored != nil {
		// Fetches the full file; client conditionals are answered from the
		// stored copy
		for _, h := range []string{"If-None-Match", "If-Modified-Since", "If-Range"} {
			up.Header.Del(h)
		}
	}
	if stored != nil {
		etag, lm := stored.validators()
		if etag != "" {
			up.Header.Set("If-None-Match", etag)
		}
		if lm != "" {
			up.Header.Set("If-Modified-Since", lm)
		}
		if etag != "" || lm != "" {
			sess.revalidate = stored
		}
	}
	sess.up = up
	return sess
}

// Served reports whether the request was answered from memory.
func (c *Session) Served() bool { return c != nil && c.served }

// ServedStatus is the status code of a response served from memory.
func (c *Session) ServedStatus() int { return c.servedStatus }

// UpstreamRequest is the request to send to the upstream in place of the
// client's.
func (c *Session) UpstreamRequest() *http.Request { return c.up }

// SetUpstreamResult records the proxy's outcome; the response is stored only
// if it completed without error.
func (c *Session) SetUpstreamResult(statusCode int, err error) {
	if c != nil {
		c.upstreamErr = err
	}
}

/* http.ResponseWriter, used by the proxy for the upstream's response */

func (c *Session) Header() http.Header { return c.w.Header() }

func (c *Session) WriteHeader(code int) {
	if c.wroteHeader {
		return
	}
	c.wroteHeader = true
	c.status = code
	h := c.w.Header()
	if code == http.StatusNotModified && c.revalidate != nil {
		// Unchanged: serves the stored copy in Finish
		c.intercept = true
		c.interceptH = h.Clone()
		return
	}
	if code == http.StatusOK && c.r.Method == http.MethodGet && (c.extMatch || isStaticContentType(h.Get("Content-Type"))) {
		c.static = true
		storable := notStorableReason(h) == "" &&
			freshnessLifetime(h, c.store.now(), c.defaultTTL()) > 0
		if cl, err := strconv.ParseInt(h.Get("Content-Length"), 10, 64); err == nil && cl > c.maxFileBytes() {
			storable = false
		}
		c.capture = storable
		if storable {
			h.Set(CacheStatusHeader, "MISS")
		} else {
			h.Set(CacheStatusHeader, "BYPASS")
		}
	}
	c.w.WriteHeader(code)
}

func (c *Session) Write(p []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	if c.intercept {
		return len(p), nil
	}
	if c.capture {
		if int64(c.buf.Len()+len(p)) > c.maxFileBytes() {
			c.capture = false
			c.buf = bytes.Buffer{}
		} else {
			c.buf.Write(p)
		}
	}
	return c.w.Write(p)
}

// Flush passes streaming flushes through to the client.
func (c *Session) Flush() {
	if c.intercept {
		return
	}
	if f, ok := c.w.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the client's writer.
func (c *Session) Unwrap() http.ResponseWriter { return c.w }

// Finish stores the captured response, or serves the stored copy after a
// 304.
func (c *Session) Finish() {
	if c == nil || c.served {
		return
	}
	now := c.store.now()
	if c.intercept {
		merged := c.revalidate.header.Clone()
		for _, k := range []string{"Cache-Control", "Expires", "Date", "Age"} {
			if vals := c.interceptH.Values(k); len(vals) > 0 {
				merged[k] = vals
			}
		}
		c.store.refresh(c.revalidate, c.interceptH, now.Add(freshnessLifetime(merged, now, c.defaultTTL())))
		if notStorableReason(merged) != "" {
			c.store.remove(c.revalidate) // no longer cacheable
		}
		clear(c.w.Header())
		c.store.serve(c.w, c.r, c.revalidate, "REVALIDATED", func(st *RuleStats) { st.Revalidated++ })
		return
	}
	if !c.static {
		return
	}
	ok := c.capture && c.upstreamErr == nil && c.r.Context().Err() == nil
	if cl, err := strconv.ParseInt(c.w.Header().Get("Content-Length"), 10, 64); ok && err == nil && cl != int64(c.buf.Len()) {
		ok = false // truncated
	}
	if !ok {
		c.store.count1(c.rule, func(st *RuleStats) { st.Uncacheable++ })
		return
	}

	h := storedHeader(c.w.Header())
	e := &entry{
		rule:     c.rule,
		baseKey:  c.baseKey,
		header:   h,
		body:     bytes.Clone(c.buf.Bytes()),
		storedAt: now,
		expires:  now.Add(freshnessLifetime(c.w.Header(), now, c.defaultTTL())),
	}
	if lm, err := http.ParseTime(h.Get("Last-Modified")); err == nil {
		e.modTime = lm
	}
	e.vary = varyHeaders(h)
	for _, name := range e.vary {
		e.varyVals = append(e.varyVals, normalizeVaryValue(name, c.r.Header.Values(name)))
	}
	c.buf = bytes.Buffer{}
	c.store.insert(e)
	c.store.count1(c.rule, func(st *RuleStats) { st.Misses++ })
}

func (c *Session) defaultTTL() time.Duration {
	return time.Duration(c.cfg.DefaultTTLSeconds) * time.Second
}

func (c *Session) maxFileBytes() int64 {
	return int64(c.cfg.MaxFileMB) << 20
}

// Headers not replayed from a stored response.
var unstoredHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade",
	"Content-Length", "Content-Range", "Date", "Age", "Set-Cookie", CacheStatusHeader,
}

func storedHeader(h http.Header) http.Header {
	out := h.Clone()
	for _, k := range unstoredHeaders {
		out.Del(k)
	}
	return out
}

type statusRecorder struct {
	http.ResponseWriter
	code  int
	bytes int64
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(p)
	s.bytes += int64(n)
	return n, err
}

// serve answers r from a stored entry. http.ServeContent handles the
// client's conditional requests (304) and Range requests (206).
func (s *Store) serve(w http.ResponseWriter, r *http.Request, e *entry, status string, count func(*RuleStats)) int {
	hdr, body, modTime, storedAt := s.snapshot(e)
	wh := w.Header()
	for k, v := range hdr {
		wh[k] = v
	}
	age := s.now().Sub(storedAt)
	if age < 0 {
		age = 0
	}
	wh.Set("Age", strconv.FormatInt(int64(age/time.Second), 10))
	wh.Set(CacheStatusHeader, status)
	rec := &statusRecorder{ResponseWriter: w}
	http.ServeContent(rec, r, "", modTime, bytes.NewReader(body))
	s.count1(e.rule, func(st *RuleStats) {
		count(st)
		st.BytesServed += rec.bytes
	})
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	return rec.code
}
