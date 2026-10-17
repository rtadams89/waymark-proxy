package hacluster

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Default locations, created by deploy/install.sh.
const (
	DefaultDir        = "/var/lib/waymark/ha" // owned by the waymark user
	DefaultStateDir   = "/var/lib/waymark-ha" // root-owned, readable by the waymark group
	DefaultScriptsDir = "/usr/local/lib/waymark-ha"
)

// Runtime reads and writes the files shared with keepalived and the helper.
type Runtime struct {
	Dir      string
	StateDir string
}

// Role is this node's keepalived state.
type Role struct {
	State string `json:"state"` // MASTER, BACKUP, FAULT, STOP, or "" if unknown
	Since int64  `json:"since"`
}

// ApplyResult is the helper's report on the last settings it processed.
type ApplyResult struct {
	ApplyID int64  `json:"applyId"`
	Status  string `json:"status"` // ok or error
	Time    int64  `json:"time"`
	Message string `json:"message"`
}

// Installed reports whether install.sh has run on this node: Waymark can
// write its folder and read the helper's result folder.
func (r *Runtime) Installed() (bool, string) {
	if st, err := os.Stat(r.Dir); err != nil || !st.IsDir() {
		return false, r.Dir + " does not exist"
	}
	probe := filepath.Join(r.Dir, ".probe")
	if err := os.WriteFile(probe, []byte("x"), 0600); err != nil {
		if errors.Is(err, syscall.EROFS) {
			// Permissions are fine but systemd's ProtectSystem=strict hides the folder
			return false, "Waymark's systemd service is sandboxed and cannot write to " + r.Dir +
				" (read-only file system). Rerun install.sh, or add ReadWritePaths=" + r.Dir + " to the service with systemctl edit and restart Waymark"
		}
		return false, "Waymark cannot write to " + r.Dir
	}
	os.Remove(probe)
	if _, err := os.ReadDir(r.StateDir); err != nil {
		return false, "Waymark cannot read " + r.StateDir
	}
	return true, ""
}

func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// WriteSettings saves cluster.env, which triggers the helper.
func (r *Runtime) WriteSettings(content string) error {
	return writeAtomic(filepath.Join(r.Dir, "cluster.env"), []byte(content), 0640)
}

// WriteHeartbeat records whether this node can serve traffic; the
// keepalived check script requires a fresh "ok".
func (r *Runtime) WriteHeartbeat(ok bool, reason string, now time.Time) error {
	line := "ok " + strconv.FormatInt(now.Unix(), 10) + "\n"
	if !ok {
		line = "fail " + strconv.FormatInt(now.Unix(), 10) + " " + strings.ReplaceAll(reason, "\n", " ") + "\n"
	}
	return writeAtomic(filepath.Join(r.Dir, "heartbeat"), []byte(line), 0644)
}

// ReadRole returns the role keepalived last reported.
func (r *Runtime) ReadRole() Role {
	b, err := os.ReadFile(filepath.Join(r.Dir, "role"))
	if err != nil {
		return Role{}
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return Role{}
	}
	switch f[0] {
	case "MASTER", "BACKUP", "FAULT", "STOP":
	default:
		return Role{}
	}
	since, _ := strconv.ParseInt(f[1], 10, 64)
	return Role{State: f[0], Since: since}
}

// ReadResult returns the helper's last report.
func (r *Runtime) ReadResult() (ApplyResult, error) {
	f, err := os.Open(filepath.Join(r.StateDir, "result"))
	if err != nil {
		return ApplyResult{}, err
	}
	defer f.Close()
	res := ApplyResult{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "APPLY_ID":
			res.ApplyID, _ = strconv.ParseInt(v, 10, 64)
		case "STATUS":
			res.Status = v
		case "TIME":
			res.Time, _ = strconv.ParseInt(v, 10, 64)
		case "MESSAGE":
			res.Message = v
		}
	}
	if res.Status == "" {
		return res, errors.New("result file is incomplete")
	}
	return res, nil
}

// WaitForResult waits for the helper's report on applyID.
func (r *Runtime) WaitForResult(applyID int64, timeout time.Duration) (ApplyResult, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if res, err := r.ReadResult(); err == nil && res.ApplyID == applyID {
			return res, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return ApplyResult{}, fmt.Errorf("no response from waymark-ha-apply within %s (is waymark-ha-apply.path enabled? check: journalctl -u waymark-ha-apply)", timeout)
}
