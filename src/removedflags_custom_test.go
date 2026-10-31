package main

import (
	"flag"
	"strings"
	"testing"
)

func TestRemovedFlagsAreAcceptedAndIgnored(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	flags := map[string]*removedFlag{}
	for name := range removedFlagNotes {
		flags[name] = &removedFlag{}
		fs.Var(flags[name], name, "")
	}
	if err := fs.Parse([]string{"-noauth", "-cfgupgrade=true", "-default_inbound_enabled=false", "-port", ":1"}); err == nil {
		// -port is not defined in this set, so parsing stops there with an error
		t.Fatal("expected an error for the undefined -port")
	}
	for _, name := range []string{"noauth", "cfgupgrade", "default_inbound_enabled"} {
		if !flags[name].set {
			t.Errorf("-%s was not recorded", name)
		}
	}
	if flags["reset_ac"].set {
		t.Error("-reset_ac was recorded without being given")
	}
}

func TestRemovedFlagWarnings(t *testing.T) {
	saved := map[string]bool{}
	for name, f := range removedFlags {
		saved[name] = f.set
		f.set = false
	}
	defer func() {
		for name, f := range removedFlags {
			f.set = saved[name]
		}
	}()
	if w := removedFlagWarnings(); len(w) != 0 {
		t.Fatalf("warnings without removed flags: %v", w)
	}
	removedFlags["noauth"].set = true
	w := removedFlagWarnings()
	if len(w) != 1 || !strings.Contains(w[0], "-noauth") || !strings.Contains(w[0], "always on") {
		t.Fatalf("unexpected warnings: %v", w)
	}
}
