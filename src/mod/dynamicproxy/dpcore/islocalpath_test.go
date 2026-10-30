package dpcore

import "testing"

func TestIsLocalPath(t *testing.T) {
	cases := map[string]bool{
		"/":                  true,
		"/app/x?y=1":         true,
		"//evil.example/":    false,
		"/\\evil.example/":   false,
		"https://a.example/": false,
		"relative":           false,
		"":                   false,
	}
	for in, want := range cases {
		if got := isLocalPath(in); got != want {
			t.Errorf("%q: got %v, want %v", in, got, want)
		}
	}
}
