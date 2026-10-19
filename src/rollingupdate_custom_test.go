package main

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"imuslab.com/zoraxy/mod/hacluster"
	"imuslab.com/zoraxy/mod/info/logger"
)

// fakeCluster simulates the other nodes for a rolling update.
type fakeCluster struct {
	mu       sync.Mutex
	cfg      *hacluster.Config
	info     map[string]haSyncPeerInfo
	polls    map[string]int // polls since the update was asked for
	asked    []string
	handOver []string
	selfUpd  []string
	// behaviour per node once asked: "ok", "error", "rollback", "master", "down"
	behave   map[string]string
	selfFail error
	handErr  error
}

func newFakeCluster(t *testing.T) *fakeCluster {
	t.Helper()
	if SystemWideLogger == nil {
		SystemWideLogger, _ = logger.NewFmtLogger()
	}
	f := &fakeCluster{
		cfg: &hacluster.Config{Enabled: true, SyncEnabled: true, Preempt: true, Nodes: []hacluster.Node{
			{Name: "a", Address: "10.0.0.1", Priority: 150},
			{Name: "b", Address: "10.0.0.2", Priority: 100},
			{Name: "c", Address: "10.0.0.3", Priority: 50},
		}},
		info: map[string]haSyncPeerInfo{
			"a": {Node: "a", Role: "MASTER", Version: "2.10.0", HeartbeatOK: true, Keepalived: "active"},
			"b": {Node: "b", Role: "BACKUP", Version: "2.10.0", HeartbeatOK: true, Keepalived: "active", Started: 1, SyncStatus: "in-sync"},
			"c": {Node: "c", Role: "BACKUP", Version: "2.10.0", HeartbeatOK: true, Keepalived: "active", Started: 1, SyncStatus: "in-sync"},
		},
		polls:  map[string]int{},
		behave: map[string]string{"b": "ok", "c": "ok"},
	}
	dir := t.TempDir()
	saved := []any{rollFetch, rollFetchOne, rollAskUpdate, rollHandOver, rollSelfUpdate, rollSelfPhase, rollStatePath, rollPoll, rollStableFor, rollNodeTimeout}
	t.Cleanup(func() {
		rollFetch = saved[0].(func(*hacluster.Config, []byte) []haNodeState)
		rollFetchOne = saved[1].(func(*hacluster.Config, []byte, hacluster.Node) (haSyncPeerInfo, error))
		rollAskUpdate = saved[2].(func(*hacluster.Config, []byte, hacluster.Node, string) error)
		rollHandOver = saved[3].(func(*hacluster.Config, []byte, string) error)
		rollSelfUpdate = saved[4].(func(string) (string, error))
		rollSelfPhase = saved[5].(func() (string, string))
		rollStatePath = saved[6].(func() string)
		rollPoll, rollStableFor, rollNodeTimeout = saved[7].(time.Duration), saved[8].(time.Duration), saved[9].(time.Duration)
		roll.mu.Lock()
		roll.st, roll.holdUntil = nil, time.Time{}
		roll.mu.Unlock()
	})
	rollPoll, rollStableFor, rollNodeTimeout = time.Millisecond, 5*time.Millisecond, 2*time.Second
	rollStatePath = func() string { return filepath.Join(dir, rollStateFile) }
	rollFetch = f.fetch
	rollFetchOne = f.fetchOne
	rollAskUpdate = f.ask
	rollHandOver = f.hand
	rollSelfUpdate = func(v string) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.selfUpd = append(f.selfUpd, v)
		if f.selfFail != nil {
			return "", f.selfFail
		}
		return "ok", nil
	}
	rollSelfPhase = func() (string, string) { return "restarting", "Restarting" }
	return f
}

func (f *fakeCluster) fetch(cfg *hacluster.Config, key []byte) []haNodeState {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []haNodeState{}
	for _, n := range f.cfg.Nodes {
		out = append(out, haNodeState{Node: n, Self: n.Name == "a", Info: f.info[n.Name]})
	}
	return out
}

func (f *fakeCluster) ask(cfg *hacluster.Config, key []byte, n hacluster.Node, v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, n.Name)
	f.polls[n.Name] = 0
	return nil
}

// fetchOne: two polls installing, one poll down, then the outcome.
func (f *fakeCluster) fetchOne(cfg *hacluster.Config, key []byte, n hacluster.Node) (haSyncPeerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls[n.Name]++
	p := f.polls[n.Name]
	info := f.info[n.Name]
	switch {
	case p <= 2:
		info.UpdatePhase, info.UpdateMessage = "downloading", "Downloading Waymark 2.11.0"
	case p == 3 || f.behave[n.Name] == "down":
		return info, errors.New("connection refused")
	default:
		info.Started = time.Now().Unix()
		switch f.behave[n.Name] {
		case "ok":
			info.Version = "2.11.0"
		case "master":
			info.Version, info.Role = "2.11.0", "MASTER"
		case "error":
			info.UpdatePhase, info.UpdateMessage = "error", "Update to 2.11.0 failed: checksum"
		case "rollback":
			// back on the old version
		}
		f.info[n.Name] = info
	}
	return info, nil
}

