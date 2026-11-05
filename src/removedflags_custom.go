package main

// Removed flags: accepted, ignored and logged. -reset_ac exits.

import (
	"flag"
	"fmt"
	"os"
	"sort"
)

// removedFlag accepts -name, -name=true and -name=false.
type removedFlag struct{ set bool }

func (f *removedFlag) String() string   { return "" }
func (f *removedFlag) IsBoolFlag() bool { return true }
func (f *removedFlag) Set(string) error { f.set = true; return nil }

// removedFlagNotes: flag name -> note for the log
var removedFlagNotes = map[string]string{
	"noauth":                  "sign-in to the admin console is always on",
	"reset_ac":                "use -set-admin-password, which sets a password without deleting the admin accounts",
	"default_inbound_enabled": "the proxy always starts",
	"cfgupgrade":              "it did nothing",
}

var removedFlags = map[string]*removedFlag{}

func init() {
	for name := range removedFlagNotes {
		f := &removedFlag{}
		removedFlags[name] = f
		flag.Var(f, name, "Removed in Waymark 3.0.0 and ignored: "+removedFlagNotes[name])
	}
}

func removedFlagWarnings() []string {
	names := []string{}
	for name, f := range removedFlags {
		if f.set {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	warnings := []string{}
	for _, name := range names {
		warnings = append(warnings, "Warning: -"+name+" was removed in Waymark 3.0.0 and is ignored ("+removedFlagNotes[name]+"). Remove it from the service's ExecStart.")
	}
	return warnings
}

// refuseRemovedCommands exits when -reset_ac was given.
func refuseRemovedCommands() {
	if removedFlags["reset_ac"].set {
		fmt.Fprintln(os.Stderr, "-reset_ac was removed in Waymark 3.0.0. To set an admin password, stop Waymark and run waymark -set-admin-password in Waymark's folder, as the user it runs as.")
		os.Exit(1)
	}
}

func logRemovedFlags() {
	for _, w := range removedFlagWarnings() {
		SystemWideLogger.PrintAndLog("system", w, nil)
	}
}
