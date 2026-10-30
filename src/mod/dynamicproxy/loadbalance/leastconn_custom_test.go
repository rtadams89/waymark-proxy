package loadbalance

import "testing"

// TestPickLeastConn checks picking by requests in progress per weight, with
// backups (weight 0) used last.
func TestPickLeastConn(t *testing.T) {
	m := &RouteManager{}
	a := &Upstream{OriginIpOrDomain: "a:80", Weight: 2}
	b := &Upstream{OriginIpOrDomain: "b:80", Weight: 1}
	backup := &Upstream{OriginIpOrDomain: "c:80", Weight: 0}
	origins := []*Upstream{a, b, backup}
	count := map[string]int{}
	for i := 0; i < 6; i++ {
		u, _, err := m.pickUpstream(origins, LBLeastConn)
		if err != nil {
			t.Fatal(err)
		}
		count[u.OriginIpOrDomain]++
		m.Track(u.OriginIpOrDomain) // still in progress
	}
	if count["a:80"] != 4 || count["b:80"] != 2 || count["c:80"] != 0 {
		t.Fatalf("weighted least connections: %v", count)
	}
	done := m.Track("x:80")
	done()
	done()
	if n := m.InFlight("x:80"); n != 0 {
		t.Fatalf("in flight after release: %d", n)
	}
	if u, _, _ := m.pickUpstream([]*Upstream{backup}, LBLeastConn); u != backup {
		t.Fatal("only backup not used")
	}
	if !ValidLoadBalancingMode("") || !ValidLoadBalancingMode(LBLeastConn) || ValidLoadBalancingMode("roundrobin") {
		t.Fatal("ValidLoadBalancingMode")
	}
}
