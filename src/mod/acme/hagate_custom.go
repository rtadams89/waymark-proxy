package acme

// Custom fork: in an active/passive pair (mod/hacluster), only the active
// node renews certificates automatically; the standby receives them through
// config sync. Manual "Renew now" is not affected.

// RenewalHold, when set, reports whether automatic renewal should be
// skipped on this node, and why.
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
