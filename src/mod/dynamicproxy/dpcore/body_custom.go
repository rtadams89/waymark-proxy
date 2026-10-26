package dpcore

import (
	"io"
	"net/http"
	"sync/atomic"
)

/*
	body_custom.go (custom fork): the request body sent upstream.

	The transport may read the request body once more after it has returned
	EOF. Meanwhile, once the response to the client has started (a streamed
	response is flushed as soon as its first bytes arrive), net/http's
	server closes the request body, so that extra read fails with "invalid
	Read on closed Body". The transport takes it as a failure to send the
	request and closes the upstream connection, cutting the response short.
	Once the body has reached EOF, stickyEOF answers every later read with
	EOF itself.
*/

type stickyEOF struct {
	rc  io.ReadCloser
	eof atomic.Bool
}

// stickyEOFBody wraps a request body (nil and http.NoBody are kept as they
// are, the transport treats them specially).
func stickyEOFBody(rc io.ReadCloser) io.ReadCloser {
	if rc == nil || rc == http.NoBody {
		return rc
	}
	return &stickyEOF{rc: rc}
}

func (b *stickyEOF) Read(p []byte) (int, error) {
	if b.eof.Load() {
		return 0, io.EOF
	}
	n, err := b.rc.Read(p)
	if err == io.EOF {
		b.eof.Store(true)
	}
	return n, err
}

func (b *stickyEOF) Close() error { return b.rc.Close() }
