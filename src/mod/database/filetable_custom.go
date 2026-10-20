package database

/*
	filetable_custom.go (custom fork): database tables kept in JSON files.

	A table registered with UseFileTable is stored in its own JSON file in
	the config folder instead of the database file: one object with a
	member per key, the value being what the database would have stored.
	Handlers keep calling Read, Write and the rest as before; the routing
	is done here.

	The file is written atomically on every change. It is read again
	whenever it changes on disk (modification time or size), so a copy
	put in place by HA sync or by hand is picked up.

	Behaviour matches the bolt backend: reading a key that is not there
	returns no error and leaves the destination unchanged.
*/

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"imuslab.com/zoraxy/mod/utils"
)

type fileTable struct {
	path string
	perm os.FileMode

	mu     sync.Mutex
	rows   map[string]json.RawMessage
	loaded bool
	mod    time.Time
	size   int64
}

var (
	fileTablesMu sync.RWMutex
	fileTables   = map[*Database]map[string]*fileTable{}
)

func (d *Database) fileTable(table string) *fileTable {
	fileTablesMu.RLock()
	defer fileTablesMu.RUnlock()
	return fileTables[d][table]
}

// UseFileTable keeps table in the JSON file at path (created with perm)
// from now on. Rows already in the database are moved into the file, where
// they replace rows with the same key, and the database table is dropped.
func (d *Database) UseFileTable(table, path string, perm os.FileMode) error {
	if d.fileTable(table) != nil {
		return fmt.Errorf("table %s is already kept in a file", table)
	}
	ft := &fileTable{path: path, perm: perm}
	if err := os.MkdirAll(filepath.Dir(path), 0775); err != nil {
		return err
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if err := ft.refresh(); err != nil {
		return err
	}

	if d.Backend != nil && d.Backend.TableExists(table) {
		entries, err := d.Backend.ListTable(table)
		if err != nil {
			return fmt.Errorf("reading table %s for the move to %s: %w", table, path, err)
		}
		moved := 0
		for _, kv := range entries {
			if len(kv) != 2 || !json.Valid(kv[1]) {
				continue
			}
			ft.rows[string(kv[0])] = json.RawMessage(append([]byte{}, kv[1]...))
			moved++
		}
		if moved > 0 || !ft.exists() {
			if err := ft.save(); err != nil {
				return fmt.Errorf("moving table %s to %s: %w", table, path, err)
			}
		}
		if err := d.Backend.DropTable(table); err != nil {
			return fmt.Errorf("removing table %s after the move to %s: %w", table, path, err)
		}
	}

	fileTablesMu.Lock()
	if fileTables[d] == nil {
		fileTables[d] = map[string]*fileTable{}
	}
	fileTables[d][table] = ft
	fileTablesMu.Unlock()
	return nil
}

// FileTablePath returns the file a table is kept in, or "" if it is in the
// database.
func (d *Database) FileTablePath(table string) string {
	if ft := d.fileTable(table); ft != nil {
		return ft.path
	}
	return ""
}

func (ft *fileTable) exists() bool {
	_, err := os.Stat(ft.path)
	return err == nil
}

// refresh loads the file if it changed since it was last read. Call with
// ft.mu held.
func (ft *fileTable) refresh() error {
	info, err := os.Stat(ft.path)
	if errors.Is(err, os.ErrNotExist) {
		if !ft.loaded || ft.size != -1 {
			ft.rows = map[string]json.RawMessage{}
			ft.loaded, ft.mod, ft.size = true, time.Time{}, -1
		}
		return nil
	}
	if err != nil {
		return err
	}
	if ft.loaded && info.ModTime().Equal(ft.mod) && info.Size() == ft.size {
		return nil
	}
	data, err := os.ReadFile(ft.path)
	if err != nil {
		return err
	}
	rows := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &rows); err != nil {
			return fmt.Errorf("%s is not valid JSON: %w", ft.path, err)
		}
	}
	if rows == nil { // the file says null
		rows = map[string]json.RawMessage{}
	}
	ft.rows, ft.loaded, ft.mod, ft.size = rows, true, info.ModTime(), info.Size()
	return nil
}

// save writes the rows to the file. Call with ft.mu held.
func (ft *fileTable) save() error {
	data, err := json.MarshalIndent(ft.rows, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := utils.WriteFileAtomic(ft.path, data, ft.perm); err != nil {
		return err
	}
	if info, err := os.Stat(ft.path); err == nil {
		ft.mod, ft.size = info.ModTime(), info.Size()
	} else {
		ft.loaded = false
	}
	return nil
}

func (ft *fileTable) write(key string, value interface{}) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if err := ft.refresh(); err != nil {
		return err // never overwrite a file that could not be read
	}
	old, had := ft.rows[key]
	ft.rows[key] = raw
	if err := ft.save(); err != nil {
		if had {
			ft.rows[key] = old
		} else {
			delete(ft.rows, key)
		}
		return err
	}
	return nil
}

func (ft *fileTable) read(key string, assignee interface{}) error {
	ft.mu.Lock()
	err := ft.refresh()
	raw, ok := ft.rows[key]
	ft.mu.Unlock()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return json.Unmarshal(raw, assignee)
}

func (ft *fileTable) keyExists(key string) bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if ft.refresh() != nil {
		return false
	}
	_, ok := ft.rows[key]
	return ok
}

func (ft *fileTable) delete(key string) error {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if err := ft.refresh(); err != nil {
		return err
	}
	old, ok := ft.rows[key]
	if !ok {
		return nil
	}
	delete(ft.rows, key)
	if err := ft.save(); err != nil {
		ft.rows[key] = old
		return err
	}
	return nil
}

func (ft *fileTable) drop() error {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if err := ft.refresh(); err != nil {
		return err
	}
	ft.rows = map[string]json.RawMessage{}
	return ft.save()
}

func (ft *fileTable) list() ([][][]byte, error) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if err := ft.refresh(); err != nil {
		return nil, err
	}
	out := make([][][]byte, 0, len(ft.rows))
	for k, v := range ft.rows {
		out = append(out, [][]byte{[]byte(k), append([]byte{}, v...)})
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i][0]) < string(out[j][0]) }) // key order, as bolt lists them
	return out, nil
}
