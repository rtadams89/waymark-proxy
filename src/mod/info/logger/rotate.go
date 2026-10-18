package logger

import (
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"imuslab.com/zoraxy/mod/utils"
)

type RotateOption struct {
	Enabled    bool   //Whether log rotation is enabled, default false
	MaxSize    int64  //Maximum size of the log file in bytes before rotation (e.g. 10 * 1024 * 1024 for 10MB)
	MaxBackups int    //Maximum number of backup files to keep
	Compress   bool   //Whether to compress the rotated files
	BackupDir  string //Directory to store backup files, if empty, use the same directory as the log file
}

// Stop the log rotation ticker
func (l *Logger) StopLogRotateTicker() {
	if l.logRotateTicker != nil {
		l.logRotateTicker.Stop()
	}
}

// Check if the log file needs rotation
func (l *Logger) LogNeedRotate(filename string) bool {
	if !l.RotateOption.Enabled {
		return false
	}
	info, err := os.Stat(filename)
	if err != nil {
		return false
	}
	return info.Size() >= l.RotateOption.MaxSize
}

// Handle web request trigger log ratation
func (l *Logger) HandleDebugTriggerLogRotation(w http.ResponseWriter, r *http.Request) {
	err := l.RotateLog()
	if err != nil {
		utils.SendErrorResponse(w, "Log rotation error: "+err.Error())
		return
	}
	l.PrintAndLog("logger", "Log rotation triggered via REST API", nil)
	utils.SendOK(w)
}

// ArchiveLog will archive the given log file, use during month change
func (l *Logger) ArchiveLog(filename string) error {
	if l.RotateOption == nil || !l.RotateOption.Enabled {
		return nil
	}

	// Determine backup directory
	backupDir := l.RotateOption.BackupDir
	if backupDir == "" {
		backupDir = filepath.Dir(filename)
	}

	// Ensure backup directory exists
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return err
	}

	// Generate archived filename with timestamp
	timestamp := time.Now().Format("20060102-150405")
	baseName := filepath.Base(filename)
	baseName = baseName[:len(baseName)-len(filepath.Ext(baseName))]
	archivedPath := uniqueBackupPath(backupDir, baseName, timestamp)

	// Rename current log file to archived file
	if err := os.Rename(filename, archivedPath); err != nil {
		return err
	}

	// Optionally compress the archived file
	if l.RotateOption.Compress {
		if err := compressFile(archivedPath); err != nil {
			return err
		}
		os.Remove(archivedPath)
		archivedPath += ".gz"
	}

	if err := l.cleanupOldBackups(backupDir, filename, l.RotateOption.MaxBackups); err != nil {
		return err
	}

	return nil
}

// Execute log rotation
func (l *Logger) RotateLog() error {
	//Custom fork: hold the logger lock while the file is swapped, so no
	//write goes to a closed file, and keep logging (reopen the current file)
	//if the rename fails. Compression and cleanup run after the lock is
	//released.
	mu := l.lock()
	mu.Lock()
	if l.RotateOption == nil || !l.RotateOption.Enabled || l.CurrentLogFile == "" {
		mu.Unlock()
		return nil
	}
	opt := *l.RotateOption
	current := l.CurrentLogFile
	if !l.LogNeedRotate(current) {
		mu.Unlock()
		return nil
	}

	backupDir := opt.BackupDir
	if backupDir == "" {
		backupDir = filepath.Dir(current)
	}
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		mu.Unlock()
		return err
	}

	//Custom fork: a log file that is not open (file logging is off) is
	//rotated but not created again; it is opened when a line is written
	wasOpen := l.file != nil
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			mu.Unlock()
			return err
		}
		l.file = nil
		l.logger = nil
	}

	timestamp := time.Now().Format("20060102-150405")
	baseName := filepath.Base(current)
	baseName = baseName[:len(baseName)-len(filepath.Ext(baseName))]
	rotatedPath := uniqueBackupPath(backupDir, baseName, timestamp)
	renameErr := os.Rename(current, rotatedPath)

	if wasOpen {
		flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
		if renameErr == nil {
			flags |= os.O_TRUNC
		}
		file, err := os.OpenFile(current, flags, 0640)
		if err != nil {
			mu.Unlock()
			return err
		}
		l.file = file
		l.logger = log.New(file, "", 0)
	}
	mu.Unlock()

	if renameErr != nil {
		return renameErr
	}
	l.PrintAndLog("logger", "Rotated log file to "+filepath.Base(rotatedPath), nil)

	if opt.Compress {
		if err := compressFile(rotatedPath); err != nil {
			return err
		}
		os.Remove(rotatedPath)
	}
	return l.cleanupOldBackups(backupDir, current, opt.MaxBackups)
}

func (l *Logger) cleanupOldBackups(backupDir string, excludeFile string, maxBackups int) error {
	if maxBackups <= 0 {
		return nil
	}

	files, err := filepath.Glob(filepath.Join(backupDir, l.Prefix+"_*.log*"))
	if err != nil {
		return err
	}

	filtered := make([]string, 0, len(files))
	for _, file := range files {
		if file == excludeFile {
			continue
		}
		filtered = append(filtered, file)
	}

	if len(filtered) <= maxBackups {
		return nil
	}

	type backupFile struct {
		path    string
		modTime time.Time
	}

	backups := make([]backupFile, 0, len(filtered))
	for _, file := range filtered {
		info, statErr := os.Stat(file)
		if statErr != nil {
			continue
		}
		backups = append(backups, backupFile{path: file, modTime: info.ModTime()})
	}

	if len(backups) <= maxBackups {
		return nil
	}

	sort.Slice(backups, func(i, j int) bool {
		if backups[i].modTime.Equal(backups[j].modTime) {
			return backups[i].path < backups[j].path
		}
		return backups[i].modTime.Before(backups[j].modTime)
	})

	for _, old := range backups[:len(backups)-maxBackups] {
		if err := os.Remove(old.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	return nil
}

// compressFile compresses the given file using gzip format and creates a .gz file.
func compressFile(filename string) error {
	gzipFilename := filename + ".gz"
	outFile, err := os.Create(gzipFilename)
	if err != nil {
		return err
	}
	defer outFile.Close()

	gzipWriter := gzip.NewWriter(outFile)
	defer gzipWriter.Close()

	fileToCompress, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer fileToCompress.Close()

	_, err = io.Copy(gzipWriter, fileToCompress)
	return err
}

// uniqueBackupPath names a backup <base>.<timestamp>.log, adding -2, -3 ...
// when that name (or its .gz) exists, so two rotations within one second
// do not overwrite each other (custom fork).
func uniqueBackupPath(dir, base, timestamp string) string {
	for i := 1; ; i++ {
		name := fmt.Sprintf("%s.%s.log", base, timestamp)
		if i > 1 {
			name = fmt.Sprintf("%s.%s-%d.log", base, timestamp, i)
		}
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if _, err := os.Stat(p + ".gz"); os.IsNotExist(err) {
				return p
			}
		}
	}
}
