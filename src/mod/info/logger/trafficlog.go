package logger

import (
	"net/http"
	"strconv"
	"time"
)

// LogHTTPRequest logs an HTTP request. clientIP is resolved by the caller.
func (l *Logger) LogHTTPRequest(r *http.Request, reqclass string, statusCode int, downstreamHostname string, upstreamHostname string, clientIP string) {
	// Read the request now
	now := time.Now()
	ev := &LogEvent{Stream: StreamTraffic, Time: now, Severity: SeverityInfo, Class: reqclass, Origin: downstreamHostname,
		Client: clientIP, UserAgent: truncateField(r.UserAgent(), 512), Method: r.Method, URI: truncateField(r.RequestURI, 4096),
		Status: statusCode, Upstream: upstreamHostname}
	go func() {
		ev.Text = "[router:" + ev.Class + "] [origin:" + ev.Origin + "] [client: " + ev.Client + "] [useragent: " + ev.UserAgent + "] " + ev.Method + " " + ev.URI + " " + strconv.Itoa(ev.Status)
		l.toSyslog(ev)
		if !l.toFile(StreamTraffic) {
			return
		}
		line := "[" + now.Format("2006-01-02 15:04:05.000000") + "] " + ev.Text
		mu := l.lock()
		mu.Lock()
		defer mu.Unlock()
		l.validateLogFilepathLocked()
		if !l.openLogFileLocked() {
			// No log file
			return
		}
		l.logger.Println(line)
	}()
}

func truncateField(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
