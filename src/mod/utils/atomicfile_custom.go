package utils

// WriteFileAtomic replaces a file so that a crash or power cut leaves either
// the old content or the new: the data goes to a temporary file in the same
// folder, which is flushed to disk, renamed over the target, and then the
// folder itself is flushed.

import (
	"os"
	"path/filepath"
	"runtime"
)

// WriteFileAtomic writes data to path durably and atomically, with exactly
// the permission bits perm (not reduced by the umask). If path is a
// symbolic link, the file it points to is replaced.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	keep = true
	syncDir(dir)
	return nil
}

// syncDir flushes a folder's entries (the rename) to disk; a no-op on Windows.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
