package main

/*
	rollingupdate_custom.go (custom fork)

	Rolling update: install the latest release on every node of an HA
	cluster, one node at a time, from one button.

	The active node runs it (a request made on a standby is passed to the
	active node over the sync connection, action "rollingupdate"):

	1. Checks the cluster first: every node answers, this node is the only
	   active one, every other node is a healthy standby with keepalived
	   running, and nothing else (an update, a switch-over) is going on.
	2. Updates the standbys one at a time, lowest priority first. Each one
	   installs the release itself (as the per-node Update button does) and
	   must come back on the new version, healthy and as a standby, and stay
	   that way for rollStableFor, before the next one starts.
	3. Makes the updated standby with the highest priority active, the same
	   way as Make active, and waits until this node has given up the
	   floating IPs.
	4. Updates this node, which restarts as a standby. With preempt on and
	   this node's priority higher, it keeps reporting itself unhealthy until
	   it restarts, so keepalived does not move the floating IPs back to it
	   while it still runs the old version.

	It stops at the first problem. This node is only updated once every
	other node runs the new version and another node is active. Nodes that
	already run the new version are skipped, so starting it again after
	fixing a problem carries on where it stopped.

	Progress is saved in .rolling-update.json next to the binary, so this
	node can confirm the last step after it restarts. Every node reports
	the progress it holds with its state over the sync connection, so the
	HA page shows it on whichever node it is opened, including while this
	node restarts.
*/

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"imuslab.com/zoraxy/mod/hacluster"
	"imuslab.com/zoraxy/mod/utils"
)

const (
	rollStateFile = ".rolling-update.json" // next to the binary
	rollShownFor  = time.Hour              // a finished rolling update stays on the HA page this long
	rollMaxHold   = 15 * time.Minute       // longest this node keeps reporting unhealthy before its own update
)

// Timings and cluster access, replaced in tests.
var (
	rollPoll        = 2 * time.Second
	rollAskRetryFor = 60 * time.Second // keep asking a node that is restarting
	rollStableFor   = 20 * time.Second // a node must stay healthy on the new version this long
	rollNodeTimeout = 12 * time.Minute // download (up to 10 minutes) and restart of one node
	rollSelfTimeout = 3 * time.Minute  // this node becoming healthy after its own restart

	rollFetch       func(cfg *hacluster.Config, key []byte) []haNodeState
	rollFetchOne    func(cfg *hacluster.Config, key []byte, n hacluster.Node) (haSyncPeerInfo, error)
	rollAskUpdate   func(cfg *hacluster.Config, key []byte, n hacluster.Node, version string) error
	rollHandOver    func(cfg *hacluster.Config, key []byte, target string) error
	rollSelfUpdate  func(version string) (string, error)
	rollSelfPhase   func() (string, string)
	rollSelfHealthy func() (bool, string)
	rollLocalHash   func() string // hash of this node's synced configuration
	rollStatePath   = rollStatePathDefault
)

// Set here rather than above: these refer back to code that starts a
// rolling update, which Go does not allow in a variable's initial value.
func init() {
	rollFetch = haFetchNodeStates
	rollFetchOne = rollFetchOneDefault
	rollAskUpdate = rollAskUpdateDefault
	rollHandOver = rollHandOverDefault
	rollSelfUpdate = updateStart
	rollSelfPhase = func() (string, string) {
		upd.mu.Lock()
		defer upd.mu.Unlock()
		return upd.phase, upd.message
	}
	rollSelfHealthy = rollSelfHealthyDefault
	rollLocalHash = func() string {
		if c, err := hacluster.BuildContent(CONF_FOLDER, sysdb); err == nil {
			return c.Hash()
		}
		return ""
	}
}

// rollStep is one node's part of a rolling update.
type rollStep struct {
	Node    string `json:"node"`
	Action  string `json:"action"` // update (a standby), switchover (make Node active), updateself (the active node)
	State   string `json:"state"`  // pending, running, done, skipped, failed
	Message string `json:"message"`
	Time    int64  `json:"time"` // when State last changed
}

