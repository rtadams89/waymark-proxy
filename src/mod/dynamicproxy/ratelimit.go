package dynamicproxy

import (
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// RequestCountPerIpTable counts requests per IP.
type RequestCountPerIpTable struct {
	table sync.Map
}

// Increment the count of requests for a given IP
func (t *RequestCountPerIpTable) Increment(ip string) {
	v, _ := t.table.LoadOrStore(ip, new(int64))
	atomic.AddInt64(v.(*int64), 1)
}

// Check if the IP is in the table and if it is, check if the count is less than the limit
func (t *RequestCountPerIpTable) Exceeded(ip string, limit int64) bool {
	v, ok := t.table.Load(ip)
	if !ok {
		return false
	}
	count := atomic.LoadInt64(v.(*int64))
	return count >= limit
}

// Get the count of requests for a given IP
func (t *RequestCountPerIpTable) GetCount(ip string) int64 {
	v, ok := t.table.Load(ip)
	if !ok {
		return 0
	}
	return atomic.LoadInt64(v.(*int64))
}

// Clear the IP table
func (t *RequestCountPerIpTable) Clear() {
	t.table.Range(func(key, value interface{}) bool {
		t.table.Delete(key)
		return true
	})
}

func (h *ProxyHandler) handleRateLimitRouting(w http.ResponseWriter, r *http.Request, pe *ProxyEndpoint) error {
	err := h.Parent.handleRateLimit(w, r, pe)
	if err != nil {
		h.Parent.logRequest(r, false, 429, "ratelimit", r.URL.Hostname(), "", pe)
	}
	return err
}

func (router *Router) handleRateLimit(w http.ResponseWriter, r *http.Request, pe *ProxyEndpoint) error {
	// Client IP from the same trusted-proxy aware resolver as access control
	ip := router.GetClientIPForEndpoint(r, pe)
	if ip == "" {
		// Unresolved client IP: allow
		return nil
	}

	router.rateLimitCounter.Increment(ip)

	if router.rateLimitCounter.Exceeded(ip, int64(pe.RateLimit)) {
		w.WriteHeader(429)
		return errors.New("rate limit exceeded")
	}

	return nil
}

// Start the ticker routine for reseting the rate limit counter every seconds
func (r *Router) startRateLimterCounterResetTicker() error {
	if r.rateLimterStop != nil {
		return errors.New("another rate limiter ticker already running")
	}
	tickerStopChan := make(chan bool)
	r.rateLimterStop = tickerStopChan

	counterResetTicker := time.NewTicker(1 * time.Second)
	go func() {
		for {
			select {
			case <-tickerStopChan:
				r.rateLimterStop = nil
				return
			case <-counterResetTicker.C:
				r.rateLimitCounter.Clear()
			}
		}
	}()

	return nil
}
