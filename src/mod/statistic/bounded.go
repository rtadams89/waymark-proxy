package statistic

import (
	"sort"
	"sync"
	"sync/atomic"
)

// The cap comes from CollectorOption.MaxEntriesPerStatMap (-stats_max_entries).
// 0 disables it (unboundedIncr in statistic.go).
//
// The cap is soft: when a new-key insert pushes the map past it, the 10% of
// entries with the lowest request counts are dropped.
//
// Recommended value when enabling: 20000 (~32MB worst case across all 8 maps).

// boundedCounter is a sidecar to a *sync.Map[string]int that tracks its
// logical size atomically and serializes trim (eviction) operations.
type boundedCounter struct {
	size   atomic.Int64
	trimMu sync.Mutex
}

func newBoundedCounter(initialSize int) *boundedCounter {
	b := &boundedCounter{}
	b.size.Store(int64(initialSize))
	return b
}

// boundedCounters is the per-DailySummary set of size sidecars, one per
// sync.Map field in DailySummary.
type boundedCounters struct {
	ForwardTypes        *boundedCounter
	RequestOrigin       *boundedCounter
	RequestClientIp     *boundedCounter
	Referer             *boundedCounter
	UserAgent           *boundedCounter
	RequestURL          *boundedCounter
	RequestMethods      *boundedCounter
	DownstreamHostnames *boundedCounter
	UpstreamHostnames   *boundedCounter
}

func newBoundedCounters() boundedCounters {
	return boundedCounters{
		ForwardTypes:        newBoundedCounter(0),
		RequestOrigin:       newBoundedCounter(0),
		RequestClientIp:     newBoundedCounter(0),
		Referer:             newBoundedCounter(0),
		UserAgent:           newBoundedCounter(0),
		RequestURL:          newBoundedCounter(0),
		RequestMethods:      newBoundedCounter(0),
		DownstreamHostnames: newBoundedCounter(0),
		UpstreamHostnames:   newBoundedCounter(0),
	}
}

// boundedIncr increments the counter for key in m, applying a soft cap of
// capN entries. New keys past the cap trigger a trim that drops the entries
// with the lowest request counts down to ~90% of capN. Safe for concurrent use.
func boundedIncr(m *sync.Map, b *boundedCounter, key string, capN int) {
	// Increment; continue only for a new key
	if !incrKey(m, key) {
		return
	}

	newSize := b.size.Add(1)
	if int(newSize) <= capN {
		return
	}

	// Trim while over the cap; a goroutine that cannot take the trim lock
	// leaves the trim to the one holding it
	for int(b.size.Load()) > capN {
		if !b.trimMu.TryLock() {
			return
		}
		// Re-check under lock — the other trimmer may have just finished.
		if int(b.size.Load()) > capN {
			evictLeastFrequent(m, b, capN)
		}
		b.trimMu.Unlock()
	}
}

// evictLeastFrequent removes the entries in m with the lowest request counts
// until ~90% of capN entries remain. Caller must hold b.trimMu.
func evictLeastFrequent(m *sync.Map, b *boundedCounter, capN int) {
	type entry struct {
		key   string
		count int
	}

	entries := make([]entry, 0, capN+capN/8)
	m.Range(func(k, v interface{}) bool {
		entries = append(entries, entry{key: k.(string), count: v.(int)})
		return true
	})

	target := capN * 9 / 10
	if target < 1 {
		target = 1
	}
	if len(entries) <= target {
		// Fewer entries than expected: reconcile the size counter
		b.size.Store(int64(len(entries)))
		return
	}

	// Ascending by count; evict the front (lowest-frequency) until target.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].count < entries[j].count
	})

	evictCount := len(entries) - target
	for i := 0; i < evictCount; i++ {
		m.Delete(entries[i].key)
	}
	b.size.Add(-int64(evictCount))
}
