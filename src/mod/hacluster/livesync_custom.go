package hacluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// LiveConfEntries are synced config files that take effect without a
// restart (they are read again whenever they change on disk), so a
// standby can apply them as soon as they change.
var LiveConfEntries = []string{
	"ui.json", // status page panels
}

func isLiveEntry(e string) bool {
	for _, l := range LiveConfEntries {
		if l == e {
			return true
		}
	}
	return false
}

// LiveChanges compares this node's content with the active node's. If
// they differ only in LiveConfEntries it returns those entries and true;
// if anything else differs it returns false.
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

// ApplyLiveEntries writes (or removes) the given LiveConfEntries files from
// the active node's content.
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
