package dpcore

import (
	"io"
	"net/http"
	"sync/atomic"
)

// stickyEOF returns EOF for every read after the request body reached EOF.

type stickyEOF struct {
	rc  io.ReadCloser
	eof atomic.Bool
}

// stickyEOFBody wraps a request body; nil and http.NoBody are returned as is.
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
