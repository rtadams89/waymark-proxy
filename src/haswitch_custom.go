package main

/*
	Manual switch-over: the active node reports itself unhealthy until the
	chosen node is active (or haSwitchHold passes), so keepalived moves the
	floating IPs. Requests are checked, then sent to the active node over
	the sync connection.
*/

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"imuslab.com/zoraxy/mod/hacluster"
	"imuslab.com/zoraxy/mod/utils"
)

// haSwitchHold is the longest this node reports itself unhealthy while
// handing over.
const haSwitchHold = 45 * time.Second

var haSwitch struct {
	mu        sync.Mutex
	holdUntil time.Time
	target    string // node taking over
	state     string // "", switching, done, failed
	message   string
	time      int64 // when state last changed
}

// haSwitchHolding reports whether this node is stepping down right now, and
// the reason written to the heartbeat.
func haSwitchHolding() (bool, string) {
	if holding, why := rollHolding(); holding {
		return true, why
	}
	haSwitch.mu.Lock()
	defer haSwitch.mu.Unlock()
	if time.Now().Before(haSwitch.holdUntil) {
		return true, "handing over to " + haSwitch.target + " (manual switch-over)"
	}
	return false, ""
}

func haSwitchSet(state, message string) {
	haSwitch.mu.Lock()
	haSwitch.state, haSwitch.message, haSwitch.time = state, message, time.Now().Unix()
	if state != "switching" {
		haSwitch.holdUntil = time.Time{}
	}
	haSwitch.mu.Unlock()
}

func haSwitchStatus() map[string]any {
	haSwitch.mu.Lock()
	defer haSwitch.mu.Unlock()
	return map[string]any{"state": haSwitch.state, "target": haSwitch.target, "message": haSwitch.message, "time": haSwitch.time}
}

// haNodeState is one node's current state for a switch-over decision.
type haNodeState struct {
	Node hacluster.Node
	Self bool
	Info haSyncPeerInfo
	Err  error
}

// haFetchNodeStates asks every node for its current state.
func haFetchNodeStates(cfg *hacluster.Config, key []byte) []haNodeState {
	self := haSelf(cfg)
	out := make([]haNodeState, len(cfg.Nodes))
	var wg sync.WaitGroup
	for i, n := range cfg.Nodes {
		out[i].Node = n
		if i == self {
			out[i].Self, out[i].Info = true, haLocalInfo()
			continue
		}
		wg.Add(1)
		go func(i int, n hacluster.Node) {
			defer wg.Done()
			out[i].Err = hacluster.SyncFetch(net.JoinHostPort(n.Address, strconv.Itoa(cfg.SyncPort)), key, "/waymark-ha/v1/state", &out[i].Info, 3*time.Second)
		}(i, n)
	}
	wg.Wait()
	return out
}

// haSwitchPlan checks that target is a reachable, healthy standby with
// keepalived running and exactly one node active, and returns the active node.
func haSwitchPlan(cfg *hacluster.Config, key []byte, target string) (active haNodeState, err error) {
	states := haFetchNodeStates(cfg, key)
	var tgt *haNodeState
	actives := []haNodeState{}
	for i := range states {
		s := &states[i]
		if s.Node.Name == target {
			tgt = s
		}
		if s.Err == nil && s.Info.Role == "MASTER" {
			actives = append(actives, *s)
		}
	}
	if tgt == nil {
		return active, fmt.Errorf("%s is not a node of this cluster", target)
	}
	if tgt.Err != nil {
		if errors.Is(tgt.Err, hacluster.ErrSyncAuth) {
			return active, fmt.Errorf("%s's cluster key does not match", target)
		}
		return active, fmt.Errorf("%s is not reachable: %v", target, tgt.Err)
	}
	switch {
	case len(actives) == 0:
		return active, errors.New("no node is active right now, so there is nothing to switch from")
	case len(actives) > 1:
		return active, errors.New("more than one node reports itself active; fix that before switching")
	case actives[0].Node.Name == target:
		return active, fmt.Errorf("%s is already the active node", target)
	}
	if tgt.Info.Role != "BACKUP" {
		return active, fmt.Errorf("%s is not a standby (its state is %s)", target, roleOrUnknown(tgt.Info.Role))
	}
	if !tgt.Info.HeartbeatOK {
		return active, fmt.Errorf("%s is not healthy: %s", target, tgt.Info.HeartbeatReason)
	}
	if k := tgt.Info.Keepalived; k != "active" && k != "unknown" {
		return active, fmt.Errorf("keepalived is not running on %s (%s)", target, k)
	}
	return actives[0], nil
}

func roleOrUnknown(r string) string {
	if r == "" {
		return "unknown"
	}
	return r
}

