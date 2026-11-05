package hacluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// LiveConfEntries are synced config files a standby applies without a
// restart.
var LiveConfEntries = []string{
	"ui.json",
}

func isLiveEntry(e string) bool {
	for _, l := range LiveConfEntries {
		if l == e {
			return true
		}
	}
	return false
}

// LiveChanges returns the LiveConfEntries that differ and true when nothing
// else differs.
func LiveChanges(local, incoming *SyncContent) ([]string, bool) {
	tl, _ := json.Marshal(local.Tables)
	ti, _ := json.Marshal(incoming.Tables)
	if !bytes.Equal(tl, ti) {
		return nil, false
	}
	changed := []string{}
	seen := map[string]bool{}
	check := func(rel string) bool {
		if seen[rel] {
			return true
		}
		seen[rel] = true
		a, inA := local.Files[rel]
		b, inB := incoming.Files[rel]
		if inA == inB && a.Mode == b.Mode && bytes.Equal(a.Data, b.Data) {
			return true
		}
		if !isLiveEntry(rel) {
			return false
		}
		changed = append(changed, rel)
		return true
	}
	for rel := range local.Files {
		if !check(rel) {
			return nil, false
		}
	}
	for rel := range incoming.Files {
		if !check(rel) {
			return nil, false
		}
	}
	return changed, true
}

// ApplyLiveEntries writes or removes the given entries from incoming.
func ApplyLiveEntries(confDir string, incoming *SyncContent, entries []string) error {
	for _, e := range entries {
		if !isLiveEntry(e) {
			return errors.New("not a live entry: " + e)
		}
		target := filepath.Join(confDir, filepath.FromSlash(e))
		f, ok := incoming.Files[e]
		if !ok {
			if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		if err := writeSyncFile(target, f); err != nil {
			return err
		}
	}
	return nil
}