// rollState is a rolling update in progress or the last one.
type rollState struct {
	Coordinator string     `json:"coordinator"` // node running it (active when it started)
	From        string     `json:"from"`        // version the coordinator ran
	To          string     `json:"to"`
	Phase       string     `json:"phase"` // running, done, failed
	Message     string     `json:"message"`
	Started     int64      `json:"started"`
	Finished    int64      `json:"finished"`
	Steps       []rollStep `json:"steps"`
}

func (s *rollState) copy() *rollState {
	if s == nil {
		return nil
	}
	c := *s
	c.Steps = append([]rollStep(nil), s.Steps...)
	return &c
}

var roll struct {
	mu        sync.Mutex
	st        *rollState
	holdUntil time.Time // report unhealthy until then (preempt, see the top)
	holdWhy   string
}

/* State */

func rollStatePathDefault() string {
	exe, err := updateExecutable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), rollStateFile)
}

// rollSave writes the state to disk; call with roll.mu held.
func rollSave() {
	p := rollStatePath()
	if p == "" || roll.st == nil {
		return
	}
	js, _ := json.MarshalIndent(roll.st, "", "  ")
	writeFileAtomic(p, js, 0600)
}

func rollLoad() *rollState {
	p := rollStatePath()
	if p == "" {
		return nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var st rollState
	if json.Unmarshal(raw, &st) != nil || st.To == "" {
		return nil
	}
	return &st
}

// rollSnapshot returns a copy of this node's rolling update state.
func rollSnapshot() *rollState {
	roll.mu.Lock()
	defer roll.mu.Unlock()
	return roll.st.copy()
}

// rollRunning reports whether this node is running a rolling update.
func rollRunning() bool {
	roll.mu.Lock()
	defer roll.mu.Unlock()
	return roll.st != nil && roll.st.Phase == "running"
}

// rollVisible is the state to show: running, or finished within rollShownFor.
func rollVisible(st *rollState) bool {
	if st == nil {
		return false
	}
	return st.Phase == "running" || time.Since(time.Unix(st.Finished, 0)) < rollShownFor
}

// rollPeerState is what this node reports to the others (haLocalInfo).
func rollPeerState() *rollState {
	st := rollSnapshot()
	if !rollVisible(st) {
		return nil
	}
	return st
}

// rollSetStep updates step i and saves.
func rollSetStep(i int, state, message string) {
	roll.mu.Lock()
	defer roll.mu.Unlock()
	if roll.st == nil || i >= len(roll.st.Steps) {
		return
	}
	s := &roll.st.Steps[i]
	if s.State != state {
		s.Time = time.Now().Unix()
	}
	s.State, s.Message = state, message
	if state == "running" {
		roll.st.Message = message
	}
	rollSave()
}

// rollFinish ends the rolling update.
func rollFinish(phase, message string) {
	roll.mu.Lock()
	if roll.st != nil {
		roll.st.Phase, roll.st.Message, roll.st.Finished = phase, message, time.Now().Unix()
		rollSave()
	}
	roll.holdUntil = time.Time{}
	roll.mu.Unlock()
	if phase == "failed" {
		SystemWideLogger.PrintAndLog("update", "Rolling update stopped: "+message, nil)
	} else {
		SystemWideLogger.PrintAndLog("update", "Rolling update: "+message, nil)
	}
}

// rollHolding reports whether this node keeps reporting itself unhealthy
// until its own update (haSwitchHolding).
func rollHolding() (bool, string) {
	roll.mu.Lock()
	defer roll.mu.Unlock()
	if time.Now().Before(roll.holdUntil) {
		return true, roll.holdWhy
	}
	return false, ""
}

func rollSetHold(d time.Duration, why string) {
	roll.mu.Lock()
	roll.holdUntil, roll.holdWhy = time.Now().Add(d), why
	roll.mu.Unlock()
}

/* Plan */

// rollPlan checks that the cluster can be updated to target and returns
// its steps. states must hold every node (haFetchNodeStates).
func rollPlan(states []haNodeState, target string) ([]rollStep, error) {
	if len(states) < 2 {
		return nil, errors.New("a rolling update needs at least two nodes; use Install on System > About instead")
	}
	for _, s := range states {
		if s.Err != nil {
			if errors.Is(s.Err, hacluster.ErrSyncAuth) {
				return nil, fmt.Errorf("%s's cluster key does not match", s.Node.Name)
			}
			return nil, fmt.Errorf("%s is not reachable: %v", s.Node.Name, s.Err)
		}
	}
	var self *haNodeState
	for i := range states {
		if states[i].Self && states[i].Err == nil && states[i].Info.Role != "MASTER" {
			return nil, errors.New("only the active node can run a rolling update")
		}
	}
	var standbys []haNodeState
	for i := range states {
		s := &states[i]
		name := s.Node.Name
		if p := s.Info.UpdatePhase; p == "downloading" || p == "verifying" || p == "restarting" {
			return nil, fmt.Errorf("%s is already installing an update", name)
		}
		if s.Info.Version != "" && versionNewer(s.Info.Version, target) {
			return nil, fmt.Errorf("%s runs %s, which is newer than %s", name, s.Info.Version, target)
		}
		if s.Self {
			self = s
			continue
		}
		switch {
		case s.Info.Role == "MASTER":
			return nil, fmt.Errorf("%s also reports itself active; fix that first", name)
		case s.Info.Role != "BACKUP":
			return nil, fmt.Errorf("%s is not a standby (its state is %s)", name, roleOrUnknown(s.Info.Role))
		case !s.Info.HeartbeatOK:
			return nil, fmt.Errorf("%s is not healthy: %s", name, s.Info.HeartbeatReason)
		}
		if k := s.Info.Keepalived; k != "active" && k != "unknown" && k != "" {
			return nil, fmt.Errorf("keepalived is not running on %s (%s)", name, k)
		}
		// A standby restarts to load the configuration it copies, which would
		// cut short its update; same-version standbys must be in sync first
		if s.Info.Version == self0(states).Info.Version && s.Info.SyncStatus != "in-sync" {
			detail := s.Info.SyncDetail
			if detail == "" {
				detail = roleOrUnknown(s.Info.SyncStatus)
			}
			return nil, fmt.Errorf("%s has not finished copying the active node's configuration (%s); try again in a minute", name, detail)
		}
		// ... and have the latest configuration: a standby made active during
		// the update would otherwise hand an older one back to this node
		if s.Info.Version == self0(states).Info.Version && s.Info.Hash != "" {
			if h := rollLocalHash(); h != "" && h != s.Info.Hash {
				return nil, fmt.Errorf("%s has not copied the latest configuration changes yet; try again in a minute (or use Sync now)", name)
			}
		}
		standbys = append(standbys, *s)
	}
	if self == nil {
		return nil, errors.New("this machine is not in the cluster's node list")
	}
	if self.Info.Role != "MASTER" {
		return nil, errors.New("only the active node can run a rolling update")
	}
	if !self.Info.HeartbeatOK {
		return nil, fmt.Errorf("this node is not healthy: %s", self.Info.HeartbeatReason)
	}

	// Lowest priority first: the likeliest failover target stays untouched longest
	sort.SliceStable(standbys, func(i, j int) bool { return standbys[i].Node.Priority < standbys[j].Node.Priority })
	steps := []rollStep{}
	needed := 0
	for _, s := range standbys {
		st := rollStep{Node: s.Node.Name, Action: "update", State: "pending"}
		if s.Info.Version == target {
			st.State, st.Message = "skipped", "Already runs "+target
		} else {
			needed++
		}
		steps = append(steps, st)
	}
	if self.Info.Version != target {
		next := standbys[len(standbys)-1] // highest priority
		steps = append(steps,
			rollStep{Node: next.Node.Name, Action: "switchover", State: "pending"},
			rollStep{Node: self.Node.Name, Action: "updateself", State: "pending"})
		needed++
	}
	if needed == 0 {
		return nil, fmt.Errorf("every node already runs %s", target)
	}
	return steps, nil
}

// self0 returns this node's entry of states (an empty one if missing).
func self0(states []haNodeState) haNodeState {
	for _, s := range states {
		if s.Self {
			return s
		}
	}
	return haNodeState{}
}

/* Start */

// rollStart starts a rolling update to version (the latest release). On a
// standby the request goes to the active node.
func rollStart(version string) (string, error) {
	cfg := haConfig()
	key, err := haSwitchKey(cfg)
	if err != nil {
		return "", errors.New(strings.Replace(err.Error(), "switching", "a rolling update", 1))
	}
	if haEffectiveRole(haRuntime()).State == "MASTER" {
		return rollStartLocal(cfg, key, version)
	}
	var active *haNodeState
	for _, s := range rollFetch(cfg, key) {
		if !s.Self && s.Err == nil && s.Info.Role == "MASTER" {
			s := s
			active = &s
		}
	}
	if active == nil {
		return "", errors.New("no node is active right now; a rolling update starts from the active node")
	}
	var out map[string]string
	addr := net.JoinHostPort(active.Node.Address, strconv.Itoa(cfg.SyncPort))
	if err := hacluster.SyncAction(addr, key, "rollingupdate", map[string]string{"version": version}, &out, 60*time.Second); err != nil {
		return "", fmt.Errorf("could not ask %s (the active node) to start it: %v", active.Node.Name, err)
	}
	if out["error"] != "" {
		return "", fmt.Errorf("%s: %s", active.Node.Name, out["error"])
	}
	return out["message"], nil
}

// haRollAction is the "rollingupdate" action: a standby passes on a
// request made on it.
func haRollAction(remote string, body []byte) (any, error) {
	var in struct {
		Version string `json:"version"`
	}
	json.Unmarshal(body, &in)
	cfg := haConfig()
	key, err := haSwitchKey(cfg)
	if err != nil {
		return nil, err
	}
	SystemWideLogger.PrintAndLog("update", "Rolling update requested by cluster node "+remote, nil)
	msg, err := rollStartLocal(cfg, key, in.Version)
	if err != nil {
		return nil, err
	}
	return map[string]string{"message": msg}, nil
}

// rollRelease returns the latest release, checking GitHub when the last
// check is older than 10 minutes.
func rollRelease(version string) (*updateRelease, error) {
	upd.mu.Lock()
	stale := upd.latest == nil || time.Since(time.Unix(upd.checked, 0)) > 10*time.Minute
	upd.mu.Unlock()
	if stale {
		if err := updateCheck(false); err != nil {
			return nil, err
		}
	}
	upd.mu.Lock()
	rel := upd.latest
	upd.mu.Unlock()
	if rel == nil {
		return nil, errors.New("the latest release is not known; check for updates first")
	}
	if version != "" && version != rel.Version {
		return nil, fmt.Errorf("%s is not the latest release (%s is)", version, rel.Version)
	}
	if rel.AssetURL == "" || rel.SumURL == "" {
		return nil, fmt.Errorf("release %s has no %s binary with a .sha256 checksum", rel.Version, updateAssetName())
	}
	return rel, nil
}

func rollStartLocal(cfg *hacluster.Config, key []byte, version string) (string, error) {
	if haEffectiveRole(haRuntime()).State != "MASTER" {
		return "", errors.New("this node is not the active one")
	}
	if rollRunning() {
		return "", errors.New("a rolling update is already in progress")
	}
	if holding, _ := haSwitchHolding(); holding {
		return "", errors.New("a switch-over is in progress")
	}
	upd.mu.Lock()
	busy := upd.busy
	upd.mu.Unlock()
	if busy {
		return "", errors.New("this node is installing an update")
	}
	rel, err := rollRelease(version)
	if err != nil {
		return "", err
	}
	steps, err := rollPlan(rollFetch(cfg, key), rel.Version)
	if err != nil {
		return "", err
	}
	if steps[len(steps)-1].Action == "updateself" {
		exe, err := updateExecutable()
		if err != nil {
			return "", fmt.Errorf("cannot find the running binary: %v", err)
		}
		if why := updateWritable(exe); why != "" {
			return "", errors.New("this node cannot update itself: " + why)
		}
	}
	roll.mu.Lock()
	if roll.st != nil && roll.st.Phase == "running" {
		roll.mu.Unlock()
		return "", errors.New("a rolling update is already in progress")
	}
	roll.st = &rollState{Coordinator: haNodeName(cfg), From: DISPLAY_VERSION, To: rel.Version, Phase: "running",
		Message: "Starting", Started: time.Now().Unix(), Steps: steps}
	rollSave()
	roll.mu.Unlock()
	SystemWideLogger.PrintAndLog("update", "Rolling update to "+rel.Version+" started", nil)
	go rollRun(cfg, key, rel.Version, steps)
	return "Rolling update to " + rel.Version + " started", nil
}

/* Run */

func rollRun(cfg *hacluster.Config, key []byte, to string, steps []rollStep) {
	nodeByName := map[string]hacluster.Node{}
	for _, n := range cfg.Nodes {
		nodeByName[n.Name] = n
	}
	for i, s := range steps {
		if s.State == "skipped" {
			continue
		}
		var err error
		switch s.Action {
		case "update":
			err = rollUpdateStandby(i, cfg, key, nodeByName[s.Node], to)
		case "switchover":
			err = rollSwitch(i, cfg, key, nodeByName[s.Node], nodeByName[steps[len(steps)-1].Node])
		case "updateself":
			err = rollUpdateSelf(i, to)
			if err == nil {
				return // this node restarts; initRollingUpdate finishes
			}
		}
		if err != nil {
			rollSetStep(i, "failed", err.Error())
			rollFinish("failed", err.Error())
			return
		}
	}
	rollFinish("done", rollDoneMessage(cfg, to, "", ""))
}

// rollDoneMessage: active is the node made active, self the node updated
// last (both empty when this node was not updated).
func rollDoneMessage(cfg *hacluster.Config, to, active, self string) string {
	msg := "Every node runs " + to
	if active == "" {
		return msg
	}
	prio := map[string]int{}
	for _, n := range cfg.Nodes {
		prio[n.Name] = n.Priority
	}
	if cfg.Preempt && prio[self] > prio[active] {
		return msg + ". Preempt is on, so " + self + " takes the floating IPs back from " + active
	}
	return msg + "; " + active + " is the active node"
}

func rollFetchOneDefault(cfg *hacluster.Config, key []byte, n hacluster.Node) (haSyncPeerInfo, error) {
	var info haSyncPeerInfo
	err := hacluster.SyncFetch(net.JoinHostPort(n.Address, strconv.Itoa(cfg.SyncPort)), key, "/waymark-ha/v1/state", &info, 3*time.Second)
	return info, err
}

func rollAskUpdateDefault(cfg *hacluster.Config, key []byte, n hacluster.Node, version string) error {
	var out map[string]string
	addr := net.JoinHostPort(n.Address, strconv.Itoa(cfg.SyncPort))
	if err := hacluster.SyncAction(addr, key, "update", map[string]string{"version": version}, &out, 60*time.Second); err != nil {
		return fmt.Errorf("could not ask %s to update: %v", n.Name, err)
	}
	if out["error"] != "" {
		return fmt.Errorf("%s did not start the update: %s", n.Name, out["error"])
	}
	return nil
}

// rollUpdateStandby asks n to install to and waits until it is back on to,
// healthy and a standby, for rollStableFor.
func rollUpdateStandby(i int, cfg *hacluster.Config, key []byte, n hacluster.Node, to string) error {
	rollSetStep(i, "running", "Asking "+n.Name+" to install "+to)
	asked := time.Now()
	for {
		err := rollAskUpdate(cfg, key, n, to)
		if err == nil {
			break
		}
		// The node may be restarting to load the active node's configuration
		// (it refuses, or does not answer, for a few seconds): ask again
		retry := strings.Contains(err.Error(), "restarting to load the configuration") || strings.Contains(err.Error(), "could not ask")
		if !retry || time.Since(asked) > rollAskRetryFor {
			return err
		}
		rollSetStep(i, "running", n.Name+" is restarting; asking it again to install "+to)
		time.Sleep(rollPoll)
	}
	asked = time.Now()
	rollSetStep(i, "running", n.Name+" is installing "+to)
	deadline := asked.Add(rollNodeTimeout)
	var stableSince time.Time
	for time.Now().Before(deadline) {
		time.Sleep(rollPoll)
		info, err := rollFetchOne(cfg, key, n)
		if err != nil {
			stableSince = time.Time{}
			if errors.Is(err, hacluster.ErrSyncAuth) {
				return fmt.Errorf("%s's cluster key no longer matches", n.Name)
			}
			rollSetStep(i, "running", "Waiting for "+n.Name+" to restart on "+to)
			continue
		}
		if info.UpdatePhase == "error" {
			return fmt.Errorf("%s: %s", n.Name, info.UpdateMessage)
		}
		if info.Version != to {
			busy := info.UpdatePhase == "downloading" || info.UpdatePhase == "verifying" || info.UpdatePhase == "restarting"
			if !busy && info.Started >= asked.Unix() {
				return fmt.Errorf("%s restarted on %s without installing %s (its update failed or was rolled back; see its log)", n.Name, info.Version, to)
			}
			msg := n.Name + " is installing " + to
			if info.UpdateMessage != "" && busy {
				msg = n.Name + ": " + info.UpdateMessage
			}
			rollSetStep(i, "running", msg)
			continue
		}
		switch {
		case info.Role == "MASTER":
			return fmt.Errorf("%s became the active node while it was updated, so the rolling update stopped; check this node", n.Name)
		case info.Role != "BACKUP" || !info.HeartbeatOK:
			stableSince = time.Time{}
			why := info.HeartbeatReason
			if info.Role != "BACKUP" {
				why = "its state is " + roleOrUnknown(info.Role)
			}
			rollSetStep(i, "running", n.Name+" runs "+to+"; waiting for it to be a healthy standby ("+why+")")
			continue
		}
		if stableSince.IsZero() {
			stableSince = time.Now()
		}
		if time.Since(stableSince) >= rollStableFor {
			rollSetStep(i, "done", n.Name+" runs "+to+" and is a healthy standby")
			return nil
		}
		rollSetStep(i, "running", n.Name+" runs "+to+"; checking that it stays healthy")
	}
	return fmt.Errorf("%s did not come back healthy on %s within %s", n.Name, to, rollNodeTimeout)
}

// rollSwitch makes target active; self is this node.
func rollSwitch(i int, cfg *hacluster.Config, key []byte, target, self hacluster.Node) error {
	rollSetStep(i, "running", "Making "+target.Name+" the active node")
	if cfg.Preempt {
		if self.Priority > target.Priority {
			// Stay out of the way until this node runs the new version too
			rollSetHold(rollMaxHold, "rolling update: stays a standby until it runs the new version")
		}
	}
	if err := rollHandOver(cfg, key, target.Name); err != nil {
		roll.mu.Lock()
		roll.holdUntil = time.Time{}
		roll.mu.Unlock()
		return err
	}
	rollSetStep(i, "done", target.Name+" is the active node")
	return nil
}

// rollHandOverDefault is Make active, waiting for the outcome.
func rollHandOverDefault(cfg *hacluster.Config, key []byte, target string) error {
	active, err := haSwitchPlan(cfg, key, target)
	if err != nil {
		return err
	}
	if !active.Self {
		return fmt.Errorf("%s is active now instead of this node", active.Node.Name)
	}
	if _, err := haStepDown(cfg, key, target); err != nil {
		return err
	}
	deadline := time.Now().Add(haSwitchHold + 15*time.Second)
	for {
		time.Sleep(time.Second)
		st := haSwitchStatus()
		if st["state"] == "done" {
			break
		}
		if st["state"] == "failed" {
			return errors.New(fmt.Sprint(st["message"]))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not take over", target)
		}
	}
	// keepalived on this node gives up the floating IPs
	for j := 0; j < 20; j++ {
		if !haVIPHeld(cfg) && haEffectiveRole(haRuntime()).State != "MASTER" {
			return nil
		}
		time.Sleep(time.Second)
	}
	return errors.New("this node still holds the floating IPs after " + target + " took over")
}

// rollUpdateSelf installs to on this node. Returns nil once the restart is
// under way.
func rollUpdateSelf(i int, to string) error {
	rollSetStep(i, "running", "Installing "+to+" on this node")
	if _, err := rollSelfUpdate(to); err != nil {
		return err
	}
	deadline := time.Now().Add(rollNodeTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(rollPoll)
		phase, msg := rollSelfPhase()
		switch phase {
		case "error":
			return errors.New(msg)
		case "restarting":
			rollSetStep(i, "running", "Restarting on "+to)
			return nil
		case "":
			return errors.New("the update on this node stopped without a result")
		}
		rollSetStep(i, "running", msg)
	}
	return errors.New("installing " + to + " on this node took too long")
}

/* After this node restarts */

func rollSelfHealthyDefault() (bool, string) {
	cfg := haConfig()
	ha.mu.Lock()
	hb := ha.heartbeat
	ha.mu.Unlock()
	role := haEffectiveRole(haRuntime()).State
	if !cfg.Enabled {
		return false, "high availability is off"
	}
	if !hb.OK || hb.Time == 0 {
		return false, hb.Reason
	}
	if role != "BACKUP" && role != "MASTER" {
		return false, "its state is " + roleOrUnknown(role)
	}
	return true, ""
}

// initRollingUpdate loads the last rolling update. When Waymark restarted
// for its last step, it confirms that step; any other step was cut short.
func initRollingUpdate() {
	st := rollLoad()
	if st == nil {
		return
	}
	roll.mu.Lock()
	roll.st = st
	roll.mu.Unlock()
	if st.Phase != "running" {
		return
	}
	last := len(st.Steps) - 1
	if last < 0 || st.Steps[last].Action != "updateself" || st.Steps[last].State != "running" {
		msg := "Waymark restarted on this node during the rolling update, so it stopped. Start it again to carry on: nodes that already run " + st.To + " are skipped"
		for i, s := range st.Steps {
			if s.State == "running" {
				rollSetStep(i, "failed", "Cut short by a restart")
			}
		}
		rollFinish("failed", msg)
		return
	}
	if DISPLAY_VERSION != st.To {
		msg := "This node came back on " + DISPLAY_VERSION + " instead of " + st.To + " (its update was rolled back?)"
		rollSetStep(last, "failed", msg)
		rollFinish("failed", msg)
		return
	}
	go func() {
		deadline := time.Now().Add(rollSelfTimeout)
		var stableSince time.Time
		why := ""
		for time.Now().Before(deadline) {
			time.Sleep(rollPoll)
			ok, reason := rollSelfHealthy()
			if !ok {
				stableSince, why = time.Time{}, reason
				rollSetStep(last, "running", "Runs "+st.To+"; waiting for it to be healthy ("+reason+")")
				continue
			}
			if stableSince.IsZero() {
				stableSince = time.Now()
			}
			if time.Since(stableSince) >= rollStableFor {
				rollSetStep(last, "done", "Runs "+st.To+" and is healthy")
				active := ""
				for _, s := range st.Steps {
					if s.Action == "switchover" {
						active = s.Node
					}
				}
				rollFinish("done", rollDoneMessage(haConfig(), st.To, active, st.Steps[last].Node))
				return
			}
			rollSetStep(last, "running", "Runs "+st.To+"; checking that it stays healthy")
		}
		msg := "This node runs " + st.To + " but did not become healthy: " + why
		rollSetStep(last, "failed", msg)
		rollFinish("failed", msg)
	}()
}

/* View */

// rollView is the rolling update to show on this node: its own, or the
// newest one another node reports (the active node runs it).
func rollView() map[string]any {
	cfg := haConfig()
	best := rollSnapshot()
	from, current := haNodeName(cfg), true
	if !rollVisible(best) {
		best = nil
	}
	haSync.mu.Lock()
	for i, n := range cfg.Nodes {
		ps := haSync.peers[n.Address]
		if i == haSelf(cfg) || ps == nil || ps.Info.Rolling == nil || !rollVisible(ps.Info.Rolling) {
			continue
		}
		// The newest one; this node's own copy wins a tie
		if best == nil || ps.Info.Rolling.Started > best.Started {
			best, from, current = ps.Info.Rolling.copy(), n.Name, ps.Err == ""
		}
	}
	haSync.mu.Unlock()
	if best == nil {
		return nil
	}
	// current is false when it comes from a node that did not answer the last
	// poll (for example the active node while it restarts for its own update)
	return map[string]any{"state": best, "reportedBy": from, "current": current}
}

/* API */

// HandleRollingUpdate starts a rolling update (POST version=<x.y.z>) or
// returns the one in progress (GET).
// /api/update/rolling
func HandleRollingUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		js, _ := json.Marshal(map[string]any{"rolling": rollView()})
		utils.SendJSONResponse(w, string(js))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	version, _ := utils.PostPara(r, "version")
	msg, err := rollStart(version)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	js, _ := json.Marshal(map[string]string{"message": msg})
	utils.SendJSONResponse(w, string(js))
}
