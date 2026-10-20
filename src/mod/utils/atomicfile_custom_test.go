package utils

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "conf.json")
	if err := WriteFileAtomic(p, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("two"), 0644); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "two" {
		t.Fatalf("content %q", b)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(p)
		if st.Mode().Perm() != 0644 {
			t.Fatalf("mode %v", st.Mode().Perm())
		}
		// a symlink is followed, not replaced
		link := filepath.Join(dir, "link.json")
		os.Symlink(p, link)
		if err := WriteFileAtomic(link, []byte("three"), 0644); err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
			t.Fatal("symlink was replaced")
		}
		if b, _ := os.ReadFile(p); string(b) != "three" {
			t.Fatalf("target content %q", b)
		}
	}
	// no temporary files left behind
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "conf.json" && e.Name() != "link.json" {
			t.Fatalf("left over: %s", e.Name())
		}
	}
	// a missing folder fails without creating anything
	if err := WriteFileAtomic(filepath.Join(dir, "nope", "x.json"), []byte("x"), 0644); err == nil {
		t.Fatal("expected an error")
	}
}
