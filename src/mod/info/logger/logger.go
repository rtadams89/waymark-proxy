package logger

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

/*
	Zoraxy Logger

	This script is designed to make a managed log for the Zoraxy
	and replace the ton of log.Println in the system core.
	The core logger is based in golang's build-in log package
*/

type Logger struct {
	Prefix         string        //Prefix for log files
	LogFolder      string        //Folder to store the log  file
	CurrentLogFile string        //Current writing filename
	RotateOption   *RotateOption //Options for log rotation, see rotate.go

	//Internal
	logRotateTicker *time.Ticker
	logger          *log.Logger
	file            *os.File
	// Custom fork: where each stream goes (outputs_custom.go, syslog_custom.go).
	// A pointer, so Logger values can still be copied (access.Options holds
	// one by value) and copies follow later changes.
	outputs *atomic.Pointer[outputState]
	// Custom fork: guards file, logger, CurrentLogFile and RotateOption, so
	// writes, the month change and rotation do not interleave (a pointer for
	// the same reason as outputs)
	mu *sync.Mutex
	// Custom fork: the log file is opened on the first line written to it,
	// so no file is created while file logging is off (openLogFileLocked)
	openErrLogged bool
	closed        bool // Close was called: no file is opened again
}

// fallbackMu serves Loggers built without a constructor (tests).
var fallbackMu sync.Mutex

func (l *Logger) lock() *sync.Mutex {
	if l.mu != nil {
		return l.mu
	}
	return &fallbackMu
}

// Create a new logger that log to files
func NewLogger(logFilePrefix string, logFolder string) (*Logger, error) {
	err := os.MkdirAll(logFolder, 0775)
	if err != nil {
		return nil, err
	}

	thisLogger := Logger{
		Prefix:    logFilePrefix,
		LogFolder: logFolder,
		outputs:   &atomic.Pointer[outputState]{},
		mu:        &sync.Mutex{},
	}

	//Custom fork: the log file is created when the first line is written to
	//it (openLogFileLocked), so turning file logging off for both streams
	//leaves no empty file behind
	thisLogger.CurrentLogFile = thisLogger.getLogFilepath()

	//Initiate the log rotation ticker
	thisLogger.logRotateTicker = time.NewTicker(1 * time.Hour)
	go func() {
		for range thisLogger.logRotateTicker.C {
			err := thisLogger.RotateLog()
			if err != nil {
				log.Println("Log rotation error: ", err.Error())
			}
		}
	}()

	return &thisLogger, nil
}

// Create a fmt logger that only log to STDOUT
func NewFmtLogger() (*Logger, error) {
	return &Logger{
		Prefix:         "",
		LogFolder:      "",
		CurrentLogFile: "",
		logger:         nil,
		file:           nil,
		outputs:        &atomic.Pointer[outputState]{},
		mu:             &sync.Mutex{},
	}, nil
}

// SetRotateOption will set the log rotation option
func (l *Logger) SetRotateOption(option *RotateOption) {
	mu := l.lock()
	mu.Lock()
	l.RotateOption = option
	mu.Unlock()
}

func (l *Logger) getLogFilepath() string {
	year, month, _ := time.Now().Date()
	return filepath.Join(l.LogFolder, l.Prefix+"_"+strconv.Itoa(year)+"-"+strconv.Itoa(int(month))+".log")
}

// PrintAndLog will log the message to file and print the log to STDOUT
func (l *Logger) PrintAndLog(title string, message string, originalError error) {
	go func() {
		l.Log(title, message, originalError, true)
	}()
}

// Println is a fast snap-in replacement for log.Println
func (l *Logger) Println(v ...interface{}) {
	//Convert the array of interfaces into string
	message := fmt.Sprint(v...)
	go func() {
		l.Log("internal", string(message), nil, true)
	}()
}

func (l *Logger) Log(title string, errorMessage string, originalError error, copyToSTDOUT bool) {
	now := time.Now()
	ev := &LogEvent{Stream: StreamSystem, Time: now, Severity: SeverityInfo, Module: title, Message: errorMessage}
	ev.Text = "[" + title + "] [system:info] " + errorMessage
	if originalError != nil {
		ev.Severity, ev.Error = SeverityError, originalError.Error()
		ev.Text = "[" + title + "] [system:error] " + errorMessage + ": " + originalError.Error()
	}
	line := "[" + now.Format("2006-01-02 15:04:05.000000") + "] " + ev.Text
	fileLogger := l.LogFolder != "" //a NewFmtLogger only prints
	written := false
	if fileLogger && l.toFile(StreamSystem) {
		mu := l.lock()
		mu.Lock()
		l.validateLogFilepathLocked()
		if l.openLogFileLocked() {
			l.logger.Println(line)
			written = true
		}
		mu.Unlock()
	} else if fileLogger {
		written = true //file logging is off for this stream: nothing is lost
	}
	if !written || copyToSTDOUT {
		//Use STDOUT instead of logger
		fmt.Println(line)
	}
	l.toSyslog(ev) //Custom fork: remote syslog (syslog_custom.go)
}

// Validate if the logging target is still valid (detect any months change)
func (l *Logger) ValidateAndUpdateLogFilepath() {
	mu := l.lock()
	mu.Lock()
	defer mu.Unlock()
	l.validateLogFilepathLocked()
}

func (l *Logger) validateLogFilepathLocked() {
	if l.LogFolder == "" || l.closed {
		return
	}
	expectedCurrentLogFilepath := l.getLogFilepath()
	if l.CurrentLogFile != expectedCurrentLogFilepath {
		//Change of month. The new month's file is opened when the first line
		//is written to it (custom fork)
		if l.file != nil {
			l.file.Close()
			l.file = nil
			l.logger = nil
		}

		//Archive the old log file, if one was written
		if _, err := os.Stat(l.CurrentLogFile); err == nil {
			if err := l.ArchiveLog(l.CurrentLogFile); err != nil {
				log.Println("Unable to archive old log file: ", err.Error())
			}
		}
		l.CurrentLogFile = expectedCurrentLogFilepath
	}
}

// openLogFileLocked opens the current log file if it is not open yet
// (custom fork), and reports whether lines can be written to it.
func (l *Logger) openLogFileLocked() bool {
	if l.file != nil && l.logger != nil {
		return true
	}
	if l.closed || l.LogFolder == "" || l.CurrentLogFile == "" {
		return false
	}
	f, err := os.OpenFile(l.CurrentLogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		if !l.openErrLogged {
			l.openErrLogged = true
			log.Println("Unable to open the log file, logging to STDOUT instead: ", err.Error())
		}
		return false
	}
	l.openErrLogged = false
	l.file = f
	//Lines carry their own timestamp
	l.logger = log.New(f, "", 0)
	return true
}

func (l *Logger) Close() {
	mu := l.lock()
	mu.Lock()
	defer mu.Unlock()
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			log.Println("Error closing log file:", err)
		}
		l.file = nil
		l.logger = nil
	}
	l.closed = true
	l.StopLogRotateTicker()
}
