package logger

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Custom fork: rotation while other goroutines log must not lose lines or
// write to a closed file, and every line has exactly one timestamp.
func TestRotateWhileLogging(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger("wm", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.SetRotateOption(&RotateOption{Enabled: true, MaxSize: 1, MaxBackups: 100})

	const writers, perWriter = 8, 200
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				l.Log("test", "line", nil, false)
			}
		}()
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				if err := l.RotateLog(); err != nil {
					t.Error(err)
					return
				}
			}
		}
	}()
	wg.Wait()
	close(stop)
	<-done

	files, _ := filepath.Glob(filepath.Join(dir, "wm_*"))
	lines := 0
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			text := sc.Text()
			if strings.Contains(text, "[test]") {
				lines++
			}
			if strings.Count(text, "[20") != 1 {
				t.Errorf("line with %d timestamps: %q", strings.Count(text, "[20"), text)
			}
		}
		fh.Close()
	}
	if lines != writers*perWriter {
		t.Fatalf("found %d lines, want %d", lines, writers*perWriter)
	}
}