func (f *fakeCluster) hand(cfg *hacluster.Config, key []byte, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handOver = append(f.handOver, target)
	return f.handErr
}

func waitRoll(t *testing.T) *rollState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := rollSnapshot()
		if st != nil && st.Phase != "running" {
			return st
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("rolling update did not finish")
	return nil
}

func startRoll(t *testing.T, f *fakeCluster) []rollStep {
	t.Helper()
	steps, err := rollPlan(f.fetch(nil, nil), "2.11.0")
	if err != nil {
		t.Fatal(err)
	}
	roll.mu.Lock()
	roll.st = &rollState{Coordinator: "a", From: "2.10.0", To: "2.11.0", Phase: "running", Started: time.Now().Unix(), Steps: steps}
	roll.mu.Unlock()
	go rollRun(f.cfg, []byte("k"), "2.11.0", steps)
	return steps
}

func TestRollPlanOrder(t *testing.T) {
	f := newFakeCluster(t)
	steps, err := rollPlan(f.fetch(nil, nil), "2.11.0")
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, s := range steps {
		got = append(got, s.Action+":"+s.Node)
	}
	// lowest priority standby first, then the highest priority one becomes active, then this node
	if strings.Join(got, ",") != "update:c,update:b,switchover:b,updateself:a" {
		t.Fatalf("steps %v", got)
	}
}

