package logger

/*
	Traffic Log

	This script log the traffic of HTTP requests

*/
import (
	"net/http"
	"strconv"
	"time"
)

// Log HTTP request. Note that this must run in go routine to prevent any blocking
// in reverse proxy router.
//
// clientIP must be resolved by the caller (e.g. via the access rule / trusted proxy
// aware resolver) instead of being derived here, so that untrusted proxy headers
// (X-Forwarded-For, X-Real-IP, etc.) cannot be used to spoof the logged source IP.
func (l *Logger) LogHTTPRequest(r *http.Request, reqclass string, statusCode int, downstreamHostname string, upstreamHostname string, clientIP string) {
	//Read the request now: it may be reused once the handler returns
	now := time.Now()
	ev := &LogEvent{Stream: StreamTraffic, Time: now, Severity: SeverityInfo, Class: reqclass, Origin: downstreamHostname,
		Client: clientIP, UserAgent: truncateField(r.UserAgent(), 512), Method: r.Method, URI: truncateField(r.RequestURI, 4096),
		Status: statusCode, Upstream: upstreamHostname}
	go func() {
		ev.Text = "[router:" + ev.Class + "] [origin:" + ev.Origin + "] [client: " + ev.Client + "] [useragent: " + ev.UserAgent + "] " + ev.Method + " " + ev.URI + " " + strconv.Itoa(ev.Status)
		l.toSyslog(ev) //Custom fork: remote syslog (syslog_custom.go)
		if !l.toFile(StreamTraffic) {
			return
		}
		line := "[" + now.Format("2006-01-02 15:04:05.000000") + "] " + ev.Text
		mu := l.lock()
		mu.Lock()
		defer mu.Unlock()
		l.validateLogFilepathLocked()
		if !l.openLogFileLocked() {
			//No log file (a NewFmtLogger, or it cannot be opened). Do not log http request
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
