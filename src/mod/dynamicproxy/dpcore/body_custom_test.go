package dpcore

import (
	"errors"
	"io"
	"net/http"
	"testing"
)

// closedAfterEOF behaves like net/http's server request body once the
// response has started: the read after EOF fails.
type closedAfterEOF struct{ reads int }

func (c *closedAfterEOF) Read(p []byte) (int, error) {
	c.reads++
	switch c.reads {
	case 1:
		return copy(p, "data"), io.EOF
	default:
		return 0, http.ErrBodyReadAfterClose
	}
}
func (c *closedAfterEOF) Close() error { return nil }

func TestStickyEOFBody(t *testing.T) {
	b := stickyEOFBody(&closedAfterEOF{})
	buf := make([]byte, 16)
	if n, err := b.Read(buf); n != 4 || err != io.EOF {
		t.Fatalf("first read: %d %v", n, err)
	}
	for i := 0; i < 3; i++ {
		if n, err := b.Read(buf); n != 0 || err != io.EOF {
			t.Fatalf("read after EOF: %d %v, want 0 EOF", n, err)
		}
	}

	// Before EOF, a failure is passed on
	early := stickyEOFBody(io.NopCloser(errReader{}))
	if _, err := early.Read(buf); !errors.Is(err, http.ErrBodyReadAfterClose) {
		t.Fatalf("early error: %v", err)
	}

	if stickyEOFBody(nil) != nil || stickyEOFBody(http.NoBody) != http.NoBody {
		t.Fatal("nil and NoBody must be kept")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, http.ErrBodyReadAfterClose }
