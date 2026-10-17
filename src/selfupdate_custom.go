package main

/*
	selfupdate_custom.go (custom fork)

	Checks GitHub for a newer Waymark release and installs it from the web
	interface.

	- Waymark asks GitHub for the latest release once a day (and when you
	  click Check now). Nothing is downloaded until you click Install.
	- Install downloads the binary for this platform and its .sha256 from the
	  release (only from this project's release downloads on github.com,
	  over HTTPS), checks the checksum, runs the new binary with -version to
	  make sure it starts and is the expected version, keeps the running
	  binary as waymark.prev, swaps the new one in and restarts Waymark in
	  place (same process, so systemd keeps tracking it).
	- If the new version does not stay up (it is started three times without
	  running for two minutes, e.g. systemd restarting a crashing binary),
	  the next start puts waymark.prev back.
	- Roll back swaps waymark.prev and the running binary, with the same
	  protection.
	- In a cluster each node is updated on its own (HA page: per node
	  buttons); a node asks another one to update itself over the sync
	  connection (action "update"). Nodes on different versions keep running
	  but pause configuration sync until they match.

	No root is involved: Waymark replaces the file in the folder it runs
	from, so that folder must be writable by the waymark user (it is with the
	install steps in the README, where /opt/waymark belongs to waymark).
	Older versions can only be installed with Roll back (to waymark.prev) or
	by hand.
*/

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"imuslab.com/zoraxy/mod/hacluster"
	"imuslab.com/zoraxy/mod/utils"
)

const (
	updateCheckInterval = 24 * time.Hour
	updateMaxBinarySize = 256 << 20
	updateStateFile     = ".update-state.json" // next to the binary
	updatePrevSuffix    = ".prev"
	updateHealthyAfter  = 2 * time.Minute // running this long counts as a good start
	updateMaxStarts     = 3               // starts without reaching updateHealthyAfter before rolling back
)

// updateTransport and updateRestartFn are replaced in tests.
var (
	updateTransport http.RoundTripper // nil: http.DefaultTransport
	updateRestartFn = updateRestart
)

var updateVersionPattern = regexp.MustCompile(`^v?(\d{1,4})\.(\d{1,4})\.(\d{1,6})$`)

// updateRelease is the latest release as GitHub reported it.
type updateRelease struct {
	Version   string `json:"version"` // 2.4.0
	Tag       string `json:"tag"`     // v2.4.0
	URL       string `json:"url"`     // release page
	Notes     string `json:"notes"`   // release description (Markdown, shown as text)
	Published string `json:"published"`
	AssetURL  string `json:"-"`
	SumURL    string `json:"-"`
	Size      int64  `json:"size"`
}

// updateState is kept next to the binary across restarts: the update in
// progress (Phase "installed") or the outcome of the last one.
type updateState struct {
	Kind     string `json:"kind"` // update or rollback
	From     string `json:"from"`
	To       string `json:"to"`
	Time     int64  `json:"time"`
	Starts   int    `json:"starts"`
	Phase    string `json:"phase"` // installed, done, rolledback
	Message  string `json:"message"`
	Finished int64  `json:"finished"`
}

var upd struct {
	mu         sync.Mutex
	latest     *updateRelease
	checked    int64
	checkErr   string
	lastManual time.Time
	busy       bool   // downloading or restarting
	phase      string // "", downloading, verifying, restarting, error
	message    string
}

/* Versions */

func parseVersion(v string) ([3]int, bool) {
	m := updateVersionPattern.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := 0; i < 3; i++ {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out, true
}

// versionNewer reports whether a is newer than b.
func versionNewer(a, b string) bool {
	va, ok1 := parseVersion(a)
	vb, ok2 := parseVersion(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	return false
}

func versionMajor(v string) int {
	p, _ := parseVersion(v)
	return p[0]
}

/* GitHub */

// updateRepoPath is "owner/repo" from PRODUCT_REPO.
func updateRepoPath() string {
	return strings.Trim(strings.TrimPrefix(PRODUCT_REPO, "https://github.com/"), "/")
}

func updateAssetName() string {
	return "waymark_" + runtime.GOOS + "_" + runtime.GOARCH
}

// updateHTTPClient only follows redirects to GitHub's own hosts over HTTPS
// (release downloads redirect to githubusercontent.com).
func updateHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: updateTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !updateAllowedHost(req.URL) {
				return fmt.Errorf("refusing to follow a redirect to %s", req.URL.Host)
			}
			return nil
		},
	}
}