// haSwitchKey returns the cluster key when switching is possible.
func haSwitchKey(cfg *hacluster.Config) ([]byte, error) {
	if !cfg.Enabled {
		return nil, errors.New("high availability is off")
	}
	key := hacluster.KeyBytes(cfg.ClusterKey)
	if !cfg.SyncEnabled || key == nil {
		return nil, errors.New("switching from the web interface needs configuration sync on: nodes reach each other over the sync connection")
	}
	if haSelf(cfg) < 0 {
		return nil, errors.New("this machine is not in the cluster's node list")
	}
	return key, nil
}

// haSwitchover makes target the active node, forwarding the request to the
// active node when it is another node.
func haSwitchover(target string) (string, error) {
	cfg := haConfig()
	key, err := haSwitchKey(cfg)
	if err != nil {
		return "", err
	}
	active, err := haSwitchPlan(cfg, key, target)
	if err != nil {
		return "", err
	}
	if active.Self {
		return haStepDown(cfg, key, target)
	}
	var out map[string]string
	addr := net.JoinHostPort(active.Node.Address, strconv.Itoa(cfg.SyncPort))
	if err := hacluster.SyncAction(addr, key, "switchover", map[string]string{"target": target}, &out, 15*time.Second); err != nil {
		return "", fmt.Errorf("could not ask %s to hand over: %v", active.Node.Name, err)
	}
	if out["error"] != "" {
		return "", fmt.Errorf("%s refused: %s", active.Node.Name, out["error"])
	}
	return out["message"], nil
}

// haSwitchAction checks a forwarded switch-over request and steps down.
func haSwitchAction(remote string, body []byte) (any, error) {
	var in struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal(body, &in); err != nil || in.Target == "" {
		return nil, errors.New("no target node given")
	}
	cfg := haConfig()
	key, err := haSwitchKey(cfg)
	if err != nil {
		return nil, err
	}
	active, err := haSwitchPlan(cfg, key, in.Target)
	if err != nil {
		return nil, err
	}
	if !active.Self {
		return nil, fmt.Errorf("this node is not the active one (%s is)", active.Node.Name)
	}
	SystemWideLogger.PrintAndLog("ha", "Switch-over to "+in.Target+" requested by "+remote, nil)
	msg, err := haStepDown(cfg, key, in.Target)
	if err != nil {
		return nil, err
	}
	return map[string]string{"message": msg}, nil
}

// haStepDown reports unhealthy until target is active.
func haStepDown(cfg *hacluster.Config, key []byte, target string) (string, error) {
	var tnode hacluster.Node
	for _, n := range cfg.Nodes {
		if n.Name == target {
			tnode = n
		}
	}
	haSwitch.mu.Lock()
	if time.Now().Before(haSwitch.holdUntil) {
		haSwitch.mu.Unlock()
		return "", errors.New("a switch-over is already in progress")
	}
	haSwitch.holdUntil = time.Now().Add(haSwitchHold)
	haSwitch.target = target
	haSwitch.state, haSwitch.time = "switching", time.Now().Unix()
	haSwitch.message = "Handing over to " + target
	haSwitch.mu.Unlock()
	SystemWideLogger.PrintAndLog("ha", "Manual switch-over: handing the floating IPs to "+target, nil)
	haWriteHeartbeat()

	self := cfg.Nodes[haSelf(cfg)]
	go func() {
		addr := net.JoinHostPort(tnode.Address, strconv.Itoa(cfg.SyncPort))
		deadline := time.Now().Add(haSwitchHold)
		for time.Now().Before(deadline) {
			time.Sleep(time.Second)
			var info haSyncPeerInfo
			if err := hacluster.SyncFetch(addr, key, "/waymark-ha/v1/state", &info, 3*time.Second); err == nil && info.Role == "MASTER" {
				// Give keepalived a moment to settle before this node is healthy again
				time.Sleep(4 * time.Second)
				msg := target + " is now the active node"
				if held, _ := rollHolding(); cfg.Preempt && tnode.Priority < self.Priority && !held {
					msg += ". Preempt is on and " + self.Name + " has the higher priority, so it takes the floating IPs back in about 30 seconds"
				}
				haSwitchSet("done", msg)
				SystemWideLogger.PrintAndLog("ha", "Manual switch-over: "+msg, nil)
				go haSyncTick(false)
				return
			}
		}
		msg := target + " did not take over within " + haSwitchHold.String() + "; " + self.Name + " reports itself healthy again and stays (or becomes) active"
		haSwitchSet("failed", msg)
		SystemWideLogger.PrintAndLog("ha", "Manual switch-over failed: "+msg, nil)
	}()
	return "Handing over to " + target + ": the floating IPs move in a few seconds", nil
}

// HandleHASwitchover makes another node active.
// POST /api/ha/switchover  target=<node name>
func HandleHASwitchover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	target, err := utils.PostPara(r, "target")
	if err != nil || target == "" {
		utils.SendErrorResponse(w, "target not defined")
		return
	}
	if rollRunning() {
		utils.SendErrorResponse(w, "a rolling update is in progress")
		return
	}
	msg, err := haSwitchover(target)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	js, _ := json.Marshal(map[string]string{"message": msg})
	utils.SendJSONResponse(w, string(js))
}
