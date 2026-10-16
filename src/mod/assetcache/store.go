package assetcache

import (
	"container/list"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Per-entry bookkeeping overhead counted against the memory budget.
const entryOverhead = 512

// Maximum stored variants (Vary) per URL; older ones are dropped.
const maxVariantsPerURL = 8

type entry struct {
	rule     string
	baseKey  string
	vary     []string // canonical request header names the response varies on
	varyVals []string // the request's values for them
	header   http.Header
	body     []byte
	modTime  time.Time
	storedAt time.Time
	expires  time.Time
	size     int64
	elem     *list.Element
}

func (e *entry) validators() (etag, lastModified string) {
	return e.header.Get("Etag"), e.header.Get("Last-Modified")
}

func (e *entry) matches(r *http.Request) bool {
	for i, name := range e.vary {
		if normalizeVaryValue(name, r.Header.Values(name)) != e.varyVals[i] {
			return false
		}
	}
	return true
}

// RuleStats are counters for one proxy rule, since startup or its last purge.
type RuleStats struct {
	Hits        int64 `json:"hits"`        // served from memory
	Revalidated int64 `json:"revalidated"` // expired, confirmed unchanged by the upstream (304), served from memory
	Misses      int64 `json:"misses"`      // static file fetched from the upstream and stored
	Uncacheable int64 `json:"uncacheable"` // static file fetched from the upstream but not storable
	BytesServed int64 `json:"bytesServed"` // body bytes served from memory
	Entries     int64 `json:"entries"`
	Bytes       int64 `json:"bytes"`
}

// Usage is the cache's memory use across all rules.
type Usage struct {
	Entries  int64 `json:"entries"`
	Bytes    int64 `json:"bytes"`
	MaxBytes int64 `json:"maxBytes"`
}

// DayStats are counters across all rules for one calendar day (local time).
type DayStats struct {
	Date        string `json:"date"` // YYYY-MM-DD
	Hits        int64  `json:"hits"`
	Revalidated int64  `json:"revalidated"`
	Misses      int64  `json:"misses"`
	Uncacheable int64  `json:"uncacheable"`
	BytesServed int64  `json:"bytesServed"` // bandwidth saved: body bytes the upstream did not have to send
}

// HitRate is the share of static file requests answered from memory (0-1),
// or -1 when there were none.
func (d DayStats) HitRate() float64 {
	served := d.Hits + d.Revalidated
	total := served + d.Misses + d.Uncacheable
	if total == 0 {
		return -1
	}
	return float64(served) / float64(total)
}

// Store is the shared memory cache. The zero value is not usable; use NewStore.
type Store struct {
	mu        sync.Mutex
	maxBytes  int64
	used      int64
	count     int64
	lru       *list.List // front = most recently used
	byKey     map[string][]*entry
	stats     map[string]*RuleStats
	today     DayStats
	yesterday DayStats
	now       func() time.Time
}

// NewStore returns an empty cache limited to maxBytes.
func NewStore(maxBytes int64) *Store {
	return &Store{
		maxBytes: maxBytes,
		lru:      list.New(),
		byKey:    map[string][]*entry{},
		stats:    map[string]*RuleStats{},
		now:      time.Now,
	}
}

// SetMaxBytes changes the memory limit, evicting files if needed.
func (s *Store) SetMaxBytes(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxBytes = n
	s.evictLocked()
}

// Usage reports memory use across all rules.
func (s *Store) Usage() Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Usage{Entries: s.count, Bytes: s.used, MaxBytes: s.maxBytes}
}

// Stats returns a copy of a rule's counters.
func (s *Store) Stats(rule string) RuleStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.stats[normalizeRule(rule)]; st != nil {
		return *st
	}
	return RuleStats{}
}

// Purge removes all of a rule's files and resets its counters.
func (s *Store) Purge(rule string) {
	rule = normalizeRule(rule)
	s.mu.Lock()
	defer s.mu.Unlock()
	for e := s.lru.Front(); e != nil; {
		next := e.Next()
		if ent := e.Value.(*entry); ent.rule == rule {
			s.removeLocked(ent)
		}
		e = next
	}
	delete(s.stats, rule)
}

func normalizeRule(rule string) string {
	return strings.ToLower(strings.TrimSpace(rule))
}

func (s *Store) statsLocked(rule string) *RuleStats {
	st := s.stats[rule]
	if st == nil {
		st = &RuleStats{}
		s.stats[rule] = st
	}
	return st
}

func (s *Store) count1(rule string, f func(*RuleStats)) {
	s.mu.Lock()
	st := s.statsLocked(rule)
	before := *st
	f(st)
	s.rollDayLocked()
	s.today.Hits += st.Hits - before.Hits
	s.today.Revalidated += st.Revalidated - before.Revalidated
	s.today.Misses += st.Misses - before.Misses
	s.today.Uncacheable += st.Uncacheable - before.Uncacheable
	s.today.BytesServed += st.BytesServed - before.BytesServed
	s.mu.Unlock()
}

func dateOf(t time.Time) string {
	return t.Local().Format("2006-01-02")
}

// rollDayLocked starts a new day's counters after midnight.
func (s *Store) rollDayLocked() {
	today := dateOf(s.now())
	if s.today.Date == today {
		return
	}
	if s.today.Date == dateOf(s.now().AddDate(0, 0, -1)) {
		s.yesterday = s.today
	} else {
		s.yesterday = DayStats{Date: dateOf(s.now().AddDate(0, 0, -1))}
	}
	s.today = DayStats{Date: today}
}

// Daily returns today's and yesterday's counters across all rules.
func (s *Store) Daily() (today, yesterday DayStats) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollDayLocked()
	return s.today, s.yesterday
}

