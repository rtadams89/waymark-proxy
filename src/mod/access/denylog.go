package access

// Logs connections refused on Layer 4 rules: the first per address and
// rule at once, later ones once a minute with a count.

import (
	"fmt"
	"sync"
	"time"
)

type denyLogEntry struct {
	last    time.Time
	pending int
}

type denyLogger struct {
	mu      sync.Mutex
	entries map[string]*denyLogEntry
}

// LogL4Denied logs a connection or packet refused on a Layer 4 rule.
func (c *Controller) LogL4Denied(ruleName, proto, ip string, d Decision) {
	if c == nil || c.Options == nil {
		return
	}
	key := ruleName + "|" + proto + "|" + ip + "|" + d.ACLID + "|" + d.RuleID
	now := time.Now()
	l := &c.denyLog
	l.mu.Lock()
	if l.entries == nil {
		l.entries = map[string]*denyLogEntry{}
	}
	if len(l.entries) > 10000 { // drop old entries
		for k, e := range l.entries {
			if now.Sub(e.last) > time.Minute {
				delete(l.entries, k)
			}
		}
	}
	e := l.entries[key]
	if e != nil && now.Sub(e.last) < time.Minute {
		e.pending++
		l.mu.Unlock()
		return
	}
	more := 0
	if e != nil {
		more = e.pending
	}
	l.entries[key] = &denyLogEntry{last: now}
	l.mu.Unlock()

	verb := "denied"
	if d.Action == ActionDrop {
		verb = "dropped"
	}
	msg := fmt.Sprintf("[proto:%s] %s from %s %s by ACL %s", proto, connWord(proto), ip, verb, d.RuleLabel())
	if more > 0 {
		msg += fmt.Sprintf(" (and %d more since the last line)", more)
	}
	c.Options.Logger.PrintAndLog("l4-proxy", "Layer 4 rule "+ruleName+": "+msg, nil)
}

func connWord(proto string) string {
	if proto == "udp" {
		return "packet"
	}
	return "connection"
}
