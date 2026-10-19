package loadbalance

/*
	leastconn_custom.go (custom fork)

	Load balancing methods shared with Layer 4 rules:
	- weighted (the default): a random destination, in proportion to its
	  weight
	- leastconn: the destination with the fewest requests in progress for
	  its weight (requests / weight), so a destination with weight 2 is
	  given twice as many as one with weight 1

	Destinations with weight 0 are backups: they are only used when no
	destination with a weight above 0 is available.

	Requests in progress are counted per destination address, across rules,
	from the moment one is picked until its response (or WebSocket) ends.
*/

import (
	"math/rand"
	"sync/atomic"
)

// Load balancing methods (ProxyEndpoint.LoadBalancingMode).
const (
	LBWeighted  = "weighted"
	LBLeastConn = "leastconn"
)

// ValidLoadBalancingMode reports whether mode is a known method ("" is
// weighted).
func ValidLoadBalancingMode(mode string) bool {
	return mode == "" || mode == LBWeighted || mode == LBLeastConn
}

func (m *RouteManager) inflightCounter(addr string) *atomic.Int64 {
	if v, ok := m.inflight.Load(addr); ok {
		return v.(*atomic.Int64)
	}
	v, _ := m.inflight.LoadOrStore(addr, &atomic.Int64{})
	return v.(*atomic.Int64)
}

// Track counts a request in progress to addr. Call the returned function
// when the request ends.
func (m *RouteManager) Track(addr string) func() {
	c := m.inflightCounter(addr)
	c.Add(1)
	done := false
	return func() {
		if !done {
			done = true
			c.Add(-1)
		}
	}
}

// InFlight returns the number of requests in progress to addr.
func (m *RouteManager) InFlight(addr string) int64 {
	return m.inflightCounter(addr).Load()
}

// pickUpstream chooses a destination from origins (all usable) with the
// given method, and returns it with its index in origins.
func (m *RouteManager) pickUpstream(origins []*Upstream, mode string) (*Upstream, int, error) {
	if mode != LBLeastConn || len(origins) == 1 {
		return getRandomUpstreamByWeight(origins)
	}
	pick := func(primary bool) (*Upstream, int) {
		best, bestIdx := (*Upstream)(nil), -1
		var bestScore float64
		ties := 0
		for i, o := range origins {
			if (o.Weight > 0) != primary {
				continue
			}
			w := o.Weight
			if w <= 0 {
				w = 1
			}
			score := float64(m.InFlight(o.OriginIpOrDomain)+1) / float64(w)
			switch {
			case best == nil || score < bestScore:
				best, bestIdx, bestScore, ties = o, i, score, 1
			case score == bestScore:
				// pick evenly among equally loaded destinations
				ties++
				if rand.Intn(ties) == 0 {
					best, bestIdx = o, i
				}
			}
		}
		return best, bestIdx
	}
	if o, i := pick(true); o != nil {
		return o, i, nil
	}
	if o, i := pick(false); o != nil {
		return o, i, nil
	}
	return getRandomUpstreamByWeight(origins)
}
