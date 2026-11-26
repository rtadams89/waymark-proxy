package dynamicproxy

import (
	"net/http/httptest"
	"testing"
)

func TestTrailingSlashLocation(t *testing.T) {
	cases := map[string]string{
		"/app":           "/app/",
		"/app?x=1&y=2":   "/app/?x=1&y=2",
		"//evil.example": "/evil.example/",
		"/%5Cevil":       "/%5Cevil/",
		"/a%20b":         "/a%20b/",
	}
	for in, want := range cases {
		if got := trailingSlashLocation(httptest.NewRequest("GET", in, nil)); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}
