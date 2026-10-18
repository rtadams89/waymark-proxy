package pathrule

import (
	"os"
	"path/filepath"
	"testing"
)

// Custom fork: blocker UUIDs from API requests never reach the file system
// unless they are well-formed UUIDs.
func TestBlockerFilenameRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(filepath.Dir(dir), "victim-file")
	os.WriteFile(victim, []byte("x"), 0600)
	defer os.Remove(victim)
	h := &Handler{Options: &Options{ConfigFolder: dir}}
	for _, bad := range []string{"../victim-file", "..", "a/b", ""} {
		if err := h.RemoveBlockerFromFile(bad); err == nil {
			t.Fatalf("RemoveBlockerFromFile(%q) accepted", bad)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatal("file outside the config folder was removed")
	}
	good := "0b5c7e0e-6f7a-4a51-9a39-2f1d3b4c5d6e"
	if err := h.SaveBlockerToFile(&BlockingPath{UUID: good}); err != nil {
		t.Fatal(err)
	}
	if err := h.RemoveBlockerFromFile(good); err != nil {
		t.Fatal(err)
	}
}
