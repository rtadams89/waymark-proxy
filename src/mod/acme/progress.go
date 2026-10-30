package acme

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	legolog "github.com/go-acme/lego/v5/log"
	"imuslab.com/zoraxy/mod/utils"
)

// ProgressEntry is one line of a certificate request's progress log.
type ProgressEntry struct {
	Time    int64  `json:"time"` // Unix milliseconds
	Level   string `json:"level"`
	Message string `json:"message"`
}

type progressLog struct {
	mu      sync.Mutex
	entries []ProgressEntry
	done    bool
	errMsg  string
	started time.Time
}

const (
	progressMaxEntries = 500
	progressKeep       = 10 * time.Minute
)

var (
	progressLogs      sync.Map // progress ID -> *progressLog
	progressIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

	// activeProgressID is the progress log that lego messages go to; it is
	// set while a certificate request holds obtainMu.
	activeProgressMu sync.RWMutex
	activeProgressID string

	// obtainMu runs certificate requests one at a time: the lego DNS
	// client and logger are shared by all requests.
	obtainMu sync.Mutex
)

func validProgressID(id string) bool { return progressIDPattern.MatchString(id) }

func getProgressLog(id string, create bool) *progressLog {
	if !validProgressID(id) {
		return nil
	}
	if v, ok := progressLogs.Load(id); ok {
		return v.(*progressLog)
	}
	if !create {
		return nil
	}
	v, loaded := progressLogs.LoadOrStore(id, &progressLog{started: time.Now()})
	if !loaded {
		// Removes a log that is never finished
		time.AfterFunc(2*time.Hour, func() { progressLogs.Delete(id) })
	}
	return v.(*progressLog)
}

// AddProgress appends a message to the progress log with the given ID,
// starting the log if needed. Invalid IDs are ignored.
func AddProgress(id, level, message string) {
	p := getProgressLog(id, true)
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.entries) >= progressMaxEntries {
		p.entries = p.entries[1:]
	}
	p.entries = append(p.entries, ProgressEntry{Time: time.Now().UnixMilli(), Level: level, Message: message})
}

// FinishProgress marks the progress log as done, with err if the request
// failed, and removes it later.
func FinishProgress(id string, err error) {
	p := getProgressLog(id, false)
	if p == nil {
		return
	}
	p.mu.Lock()
	p.done = true
	if err != nil {
		p.errMsg = err.Error()
	}
	p.mu.Unlock()
	time.AfterFunc(progressKeep, func() { progressLogs.Delete(id) })
}

func setActiveProgress(id string) {
	activeProgressMu.Lock()
	activeProgressID = id
	activeProgressMu.Unlock()
}

func currentActiveProgress() string {
	activeProgressMu.RLock()
	defer activeProgressMu.RUnlock()
	return activeProgressID
}

// HandleObtainProgress returns the progress log of a certificate request:
// GET id, from (index of the first entry wanted).
func HandleObtainProgress(w http.ResponseWriter, r *http.Request) {
	id, err := utils.GetPara(r, "id")
	if err != nil || !validProgressID(id) {
		utils.SendErrorResponse(w, "invalid progress ID")
		return
	}
	from := 0
	if s, err := utils.GetPara(r, "from"); err == nil {
		from, _ = strconv.Atoi(s)
	}
	type response struct {
		Entries []ProgressEntry `json:"entries"`
		Next    int             `json:"next"`
		Done    bool            `json:"done"`
		Error   string          `json:"error,omitempty"`
		Started int64           `json:"started"`
		Found   bool            `json:"found"`
	}
	resp := response{Entries: []ProgressEntry{}}
	if p := getProgressLog(id, false); p != nil {
		p.mu.Lock()
		if from < 0 || from > len(p.entries) {
			from = 0
		}
		resp.Entries = append(resp.Entries, p.entries[from:]...)
		resp.Next = len(p.entries)
		resp.Done = p.done
		resp.Error = p.errMsg
		resp.Started = p.started.UnixMilli()
		resp.Found = true
		p.mu.Unlock()
	}
	js, _ := json.Marshal(resp)
	utils.SendJSONResponse(w, string(js))
}

// legoLogHandler sends lego's log messages to the system log and to the
// progress log of the running certificate request.
type legoLogHandler struct {
	a     *ACMEHandler
	attrs []slog.Attr
}

func (h *legoLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

func (h *legoLogHandler) Handle(_ context.Context, rec slog.Record) error {
	parts := []string{}
	add := func(at slog.Attr) bool {
		if at.Key != "" {
			parts = append(parts, at.Key+"="+at.Value.Resolve().String())
		}
		return true
	}
	for _, at := range h.attrs {
		add(at)
	}
	rec.Attrs(add)
	msg := strings.TrimSpace(rec.Message)
	if len(parts) > 0 {
		msg += " (" + strings.Join(parts, ", ") + ")"
	}

	level := "info"
	logMsg := msg
	switch {
	case rec.Level >= slog.LevelError:
		level, logMsg = "error", "Error: "+msg
	case rec.Level >= slog.LevelWarn:
		level, logMsg = "warning", "Warning: "+msg
	}
	if h.a != nil && h.a.Logger != nil {
		h.a.Logger.PrintAndLog("acme", logMsg, nil)
	}
	if id := currentActiveProgress(); id != "" {
		AddProgress(id, level, msg)
	}
	return nil
}

func (h *legoLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := &legoLogHandler{a: h.a, attrs: append(append([]slog.Attr{}, h.attrs...), attrs...)}
	return n
}

func (h *legoLogHandler) WithGroup(_ string) slog.Handler { return h }

// installLegoLogger routes lego's log output through a.
func installLegoLogger(a *ACMEHandler) {
	legolog.SetDefault(slog.New(&legoLogHandler{a: a}))
}