// RestoreDaily loads saved daily counters (after a restart). Counters for
// days other than today and yesterday are ignored.
func (s *Store) RestoreDaily(saved []DayStats) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollDayLocked()
	for _, d := range saved {
		switch d.Date {
		case s.today.Date:
			s.today.Hits += d.Hits
			s.today.Revalidated += d.Revalidated
			s.today.Misses += d.Misses
			s.today.Uncacheable += d.Uncacheable
			s.today.BytesServed += d.BytesServed
		case s.yesterday.Date:
			s.yesterday = d
		}
	}
}

// lookup returns the stored variant matching r (fresh or not) and marks it
// recently used.
func (s *Store) lookup(baseKey string, r *http.Request) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.byKey[baseKey] {
		if e.matches(r) {
			s.lru.MoveToFront(e.elem)
			return e
		}
	}
	return nil
}

// snapshot copies what is needed to serve e outside the lock.
func (s *Store) snapshot(e *entry) (http.Header, []byte, time.Time, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return e.header.Clone(), e.body, e.modTime, e.storedAt
}

func (s *Store) insert(e *entry) {
	e.size = int64(len(e.body)) + headerSize(e.header) + entryOverhead
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.size > s.maxBytes {
		return
	}
	// Replace an existing variant with the same Vary values
	variants := s.byKey[e.baseKey]
	for _, old := range variants {
		if sameVariant(old, e) {
			s.removeLocked(old)
			break
		}
	}
	variants = s.byKey[e.baseKey]
	if len(variants) >= maxVariantsPerURL {
		s.removeLocked(variants[0])
	}
	e.elem = s.lru.PushFront(e)
	s.byKey[e.baseKey] = append(s.byKey[e.baseKey], e)
	s.used += e.size
	s.count++
	st := s.statsLocked(e.rule)
	st.Entries++
	st.Bytes += e.size
	s.evictLocked()
}

// refresh updates a stored entry after a 304 from the upstream.
func (s *Store) refresh(e *entry, header http.Header, expires time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.elem == nil {
		return false // evicted meanwhile
	}
	for _, k := range []string{"Cache-Control", "Expires", "Etag", "Last-Modified", "Vary"} {
		if vals := header.Values(k); len(vals) > 0 {
			e.header[k] = append([]string{}, vals...)
		}
	}
	e.storedAt = s.now()
	e.expires = expires
	if lm, err := http.ParseTime(e.header.Get("Last-Modified")); err == nil {
		e.modTime = lm
	}
	return true
}

func (s *Store) remove(e *entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.elem != nil {
		s.removeLocked(e)
	}
}

func (s *Store) removeLocked(e *entry) {
	if e.elem == nil {
		return
	}
	s.lru.Remove(e.elem)
	e.elem = nil
	variants := s.byKey[e.baseKey]
	for i, v := range variants {
		if v == e {
			variants = append(variants[:i], variants[i+1:]...)
			break
		}
	}
	if len(variants) == 0 {
		delete(s.byKey, e.baseKey)
	} else {
		s.byKey[e.baseKey] = variants
	}
	s.used -= e.size
	s.count--
	if st := s.stats[e.rule]; st != nil {
		st.Entries--
		st.Bytes -= e.size
	}
}

func (s *Store) evictLocked() {
	for s.used > s.maxBytes {
		back := s.lru.Back()
		if back == nil {
			return
		}
		s.removeLocked(back.Value.(*entry))
	}
}

func sameVariant(a, b *entry) bool {
	if len(a.vary) != len(b.vary) {
		return false
	}
	for i := range a.vary {
		if a.vary[i] != b.vary[i] || a.varyVals[i] != b.varyVals[i] {
			return false
		}
	}
	return true
}

func headerSize(h http.Header) int64 {
	var n int64
	for k, vs := range h {
		for _, v := range vs {
			n += int64(len(k) + len(v) + 4)
		}
	}
	return n
}

// varyHeaders lists the request headers a response varies on. A compressed
// response varies on Accept-Encoding even if the upstream forgot to say so.
func varyHeaders(h http.Header) []string {
	names := []string{}
	seen := map[string]bool{}
	add := func(n string) {
		n = http.CanonicalHeaderKey(strings.TrimSpace(n))
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, v := range h.Values("Vary") {
		for _, f := range strings.Split(v, ",") {
			add(f)
		}
	}
	if h.Get("Content-Encoding") != "" {
		add("Accept-Encoding")
	}
	return names
}

func normalizeVaryValue(name string, vals []string) string {
	joined := strings.Join(vals, ",")
	if name != "Accept-Encoding" {
		return strings.TrimSpace(joined)
	}
	// Only whether each common coding is accepted matters
	codings := []string{}
	lower := strings.ToLower(joined)
	for _, c := range []string{"br", "gzip", "zstd", "deflate"} {
		for _, part := range strings.Split(lower, ",") {
			p, q, _ := strings.Cut(strings.TrimSpace(part), ";")
			if strings.TrimSpace(p) == c && !zeroQuality(q) {
				codings = append(codings, c)
				break
			}
		}
	}
	return strings.Join(codings, ",")
}

// zeroQuality reports whether a parameter string like "q=0" or " q=0.000"
// marks a coding as not acceptable.
func zeroQuality(params string) bool {
	for _, p := range strings.Split(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
			q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return err == nil && q == 0
		}
	}
	return false
}

// Rules lists the rules that have stored files or counters.
func (s *Store) Rules() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	rules := []string{}
	for r := range s.stats {
		rules = append(rules, r)
	}
	return rules
}
