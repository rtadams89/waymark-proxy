package logger

import (
	"os"
	"testing"
)

// Custom fork: with file logging off for both streams no log file is
// created; it appears with the first line written to it.
func TestLogFileCreatedOnFirstWrite(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger("wm", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.ApplyOutputs(&LogOutputs{SystemToFile: false, TrafficToFile: false, Syslog: []SyslogDestination{}})

	l.Log("test", "not written", nil, false)
	l.ValidateAndUpdateLogFilepath()
	if err := l.RotateLog(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.CurrentLogFile); !os.IsNotExist(err) {
		t.Fatalf("log file exists with file logging off (stat error: %v)", err)
	}

	l.ApplyOutputs(&LogOutputs{SystemToFile: true, TrafficToFile: false, Syslog: []SyslogDestination{}})
	l.Log("test", "written", nil, false)
	b, err := os.ReadFile(l.CurrentLogFile)
	if err != nil {
		t.Fatalf("log file not created by the first line: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("log file is empty")
	}
}