func TestRollPlanRefuses(t *testing.T) {
	cases := map[string]func(f *fakeCluster){
		"not reachable": func(f *fakeCluster) { i := f.info["b"]; i.Role = ""; f.info["b"] = i },
		"not healthy": func(f *fakeCluster) {
			i := f.info["c"]
			i.HeartbeatOK, i.HeartbeatReason = false, "proxy down"
			f.info["c"] = i
		},
		"also reports": func(f *fakeCluster) { i := f.info["c"]; i.Role = "MASTER"; f.info["c"] = i },
		"only the active": func(f *fakeCluster) {
			i := f.info["a"]
			i.Role = "BACKUP"
			f.info["a"] = i
			j := f.info["b"]
			j.Role = "MASTER"
			f.info["b"] = j
		},
		"already installing": func(f *fakeCluster) { i := f.info["b"]; i.UpdatePhase = "downloading"; f.info["b"] = i },
		"newer":              func(f *fakeCluster) { i := f.info["b"]; i.Version = "2.12.0"; f.info["b"] = i },
		"keepalived":         func(f *fakeCluster) { i := f.info["b"]; i.Keepalived = "failed"; f.info["b"] = i },
		"already runs": func(f *fakeCluster) {
			for k, i := range f.info {
				i.Version = "2.11.0"
				f.info[k] = i
			}
		},
	}
	for want, mod := range cases {
		f := newFakeCluster(t)
		mod(f)
		_, err := rollPlan(f.fetch(nil, nil), "2.11.0")
		if want == "not reachable" {
			// an empty role on a standby
			want = "not a standby"
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
	// unreachable node
	f := newFakeCluster(t)
	states := f.fetch(nil, nil)
	states[2].Err = errors.New("timeout")
	if _, err := rollPlan(states, "2.11.0"); err == nil || !strings.Contains(err.Error(), "c is not reachable") {
		t.Errorf("unreachable: %v", err)
	}
}

func TestRollPlanSkipsUpdatedNodes(t *testing.T) {
	f := newFakeCluster(t)
	i := f.info["c"]
	i.Version = "2.11.0"
	f.info["c"] = i
	steps, err := rollPlan(f.fetch(nil, nil), "2.11.0")
	if err != nil {
		t.Fatal(err)
	}
	if steps[0].Node != "c" || steps[0].State != "skipped" || steps[1].State != "pending" {
		t.Fatalf("%+v", steps)
	}
	// This node already updated: only the standbys
	f = newFakeCluster(t)
	i = f.info["a"]
	i.Version = "2.11.0"
	f.info["a"] = i
	steps, _ = rollPlan(f.fetch(nil, nil), "2.11.0")
	if len(steps) != 2 || steps[1].Action != "update" {
		t.Fatalf("%+v", steps)
	}
}

func TestRollRunHappyPath(t *testing.T) {
	f := newFakeCluster(t)
	startRoll(t, f)
	// rollRun returns once this node restarts; the state stays running
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := rollSnapshot()
		if s := st.Steps[3]; s.State == "running" && strings.HasPrefix(s.Message, "Restarting") {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	st := rollSnapshot()
	if st.Phase != "running" {
		t.Fatalf("phase %s: %s", st.Phase, st.Message)
	}
	if strings.Join(f.asked, ",") != "c,b" || strings.Join(f.handOver, ",") != "b" || strings.Join(f.selfUpd, ",") != "2.11.0" {
		t.Fatalf("asked %v handOver %v self %v", f.asked, f.handOver, f.selfUpd)
	}
	for _, s := range st.Steps[:3] {
		if s.State != "done" {
			t.Fatalf("step %+v", s)
		}
	}
	// Preempt on and this node has the higher priority: it stays unhealthy until it restarts
	if h, _ := haSwitchHolding(); !h {
		t.Fatal("expected this node to hold off")
	}

	// After the restart on the new version, the last step is confirmed
	// (this test binary runs DISPLAY_VERSION, so pretend that is the new version)
	savedHealthy := rollSelfHealthy
	rollSelfHealthy = func() (bool, string) { return true, "" }
	defer func() { rollSelfHealthy = savedHealthy }()
	roll.mu.Lock()
	roll.st.To = DISPLAY_VERSION
	rollSave()
	roll.st, roll.holdUntil = nil, time.Time{}
	roll.mu.Unlock()
	initRollingUpdate()
	st = waitRoll(t)
	if st.Phase != "done" || st.Steps[3].State != "done" || !strings.Contains(st.Message, "b is the active node") {
		t.Fatalf("%+v", st)
	}
}

func TestRollRunStopsAtFirstProblem(t *testing.T) {
	for behave, want := range map[string]string{
		"error":    "checksum",
		"rollback": "without installing 2.11.0",
		"master":   "became the active node",
		"down":     "did not come back",
	} {
		f := newFakeCluster(t)
		f.behave["c"] = behave
		if behave == "down" {
			rollNodeTimeout = 50 * time.Millisecond
		}
		startRoll(t, f)
		st := waitRoll(t)
		if st.Phase != "failed" || !strings.Contains(st.Message, want) || st.Steps[0].State != "failed" {
			t.Errorf("%s: %+v", behave, st)
		}
		// Nothing after the failed node
		if len(f.asked) != 1 || len(f.handOver) != 0 || len(f.selfUpd) != 0 {
			t.Errorf("%s: went on: asked %v handOver %v self %v", behave, f.asked, f.handOver, f.selfUpd)
		}
		for _, s := range st.Steps[1:] {
			if s.State != "pending" {
				t.Errorf("%s: step %+v", behave, s)
			}
		}
	}
}

func TestRollRunHandOverFails(t *testing.T) {
	f := newFakeCluster(t)
	f.handErr = errors.New("b did not take over within 45s")
	startRoll(t, f)
	st := waitRoll(t)
	if st.Phase != "failed" || st.Steps[2].State != "failed" || len(f.selfUpd) != 0 {
		t.Fatalf("%+v self %v", st, f.selfUpd)
	}
	if h, _ := haSwitchHolding(); h {
		t.Fatal("this node should report healthy again")
	}
}

func TestRollRestartMidway(t *testing.T) {
	f := newFakeCluster(t)
	steps, _ := rollPlan(f.fetch(nil, nil), "2.11.0")
	steps[0].State = "running"
	roll.mu.Lock()
	roll.st = &rollState{Coordinator: "a", To: "2.11.0", Phase: "running", Started: 1, Steps: steps}
	rollSave()
	roll.st = nil
	roll.mu.Unlock()
	initRollingUpdate()
	st := rollSnapshot()
	if st.Phase != "failed" || st.Steps[0].State != "failed" || !strings.Contains(st.Message, "Start it again") {
		t.Fatalf("%+v", st)
	}
}

func TestRollSelfCameBackOnOldVersion(t *testing.T) {
	f := newFakeCluster(t)
	steps, _ := rollPlan(f.fetch(nil, nil), "9.0.0")
	for i := range steps {
		steps[i].State = "done"
	}
	steps[3].State = "running"
	roll.mu.Lock()
	roll.st = &rollState{Coordinator: "a", To: "9.0.0", Phase: "running", Started: 1, Steps: steps}
	rollSave()
	roll.st = nil
	roll.mu.Unlock()
	initRollingUpdate() // DISPLAY_VERSION is not 2.11.0
	st := rollSnapshot()
	if st.Phase != "failed" || st.Steps[3].State != "failed" || !strings.Contains(st.Message, "instead of 9.0.0") {
		t.Fatalf("%+v", st)
	}
}
