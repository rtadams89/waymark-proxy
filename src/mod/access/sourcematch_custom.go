package access

// Matching a single ACL rule source.

import (
	"errors"
	"strings"
)

// NormalizeSource checks a source and returns it in canonical form.
func (c *Controller) NormalizeSource(s Source) (Source, error) {
	switch s.Type {
	case SourceObject:
		c.mu.RLock()
		_, ok := c.objectsWithBuiltIns()[s.Value]
		c.mu.RUnlock()
		if !ok {
			return s, errors.New("the object does not exist")
		}
	case SourceIP, SourceCIDR, SourceRange, SourceWildcard, SourceCountry:
		v, err := NormalizeValue(s.Type, s.Value)
		if err != nil {
			return s, err
		}
		s.Value = v
	case SourceAny:
		return s, errors.New("choose an address, object or country")
	default:
		return s, errors.New("unknown source type " + s.Type)
	}
	return s, nil
}

// SourceMatches reports whether ip matches s. known is false when the
// match depends on a country that cannot be looked up.
func (c *Controller) SourceMatches(s Source, ip string) (matched bool, known bool) {
	if c == nil {
		return false, true
	}
	var m *matcher
	if s.Type == SourceObject {
		c.mu.RLock()
		m = c.objMatchers[s.Value]
		c.mu.RUnlock()
		if m == nil {
			m = &matcher{}
		}
	} else {
		m = &matcher{}
		m.add(s.Type, s.Value)
	}
	matched, known = m.match(c.newEvalCtx(strings.TrimSpace(ip)))
	if !known {
		return false, false
	}
	return matched != s.Not, true
}

// ObjectName returns an object's name, or id if there is no such object.
func (c *Controller) ObjectName(id string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if o := c.objectsWithBuiltIns()[id]; o != nil {
		return o.Name
	}
	return id
}