func updateAllowedHost(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "github.com" || h == "api.github.com" || strings.HasSuffix(h, ".githubusercontent.com")
}

func updateGet(ctx context.Context, client *http.Client, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", PRODUCT_SHORT_NAME+"/"+DISPLAY_VERSION)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s answered %s", req.URL.Host, resp.Status)
	}
	return resp, nil
}

// updateFetchLatest asks GitHub for the latest release.
func updateFetchLatest() (*updateRelease, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+updateRepoPath()+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", PRODUCT_SHORT_NAME+"/"+DISPLAY_VERSION)
	resp, err := updateHTTPClient(20 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach GitHub: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("no release has been published yet")
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, errors.New("GitHub's rate limit for this address was reached; try again in an hour")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var gh struct {
		TagName     string `json:"tag_name"`
		HTMLURL     string `json:"html_url"`
		Body        string `json:"body"`
		PublishedAt string `json:"published_at"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&gh); err != nil {
		return nil, fmt.Errorf("unexpected answer from GitHub: %v", err)
	}
	if gh.Draft || gh.Prerelease {
		return nil, errors.New("the latest release is marked as a draft or pre-release")
	}
	v, ok := parseVersion(gh.TagName)
	if !ok {
		return nil, fmt.Errorf("the latest release's tag %q is not a version number", gh.TagName)
	}
	rel := &updateRelease{
		Version:   fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]),
		Tag:       gh.TagName,
		URL:       PRODUCT_REPO + "/releases/tag/" + url.PathEscape(gh.TagName),
		Notes:     gh.Body,
		Published: gh.PublishedAt,
	}
	if len(rel.Notes) > 20000 {
		rel.Notes = rel.Notes[:20000] + "\n..."
	}
	// Only accept downloads from this project's release on github.com
	prefix := PRODUCT_REPO + "/releases/download/" + gh.TagName + "/"
	for _, a := range gh.Assets {
		switch a.Name {
		case updateAssetName():
			if a.URL == prefix+a.Name {
				rel.AssetURL, rel.Size = a.URL, a.Size
			}
		case updateAssetName() + ".sha256":
			if a.URL == prefix+a.Name {
				rel.SumURL = a.URL
			}
		}
	}
	return rel, nil
}

// updateCheck refreshes the latest release. manual checks are limited to
// one a minute (GitHub allows 60 unauthenticated requests an hour).
func updateCheck(manual bool) error {
	upd.mu.Lock()
	if manual {
		if time.Since(upd.lastManual) < time.Minute {
			upd.mu.Unlock()
			return nil
		}
		upd.lastManual = time.Now()
	}
	upd.mu.Unlock()
	rel, err := updateFetchLatest()
	upd.mu.Lock()
	defer upd.mu.Unlock()
	upd.checked = time.Now().Unix()
	if err != nil {
		upd.checkErr = err.Error()
		return err
	}
	upd.latest, upd.checkErr = rel, ""
	if versionNewer(rel.Version, DISPLAY_VERSION) {
		SystemWideLogger.PrintAndLog("update", "Waymark "+rel.Version+" is available (running "+DISPLAY_VERSION+")", nil)
	}
	return nil
}

func updateCheckLoop() {
	time.Sleep(15 * time.Second) // let startup finish first
	for {
		updateCheck(false)
		time.Sleep(updateCheckInterval)
	}
}

/* Files next to the binary */

func updateExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func updateStatePath(exe string) string { return filepath.Join(filepath.Dir(exe), updateStateFile) }

func readUpdateState(exe string) (updateState, bool) {
	var st updateState
	b, err := os.ReadFile(updateStatePath(exe))
	if err != nil || json.Unmarshal(b, &st) != nil {
		return st, false
	}
	return st, true
}

func writeUpdateState(exe string, st updateState) error {
	b, _ := json.MarshalIndent(st, "", "  ")
	return writeFileAtomic(updateStatePath(exe), b, 0644)
}

// writeFileAtomic writes data to a temporary file in the same folder and
// renames it into place.
func writeFileAtomic(dst string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// copyFileAtomic copies src to dst (through a temporary file).
func copyFileAtomic(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// updateWritable reports why the binary cannot be replaced, or "" if it can.
func updateWritable(exe string) string {
	dir := filepath.Dir(exe)
	f, err := os.CreateTemp(dir, ".waymark-write-test-*")
	if err != nil {
		switch {
		case errors.Is(err, syscall.EROFS):
			return "Waymark's systemd service cannot write to " + dir + " (read-only file system): add ReadWritePaths=" + dir + " to the service"
		case errors.Is(err, os.ErrPermission):
			return "the " + haOwner() + " user cannot write to " + dir + ", the folder holding the Waymark binary"
		}
		return "cannot write to " + dir + ": " + err.Error()
	}
	f.Close()
	os.Remove(f.Name())
	return ""
}

// binaryVersion runs bin -version and returns the version it prints.
func binaryVersion(bin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-version")
	cmd.Dir = filepath.Dir(bin)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("it did not run: %v", err)
	}
	line := strings.TrimSpace(string(out))
	if !strings.HasPrefix(line, PRODUCT_NAME+" ") {
		return "", fmt.Errorf("it is not a Waymark binary (it printed %q)", truncate(line, 80))
	}
	f := strings.Fields(strings.TrimPrefix(line, PRODUCT_NAME+" "))
	if len(f) == 0 {
		return "", errors.New("it printed no version")
	}
	if _, ok := parseVersion(f[0]); !ok {
		return "", fmt.Errorf("it printed an unexpected version %q", f[0])
	}
	return f[0], nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

/* Install */

func updateSetPhase(phase, message string) {
	upd.mu.Lock()
	upd.phase, upd.message = phase, message
	upd.busy = phase == "downloading" || phase == "verifying" || phase == "restarting"
	upd.mu.Unlock()
	if phase == "error" {
		SystemWideLogger.PrintAndLog("update", message, nil)
	}
}

// updateStart begins installing version (which must be the latest release
// and newer than this one) in the background.
func updateStart(version string) (string, error) {
	upd.mu.Lock()
	if upd.busy {
		upd.mu.Unlock()
		return "", errors.New("an update is already in progress")
	}
	rel := upd.latest
	upd.mu.Unlock()
	if rel == nil {
		return "", errors.New("check for updates first")
	}
	if version != "" && version != rel.Version {
		return "", fmt.Errorf("%s is not the latest release (%s is)", version, rel.Version)
	}
	if !versionNewer(rel.Version, DISPLAY_VERSION) {
		return "", fmt.Errorf("this node already runs %s; the latest release is %s", DISPLAY_VERSION, rel.Version)
	}
	if rel.AssetURL == "" || rel.SumURL == "" {
		return "", fmt.Errorf("release %s has no %s binary with a .sha256 checksum", rel.Version, updateAssetName())
	}
	exe, err := updateExecutable()
	if err != nil {
		return "", fmt.Errorf("cannot find the running binary: %v", err)
	}
	if why := updateWritable(exe); why != "" {
		return "", errors.New("cannot update here: " + why)
	}
	updateSetPhase("downloading", "Downloading Waymark "+rel.Version)
	go func() {
		if err := updateInstall(exe, rel); err != nil {
			updateSetPhase("error", "Update to "+rel.Version+" failed: "+err.Error())
		}
	}()
	return "Downloading Waymark " + rel.Version + "; this node restarts when it is installed", nil
}

func updateInstall(exe string, rel *updateRelease) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := updateHTTPClient(10 * time.Minute)

	// Checksum file: "<64 hex>  waymark_linux_amd64"
	resp, err := updateGet(ctx, client, rel.SumURL)
	if err != nil {
		return fmt.Errorf("downloading the checksum: %v", err)
	}
	sumLine, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("downloading the checksum: %v", err)
	}
	fields := strings.Fields(string(sumLine))
	if len(fields) == 0 || !regexp.MustCompile(`^[0-9a-fA-F]{64}$`).MatchString(fields[0]) {
		return errors.New("the release's checksum file is not in sha256sum format")
	}
	wantSum := strings.ToLower(fields[0])

	// Binary, written next to the running one so it can be renamed into place
	resp, err = updateGet(ctx, client, rel.AssetURL)
	if err != nil {
		return fmt.Errorf("downloading the binary: %v", err)
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".waymark-update-*")
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
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, updateMaxBinarySize+1))
	if err == nil && n > updateMaxBinarySize {
		err = errors.New("the binary is unexpectedly large")
	}
	if err == nil {
		err = tmp.Chmod(0755)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("downloading the binary: %v", err)
	}

	updateSetPhase("verifying", "Checking Waymark "+rel.Version)
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSum {
		return errors.New("the downloaded binary does not match the release's SHA-256 checksum")
	}
	got, err := binaryVersion(tmpName)
	if err != nil {
		return fmt.Errorf("the downloaded binary failed its check: %v", err)
	}
	if got != rel.Version {
		return fmt.Errorf("the downloaded binary reports version %s, expected %s", got, rel.Version)
	}

	// Keep the running binary for Roll back, then swap the new one in
	if err := copyFileAtomic(exe, exe+updatePrevSuffix, 0755); err != nil {
		return fmt.Errorf("could not keep a copy of the running binary: %v", err)
	}
	st := updateState{Kind: "update", From: DISPLAY_VERSION, To: rel.Version, Time: time.Now().Unix(), Phase: "installed"}
	if err := writeUpdateState(exe, st); err != nil {
		return fmt.Errorf("could not record the update: %v", err)
	}
	if err := os.Rename(tmpName, exe); err != nil {
		return fmt.Errorf("could not replace the binary: %v", err)
	}
	keep = true
	SystemWideLogger.PrintAndLog("update", "Installed Waymark "+rel.Version+" (was "+DISPLAY_VERSION+"); restarting", nil)
	updateRestartFn(exe, "Installed Waymark "+rel.Version+"; restarting")
	return nil
}

// updateRestart restarts Waymark in place with the binary now at exe.
func updateRestart(exe, message string) {
	updateSetPhase("restarting", message)
	go func() {
		time.Sleep(1500 * time.Millisecond) // let the status reach the browser
		mdnsTickerStop = nil
		ShutdownSeq()
		err := syscall.Exec(exe, os.Args, os.Environ())
		// Only reached if exec failed; exit so systemd starts Waymark again
		fmt.Println("Update: restart failed: " + err.Error())
		os.Exit(1)
	}()
}

// updateRollback swaps waymark.prev and the running binary.
func updateRollback() (string, error) {
	upd.mu.Lock()
	if upd.busy {
		upd.mu.Unlock()
		return "", errors.New("an update is in progress")
	}
	upd.mu.Unlock()
	exe, err := updateExecutable()
	if err != nil {
		return "", err
	}
	prev := exe + updatePrevSuffix
	if _, err := os.Stat(prev); err != nil {
		return "", errors.New("there is no previous version to go back to")
	}
	if why := updateWritable(exe); why != "" {
		return "", errors.New("cannot roll back here: " + why)
	}
	prevVersion, err := binaryVersion(prev)
	if err != nil {
		return "", fmt.Errorf("the previous binary failed its check: %v", err)
	}
	// Copy prev aside, save the running binary as prev, move the copy in
	staged := filepath.Join(filepath.Dir(exe), ".waymark-rollback")
	if err := copyFileAtomic(prev, staged, 0755); err != nil {
		return "", err
	}
	if err := copyFileAtomic(exe, prev, 0755); err != nil {
		os.Remove(staged)
		return "", err
	}
	st := updateState{Kind: "rollback", From: DISPLAY_VERSION, To: prevVersion, Time: time.Now().Unix(), Phase: "installed"}
	if err := writeUpdateState(exe, st); err != nil {
		os.Remove(staged)
		return "", err
	}
	if err := os.Rename(staged, exe); err != nil {
		os.Remove(staged)
		return "", err
	}
	msg := "Rolled back to Waymark " + prevVersion + "; restarting"
	SystemWideLogger.PrintAndLog("update", msg+" (was "+DISPLAY_VERSION+")", nil)
	updateRestartFn(exe, msg)
	return msg, nil
}

/* Startup */

// updateStartupCheck runs early at every start. After an update or roll
// back it counts starts of the new version; when the new version has been
// started updateMaxStarts times without running updateHealthyAfter, the
// previous binary is put back. Once it has run that long, the update is
// recorded as done.
func updateStartupCheck() {
	exe, err := updateExecutable()
	if err != nil {
		return
	}
	st, ok := readUpdateState(exe)
	if !ok || st.Phase != "installed" {
		return
	}
	if DISPLAY_VERSION != st.To {
		// Someone put another binary in place by hand
		st.Phase, st.Message, st.Finished = "done", "Waymark "+DISPLAY_VERSION+" is running instead of "+st.To, time.Now().Unix()
		writeUpdateState(exe, st)
		return
	}
	st.Starts++
	if st.Starts > updateMaxStarts {
		prev := exe + updatePrevSuffix
		if v, err := binaryVersion(prev); err == nil && v == st.From {
			if copyFileAtomic(prev, exe, 0755) == nil {
				st.Phase, st.Finished = "rolledback", time.Now().Unix()
				st.Message = fmt.Sprintf("Waymark %s did not stay up (started %d times), so %s was put back", st.To, st.Starts-1, st.From)
				writeUpdateState(exe, st)
				fmt.Println("Update: " + st.Message)
				if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
					os.Exit(1)
				}
			}
		}
		// Nothing to go back to: keep running this version
		st.Phase, st.Message, st.Finished = "done", "Waymark "+st.To+" restarted several times and the previous version could not be restored", time.Now().Unix()
		writeUpdateState(exe, st)
		return
	}
	writeUpdateState(exe, st)
	go func() {
		time.Sleep(updateHealthyAfter)
		st.Phase, st.Finished = "done", time.Now().Unix()
		if st.Kind == "rollback" {
			st.Message = "Rolled back from " + st.From + " to " + st.To
		} else {
			st.Message = "Updated from " + st.From + " to " + st.To
		}
		writeUpdateState(exe, st)
		SystemWideLogger.PrintAndLog("update", st.Message, nil)
	}()
}

func initSelfUpdate() {
	go updateCheckLoop()
}

/* Cluster: ask another node to update itself */

// haUpdateAction is the "update" action: another node asks this one to
// install the latest release (it checks GitHub itself and verifies the
// download the same way as a local install).
func haUpdateAction(remote string, body []byte) (any, error) {
	var in struct {
		Version string `json:"version"`
	}
	json.Unmarshal(body, &in)
	upd.mu.Lock()
	stale := upd.latest == nil || time.Since(time.Unix(upd.checked, 0)) > 10*time.Minute
	upd.mu.Unlock()
	if stale {
		if err := updateCheck(false); err != nil {
			return nil, err
		}
	}
	msg, err := updateStart(in.Version)
	if err != nil {
		return nil, err
	}
	SystemWideLogger.PrintAndLog("update", "Update requested by cluster node "+remote, nil)
	return map[string]string{"message": msg}, nil
}

/* API */

func updateStatus() map[string]any {
	exe, _ := updateExecutable()
	last, hasLast := readUpdateState(exe)
	prevVersion := ""
	if exe != "" {
		if _, err := os.Stat(exe + updatePrevSuffix); err == nil {
			prevVersion = "unknown"
			if hasLast && last.From != "" {
				prevVersion = last.From
			}
			if hasLast && last.Phase == "rolledback" {
				prevVersion = "" // waymark.prev is the version now running
			}
		}
	}
	upd.mu.Lock()
	defer upd.mu.Unlock()
	resp := map[string]any{
		"current":    DISPLAY_VERSION,
		"checked":    upd.checked,
		"checkError": upd.checkErr,
		"phase":      upd.phase,
		"message":    upd.message,
		"busy":       upd.busy,
		"previous":   prevVersion,
		"asset":      updateAssetName(),
		"repo":       PRODUCT_REPO,
	}
	if hasLast {
		resp["last"] = last
	}
	if exe != "" {
		resp["executable"] = exe
		resp["writable"] = updateWritable(exe) == ""
		resp["writableReason"] = updateWritable(exe)
	}
	if upd.latest != nil {
		resp["latest"] = upd.latest
		resp["available"] = versionNewer(upd.latest.Version, DISPLAY_VERSION)
		resp["major"] = versionMajor(upd.latest.Version) > versionMajor(DISPLAY_VERSION)
		resp["installable"] = upd.latest.AssetURL != "" && upd.latest.SumURL != ""
	}
	return resp
}

// HandleUpdateStatus returns this node's version, the latest release and
// the state of an update in progress.
// GET /api/update/status
func HandleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	resp := updateStatus()
	cfg := haConfig()
	resp["haActive"] = cfg.Enabled && len(cfg.Nodes) > 1 && haEffectiveRole(haRuntime()).State == "MASTER"
	js, _ := json.Marshal(resp)
	utils.SendJSONResponse(w, string(js))
}

// HandleUpdateCheck asks GitHub for the latest release now.
// POST /api/update/check
func HandleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := updateCheck(true); err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	js, _ := json.Marshal(updateStatus())
	utils.SendJSONResponse(w, string(js))
}

// HandleUpdateInstall installs the latest release on this node.
// POST /api/update/install  version=<x.y.z>
func HandleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	version, _ := utils.PostPara(r, "version")
	msg, err := updateStart(version)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	js, _ := json.Marshal(map[string]string{"message": msg})
	utils.SendJSONResponse(w, string(js))
}

// HandleUpdateRollback goes back to the previous binary.
// POST /api/update/rollback
func HandleUpdateRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	msg, err := updateRollback()
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	js, _ := json.Marshal(map[string]string{"message": msg})
	utils.SendJSONResponse(w, string(js))
}

// HandleUpdatePeer asks another cluster node to install the latest release.
// POST /api/update/peer  node=<name>  version=<x.y.z>
func HandleUpdatePeer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name, _ := utils.PostPara(r, "node")
	version, _ := utils.PostPara(r, "version")
	cfg := haConfig()
	key, err := haSwitchKey(cfg)
	if err != nil {
		utils.SendErrorResponse(w, strings.Replace(err.Error(), "switching", "updating other nodes", 1))
		return
	}
	var node *hacluster.Node
	for i, n := range cfg.Nodes {
		if n.Name == name && i != haSelf(cfg) {
			node = &cfg.Nodes[i]
		}
	}
	if node == nil {
		utils.SendErrorResponse(w, "unknown node")
		return
	}
	var out map[string]string
	addr := net.JoinHostPort(node.Address, strconv.Itoa(cfg.SyncPort))
	if err := hacluster.SyncAction(addr, key, "update", map[string]string{"version": version}, &out, 40*time.Second); err != nil {
		utils.SendErrorResponse(w, "could not reach "+node.Name+": "+err.Error())
		return
	}
	if out["error"] != "" {
		utils.SendErrorResponse(w, node.Name+": "+out["error"])
		return
	}
	js, _ := json.Marshal(map[string]string{"message": node.Name + ": " + out["message"]})
	utils.SendJSONResponse(w, string(js))
}
