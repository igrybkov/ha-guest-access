package config_test

import (
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/approval"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/inventory"
	"github.com/igrybkov/ha-guest-access/internal/ma"
)

// Package constants are only fallbacks for zero options, but a fallback
// that differs from the option default would change behavior in a way no
// option shows. Each one equals its option default.
func TestPackageDefaultsMatchOptions(t *testing.T) {
	o := config.DefaultOptions()
	rl := o.Settings.RateLimits
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"access.GuardPerIPFails", access.GuardPerIPFails, rl.PasswordFailsPerIP},
		{"access.GuardPerIPWindow", access.GuardPerIPWindow, o.PasswordWindowPerIP},
		{"access.GuardPerLinkFails", access.GuardPerLinkFails, rl.PasswordFailsPerLink},
		{"access.GuardPerLinkWin", access.GuardPerLinkWin, o.PasswordWindowPerLink},
		{"access.GuardBlock", access.GuardBlock, time.Duration(rl.PasswordLockoutMin) * time.Minute},
		{"access.GuardLinkDelay", access.GuardLinkDelay, time.Duration(rl.PasswordLinkDelayS) * time.Second},
		{"approval.DefaultPendingTTL", approval.DefaultPendingTTL, o.Approval.PendingTTL},
		{"approval.DefaultMaxPending", approval.DefaultMaxPending, o.Approval.MaxPending},
		{"approval.DefaultMaxDevices", approval.DefaultMaxDevices, o.Approval.MaxDevices},
		{"approval.DefaultEventsPerHour", approval.DefaultEventsPerHour, o.Approval.EventsPerHour},
		{"approval.DefaultTouchThrottle", approval.DefaultTouchThrottle, o.Approval.TouchThrottle},
		{"approval.DefaultMaxBodyBytes", int64(approval.DefaultMaxBodyBytes), o.GuestBodyBytes},
		{"approval.DefaultEventTimeout", approval.DefaultEventTimeout, o.HAEventTimeout},
		{"ma.Timeout", ma.Timeout, o.MATimeout},
		{"inventory.MaxAge", inventory.MaxAge, o.InventoryMaxAge},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, option default %v", c.name, c.got, c.want)
		}
	}
}
