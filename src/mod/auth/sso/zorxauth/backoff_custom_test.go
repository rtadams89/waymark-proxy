package zorxauth

import (
	"testing"
	"time"
)

func TestLoginBackoff(t *testing.T) {
	cases := map[int64]time.Duration{0: 0, 1: 100 * time.Millisecond, 2: 200 * time.Millisecond, 9: 25600 * time.Millisecond, 10: 30 * time.Second, 65: 30 * time.Second, 1000: 30 * time.Second}
	for n, want := range cases {
		if got := loginBackoff(n); got != want {
			t.Errorf("%d failures: got %s, want %s", n, got, want)
		}
	}
}
