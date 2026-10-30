package dpcore

import (
	"mime"
	"net/http"
	"strings"
	"time"
)

// getFlushInterval returns the flush interval for a response: -1 (immediate)
// for streams, otherwise FlushInterval.
func (p *ReverseProxy) getFlushInterval(req *http.Request, res *http.Response) time.Duration {
	contentType := res.Header.Get("Content-Type")
	if actualContentType, _, _ := mime.ParseMediaType(contentType); actualContentType == "text/event-stream" {
		return -1
	}

	// Unknown length: streaming
	if res.ContentLength == -1 || p.isBidirectionalStream(req, res) {
		return -1
	}

	// Keep-alive connections
	connectionHeader := req.Header["Connection"]
	if len(connectionHeader) > 0 && strings.Contains(strings.Join(connectionHeader, ","), "keep-alive") {
		return -1
	}

	return p.FlushInterval

}

// isBidirectionalStream reports whether the request and response form a
// bidirectional stream.
func (p *ReverseProxy) isBidirectionalStream(req *http.Request, res *http.Response) bool {
	// Only identity encoding qualifies
	ae := req.Header.Get("Accept-Encoding")

	return req.ProtoMajor == 2 &&
		res.ProtoMajor == 2 &&
		res.ContentLength == -1 &&
		(ae == "identity" || ae == "")
}
