package auth

import (
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	db "imuslab.com/zoraxy/mod/database"
)

func TestSetOrCreateAccount(t *testing.T) {
	d, err := db.NewDatabase(filepath.Join(t.TempDir(), "sys.db"), db.GetRecommendedBackendType())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if got := ListUsernames(d); len(got) != 0 {
		t.Fatalf("users in an empty database: %v", got)
	}
	created, err := SetOrCreateAccount(d, "ryan", "first")
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	d.Write("auth", "sessionkey", "old-key")
	created, err = SetOrCreateAccount(d, "ryan", "second")
	if err != nil || created {
		t.Fatalf("change: %v %v", created, err)
	}
	if d.KeyExists("auth", "sessionkey") {
		t.Fatal("the session key was kept")
	}
	var hash string
	d.Read("auth", "passhash/ryan", &hash)
	if ok, _ := PasswordMatches(hash, "second"); !ok {
		t.Fatal("the new password does not match")
	}
	if ok, _ := PasswordMatches(hash, "first"); ok {
		t.Fatal("the old password still matches")
	}
	SetOrCreateAccount(d, "alice", "pw")
	if got := ListUsernames(d); !reflect.DeepEqual(got, []string{"alice", "ryan"}) {
		t.Fatalf("usernames: %v", got)
	}

	for _, bad := range [][2]string{{"", "pw"}, {" ryan", "pw"}, {"a/b", "pw"}, {"a\nb", "pw"}, {strings.Repeat("x", 65), "pw"}, {"ok", ""}, {"ok", strings.Repeat("p", 73)}} {
		if _, err := SetOrCreateAccount(d, bad[0], bad[1]); err == nil {
			t.Errorf("accepted %q / %d-byte password", bad[0], len(bad[1]))
		}
	}
	if got := ListUsernames(d); len(got) != 2 {
		t.Fatalf("a rejected account was saved: %v", got)
	}
}

func TestRequestUsesHTTPS(t *testing.T) {
	cases := []struct {
		remote, proto string
		want          bool
	}{
		{"127.0.0.1:5000", "https", true},
		{"[::1]:5000", "HTTPS", true},
		{"127.0.0.1:5000", "http", false},
		{"127.0.0.1:5000", "", false},
		{"192.168.1.5:5000", "https", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		if got := requestUsesHTTPS(r); got != c.want {
			t.Errorf("%s %q: got %v", c.remote, c.proto, got)
		}
	}
}
