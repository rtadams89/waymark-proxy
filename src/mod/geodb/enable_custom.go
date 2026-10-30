package geodb

// GeoIP lookups can be turned off. While off, the dataset is unloaded, every
// lookup returns an empty country code, and scheduled database downloads are
// skipped.

// Enabled reports whether GeoIP lookups are on.
func (s *Store) Enabled() bool {
	return !s.disabled.Load()
}

// SetEnabled turns GeoIP lookups on or off at runtime.
func (s *Store) SetEnabled(enabled bool) error {
	if enabled {
		if !s.disabled.Load() {
			return nil
		}
		dataset, err := s.loadDataSet()
		if err != nil {
			return err
		}
		s.dataset.Store(dataset)
		s.disabled.Store(false)
		return nil
	}
	s.disabled.Store(true)
	s.dataset.Store(nil)
	s.slowLookupCacheIpv4.Clear()
	s.slowLookupCacheIpv6.Clear()
	return nil
}
