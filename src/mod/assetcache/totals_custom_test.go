package assetcache

import "testing"

func TestTotalsSurvivePurge(t *testing.T) {
	s := NewStore(1 << 20)
	s.count1("a.example", func(st *RuleStats) { st.Hits += 3; st.Misses++ })
	s.Purge("a.example")
	if st := s.Stats("a.example"); st.Hits != 0 {
		t.Fatalf("purge should reset the session counters: %+v", st)
	}
	tot := s.AllTotals()["a.example"]
	if tot.Hits != 3 || tot.Misses != 1 || tot.HitRate() != 0.75 {
		t.Fatalf("totals after purge: %+v rate %v", tot, tot.HitRate())
	}
	s.RestoreTotals(map[string]Totals{"A.Example": {Hits: 1, Misses: 1}})
	if tot := s.AllTotals()["a.example"]; tot.Hits != 4 || tot.Misses != 2 {
		t.Fatalf("restore adds: %+v", tot)
	}
	s.ForgetTotals("a.example")
	if _, ok := s.AllTotals()["a.example"]; ok {
		t.Fatal("forget should drop the rule")
	}
	if (Totals{}).HitRate() != -1 {
		t.Fatal("no requests: -1")
	}
}
