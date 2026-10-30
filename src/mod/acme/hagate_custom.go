package acme

// Automatic certificate renewal hold for HA standby nodes.

// RenewalHold, when set, reports whether automatic renewal is held on this
// node, and why.
var RenewalHold func() (hold bool, reason string)

func (a *AutoRenewer) renewalHeld() bool {
	if RenewalHold == nil {
		return false
	}
	if hold, reason := RenewalHold(); hold {
		a.Logf("Automatic certificate renewal skipped: "+reason, nil)
		return true
	}
	return false
}
