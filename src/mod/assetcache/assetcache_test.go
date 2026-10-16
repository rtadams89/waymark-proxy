package assetcache

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// upstream is a fake origin: per-path response headers/bodies, request log.
type upstream struct {
	mu       sync.Mutex
	hits     map[string]int
	lastReq  map[string]http.Header
	headers  map[string]http.Header
	bodies   map[string]string
	notMod   map[string]bool // answer 304 when If-None-Match matches ETag
	statuses map[string]int
}

func newUpstream() *upstream {
	return &upstream{hits: map[string]int{}, lastReq: map[string]http.Header{}, headers: map[string]http.Header{},
		bodies: map[string]string{}, notMod: map[string]bool{}, statuses: map[string]int{}}
}

func (u *upstream) set(p string, body string, kv ...string) {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	u.mu.Lock()
	u.headers[p], u.bodies[p] = h, body
	u.mu.Unlock()
}

func (u *upstream) count(p string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits[p]
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	p := r.URL.RequestURI()
	u.hits[p]++
	u.lastReq[p] = r.Header.Clone()
	h, body, notMod, status := u.headers[p], u.bodies[p], u.notMod[p], u.statuses[p]
	u.mu.Unlock()
	for k, v := range h {
		w.Header()[k] = v
	}
	if notMod && h.Get("Etag") != "" && r.Header.Get("If-None-Match") == h.Get("Etag") {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") && h.Get("Vary") != "" {
		w.Header().Set("Content-Encoding", "gzip")
		body = "GZ:" + body
	}
	if status == 0 {
		status = 200
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(status)
	io.WriteString(w, body)
}

type harness struct {
	store *Store
	cfg   *Config
	up    *upstream
	opts  BeginOptions
	clock time.Time
	upErr error
}

func newHarness(t *testing.T) *harness {
	cfg := DefaultConfig()
	cfg.Enabled = true
	h := &harness{store: NewStore(64 << 20), cfg: cfg, up: newUpstream(), clock: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	h.store.now = func() time.Time { return h.clock }
	return h
}

// do sends a request through the cache to the fake upstream, the same way
// dynamicproxy's hostRequest does.
func (h *harness) do(method, target string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://app.test"+target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	var w http.ResponseWriter = httptest.NewRecorder()
	rec := w.(*httptest.ResponseRecorder)
	sess := h.store.Begin(w, r, "App.Test", h.cfg, h.opts)
	if sess != nil {
		if sess.Served() {
			return rec
		}
		w, r = sess, sess.UpstreamRequest()
		defer sess.Finish()
	}
	h.up.ServeHTTP(w, r)
	sess.SetUpstreamResult(0, h.upErr)
	return rec
}

func status(rec *httptest.ResponseRecorder) string { return rec.Header().Get(CacheStatusHeader) }

func TestStaticFileCached(t *testing.T) {
	h := newHarness(t)
	h.up.set("/app.css", "body{}", "Content-Type", "text/css", "Etag", `"v1"`)
	r1 := h.do("GET", "/app.css")
	if r1.Code != 200 || status(r1) != "MISS" || r1.Body.String() != "body{}" {
		t.Fatalf("first: %d %q %q", r1.Code, status(r1), r1.Body.String())
	}
	h.clock = h.clock.Add(90 * time.Second)
	r2 := h.do("GET", "/app.css")
	if r2.Code != 200 || status(r2) != "HIT" || r2.Body.String() != "body{}" || h.up.count("/app.css") != 1 {
		t.Fatalf("second: %d %q %q upstream=%d", r2.Code, status(r2), r2.Body.String(), h.up.count("/app.css"))
	}
	if r2.Header().Get("Content-Type") != "text/css" || r2.Header().Get("Age") != "90" || r2.Header().Get("Etag") != `"v1"` {
		t.Fatalf("hit headers: %v", r2.Header())
	}
	st := h.store.Stats("app.test")
	if st.Hits != 1 || st.Misses != 1 || st.Entries != 1 || st.BytesServed != 6 {
		t.Fatalf("stats: %+v", st)
	}
	// HEAD is answered from the stored GET
	r3 := h.do("HEAD", "/app.css")
	if r3.Code != 200 || status(r3) != "HIT" || r3.Body.Len() != 0 || h.up.count("/app.css") != 1 {
		t.Fatalf("head: %d %q", r3.Code, status(r3))
	}
}

func TestPagesAndAPIsNotCached(t *testing.T) {
	h := newHarness(t)
	h.up.set("/index.html", "<html>", "Content-Type", "text/html")
	h.up.set("/api/items", "[]", "Content-Type", "application/json")
	h.up.set("/", "<html>", "Content-Type", "text/html")
	for _, p := range []string{"/index.html", "/api/items", "/"} {
		h.do("GET", p)
		r := h.do("GET", p)
		if status(r) != "" || h.up.count(p) != 2 {
			t.Fatalf("%s: status %q, upstream %d", p, status(r), h.up.count(p))
		}
	}
	if u := h.store.Usage(); u.Entries != 0 {
		t.Fatalf("stored %d entries", u.Entries)
	}
}

func TestExtensionlessByContentType(t *testing.T) {
	h := newHarness(t)
	h.up.set("/avatar?id=3", "PNG", "Content-Type", "image/png")
	h.do("GET", "/avatar?id=3")
	if r := h.do("GET", "/avatar?id=3"); status(r) != "HIT" {
		t.Fatalf("content type match: %q", status(r))
	}
	// A different query string is a different file
	h.up.set("/avatar?id=4", "PNG4", "Content-Type", "image/png")
	if r := h.do("GET", "/avatar?id=4"); status(r) != "MISS" || r.Body.String() != "PNG4" {
		t.Fatalf("query key: %q %q", status(r), r.Body.String())
	}
	h.cfg.MatchContentTypes = false
	h.store.Purge("app.test")
	h.do("GET", "/avatar?id=3")
	if r := h.do("GET", "/avatar?id=3"); status(r) != "" {
		t.Fatalf("matching off: %q", status(r))
	}
}

func TestNotStorable(t *testing.T) {
	h := newHarness(t)
	cases := map[string][]string{
		"/a.js": {"Cache-Control", "no-store"},
		"/b.js": {"Cache-Control", "private, max-age=600"},
		"/c.js": {"Cache-Control", "no-cache"},
		"/d.js": {"Set-Cookie", "s=1"},
		"/e.js": {"Cache-Control", "max-age=0"},
		"/f.js": {"Vary", "*"},
	}
	for p, kv := range cases {
		h.up.set(p, "x", append([]string{"Content-Type", "text/javascript"}, kv...)...)
		r1 := h.do("GET", p)
		r2 := h.do("GET", p)
		if status(r1) != "BYPASS" || status(r2) != "BYPASS" || h.up.count(p) != 2 {
			t.Fatalf("%s %v: %q %q upstream=%d", p, kv, status(r1), status(r2), h.up.count(p))
		}
	}
	if st := h.store.Stats("app.test"); st.Uncacheable != 12 || st.Entries != 0 {
		t.Fatalf("stats: %+v", st)
	}
	// Errors are passed through untouched
	h.up.set("/missing.png", "nope", "Content-Type", "text/plain")
	h.up.statuses["/missing.png"] = 404
	if r := h.do("GET", "/missing.png"); r.Code != 404 || status(r) != "" {
		t.Fatalf("404: %d %q", r.Code, status(r))
	}
}

func TestFreshnessAndRevalidation(t *testing.T) {
	h := newHarness(t)
	h.up.set("/logo.png", "IMG", "Content-Type", "image/png", "Cache-Control", "max-age=60", "Etag", `"abc"`)
	h.up.notMod["/logo.png"] = true
	h.do("GET", "/logo.png")
	h.clock = h.clock.Add(59 * time.Second)
	if r := h.do("GET", "/logo.png"); status(r) != "HIT" {
		t.Fatalf("within max-age: %q", status(r))
	}
	h.clock = h.clock.Add(2 * time.Second)
	r := h.do("GET", "/logo.png")
	if status(r) != "REVALIDATED" || r.Code != 200 || r.Body.String() != "IMG" {
		t.Fatalf("revalidate: %d %q %q", r.Code, status(r), r.Body.String())
	}
	if got := h.up.lastReq["/logo.png"].Get("If-None-Match"); got != `"abc"` || h.up.count("/logo.png") != 2 {
		t.Fatalf("conditional request: %q, upstream %d", got, h.up.count("/logo.png"))
	}
	// Fresh again for another max-age
	h.clock = h.clock.Add(30 * time.Second)
	if r := h.do("GET", "/logo.png"); status(r) != "HIT" {
		t.Fatalf("after revalidation: %q", status(r))
	}
	// Changed upstream: 200 replaces the stored copy
	h.up.set("/logo.png", "IMG2", "Content-Type", "image/png", "Cache-Control", "max-age=60", "Etag", `"def"`)
	h.clock = h.clock.Add(61 * time.Second)
	if r := h.do("GET", "/logo.png"); status(r) != "MISS" || r.Body.String() != "IMG2" {
		t.Fatalf("changed: %q %q", status(r), r.Body.String())
	}
	if r := h.do("GET", "/logo.png"); status(r) != "HIT" || r.Body.String() != "IMG2" {
		t.Fatalf("new copy: %q %q", status(r), r.Body.String())
	}
	if st := h.store.Stats("app.test"); st.Entries != 1 || st.Revalidated != 1 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestDefaultTTLAndExpires(t *testing.T) {
	h := newHarness(t)
	h.cfg.DefaultTTLSeconds = 100
	h.up.set("/a.woff2", "F", "Content-Type", "font/woff2")
	h.do("GET", "/a.woff2")
	h.clock = h.clock.Add(99 * time.Second)
	if r := h.do("GET", "/a.woff2"); status(r) != "HIT" {
		t.Fatalf("default ttl: %q", status(r))
	}
	h.clock = h.clock.Add(2 * time.Second)
	if r := h.do("GET", "/a.woff2"); status(r) != "MISS" {
		t.Fatalf("default ttl expired: %q", status(r))
	}
	h.up.set("/b.css", "B", "Content-Type", "text/css",
		"Date", h.clock.Format(http.TimeFormat), "Expires", h.clock.Add(10*time.Second).Format(http.TimeFormat))
	h.do("GET", "/b.css")
	h.clock = h.clock.Add(11 * time.Second)
	if r := h.do("GET", "/b.css"); status(r) != "MISS" {
		t.Fatalf("expires: %q", status(r))
	}
	// s-maxage wins over max-age for a shared cache
	h.up.set("/c.css", "C", "Content-Type", "text/css", "Cache-Control", "max-age=5, s-maxage=500")
	h.do("GET", "/c.css")
	h.clock = h.clock.Add(100 * time.Second)
	if r := h.do("GET", "/c.css"); status(r) != "HIT" {
		t.Fatalf("s-maxage: %q", status(r))
	}
}

func TestClientConditionalAndRange(t *testing.T) {
	h := newHarness(t)
	h.up.set("/v.mp4", "0123456789", "Content-Type", "video/mp4", "Etag", `"e1"`)
	// Client's conditional is not forwarded on a miss (the cache needs the body)
	r := h.do("GET", "/v.mp4", "If-None-Match", `"e1"`)
	if r.Code != 200 || h.up.lastReq["/v.mp4"].Get("If-None-Match") != "" {
		t.Fatalf("miss with conditional: %d", r.Code)
	}
	r = h.do("GET", "/v.mp4", "If-None-Match", `"e1"`)
	if r.Code != 304 || status(r) != "HIT" {
		t.Fatalf("conditional hit: %d %q", r.Code, status(r))
	}
	r = h.do("GET", "/v.mp4", "Range", "bytes=2-4")
	if r.Code != 206 || r.Body.String() != "234" || status(r) != "HIT" {
		t.Fatalf("range hit: %d %q", r.Code, r.Body.String())
	}
	// A range request for an uncached file goes straight through
	h.up.set("/w.mp4", "abc", "Content-Type", "video/mp4")
	h.do("GET", "/w.mp4", "Range", "bytes=0-1")
	if h.store.Usage().Entries != 1 {
		t.Fatalf("range miss was stored")
	}
}

func TestVaryAcceptEncoding(t *testing.T) {
	h := newHarness(t)
	h.up.set("/app.js", "JS", "Content-Type", "text/javascript", "Vary", "Accept-Encoding")
	h.do("GET", "/app.js", "Accept-Encoding", "gzip, br")
	h.do("GET", "/app.js")
	r1 := h.do("GET", "/app.js", "Accept-Encoding", "br, gzip")
	r2 := h.do("GET", "/app.js")
	if status(r1) != "HIT" || r1.Body.String() != "GZ:JS" || r1.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip variant: %q %q", status(r1), r1.Body.String())
	}
	if status(r2) != "HIT" || r2.Body.String() != "JS" || r2.Header().Get("Content-Encoding") != "" {
		t.Fatalf("identity variant: %q %q", status(r2), r2.Body.String())
	}
	if h.up.count("/app.js") != 2 || h.store.Stats("app.test").Entries != 2 {
		t.Fatalf("variants: upstream %d", h.up.count("/app.js"))
	}
	if normalizeVaryValue("Accept-Encoding", []string{"gzip;q=0, br;q=0.5"}) != "br" {
		t.Fatalf("q values")
	}
}

func TestLimitsAndEviction(t *testing.T) {
	h := newHarness(t)
	h.cfg.MaxFileMB = 1
	big := strings.Repeat("x", 1<<20+1)
	h.up.set("/big.png", big, "Content-Type", "image/png")
	if r := h.do("GET", "/big.png"); status(r) != "BYPASS" || r.Body.Len() != len(big) {
		t.Fatalf("too big: %q", status(r))
	}
	h.store.SetMaxBytes(3 * (1000 + entryOverhead + 200))
	for i := 0; i < 5; i++ {
		p := fmt.Sprintf("/f%d.png", i)
		h.up.set(p, strings.Repeat("y", 1000), "Content-Type", "image/png")
		h.do("GET", p)
		if i == 3 {
			h.do("GET", "/f0.png") // keep f0 recently used
		}
	}
	u := h.store.Usage()
	if u.Entries != 3 || u.Bytes > u.MaxBytes {
		t.Fatalf("eviction: %+v", u)
	}
	if r := h.do("GET", "/f0.png"); status(r) != "HIT" {
		t.Fatalf("LRU kept recently used file: %q", status(r))
	}
	if r := h.do("GET", "/f1.png"); status(r) != "MISS" {
		t.Fatalf("LRU evicted oldest: %q", status(r))
	}
}

func TestPurgeAndRules(t *testing.T) {
	h := newHarness(t)
	h.up.set("/a.css", "A", "Content-Type", "text/css")
	h.do("GET", "/a.css")
	h.do("GET", "/a.css")
	other := h.store.Begin(httptest.NewRecorder(), httptest.NewRequest("GET", "http://app.test/a.css", nil), "other.test", h.cfg, BeginOptions{})
	if other == nil || other.Served() {
		t.Fatalf("rules must not share entries")
	}
	h.store.Purge("APP.TEST")
	if st := h.store.Stats("app.test"); st != (RuleStats{}) {
		t.Fatalf("purge stats: %+v", st)
	}
	if r := h.do("GET", "/a.css"); status(r) != "MISS" {
		t.Fatalf("after purge: %q", status(r))
	}
}

func TestBypassCases(t *testing.T) {
	h := newHarness(t)
	h.up.set("/a.css", "A", "Content-Type", "text/css")
	h.do("GET", "/a.css")
	// Hard reload refetches (and refreshes the stored copy)
	if r := h.do("GET", "/a.css", "Cache-Control", "no-cache"); status(r) != "MISS" || h.up.count("/a.css") != 2 {
		t.Fatalf("hard reload: %q", status(r))
	}
	// Upstream credentials: never shared
	h.up.set("/p.css", "P", "Content-Type", "text/css")
	h.do("GET", "/p.css", "Authorization", "Bearer x")
	h.do("GET", "/p.css", "Authorization", "Bearer x")
	if h.up.count("/p.css") != 2 {
		t.Fatalf("authorization bypass")
	}
	// ...unless they are Zoraxy's own basic auth
	h.opts.AuthorizationIsZoraxys = true
	h.do("GET", "/p.css", "Authorization", "Basic eDp5")
	if r := h.do("GET", "/p.css", "Authorization", "Basic eDp5"); status(r) != "HIT" {
		t.Fatalf("zoraxy basic auth: %q", status(r))
	}
	// Failed transfers are not stored
	h.upErr = errors.New("connection reset")
	h.up.set("/e.css", "E", "Content-Type", "text/css")
	h.do("GET", "/e.css")
	h.upErr = nil
	if r := h.do("GET", "/e.css"); status(r) != "MISS" {
		t.Fatalf("upstream error stored: %q", status(r))
	}
	// Caching off
	h.cfg.Enabled = false
	if r := h.do("GET", "/a.css"); status(r) != "" {
		t.Fatalf("disabled: %q", status(r))
	}
	// Other methods
	h.cfg.Enabled = true
	if s := h.store.Begin(httptest.NewRecorder(), httptest.NewRequest("POST", "http://app.test/a.css", nil), "app.test", h.cfg, BeginOptions{}); s != nil {
		t.Fatalf("POST got a session")
	}
}

func TestConfigValidate(t *testing.T) {
	c := DefaultConfig()
	c.Extensions = []string{" .CSS", "*.js", "css", ""}
	v, err := c.Validate()
	if err != nil || strings.Join(v.Extensions, ",") != "css,js" {
		t.Fatalf("normalise: %v %v", v, err)
	}
	for _, bad := range []func(*Config){
		func(c *Config) { c.Extensions = []string{"c/ss"} },
		func(c *Config) { c.DefaultTTLSeconds = 0 },
		func(c *Config) { c.MaxFileMB = 0 },
		func(c *Config) { c.Enabled, c.Extensions, c.MatchContentTypes = true, nil, false },
	} {
		c := DefaultConfig()
		bad(c)
		if _, err := c.Validate(); err == nil {
			t.Fatalf("expected error for %+v", c)
		}
	}
}
