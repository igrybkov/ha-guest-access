package web

import (
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/config"
)

// The fallbacks New uses for zero options equal the option defaults.
func TestWebDefaultsMatchOptions(t *testing.T) {
	o := config.DefaultOptions()
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"defaultHACall", defaultHACall, o.HACallTimeout},
		{"defaultHAEvent", defaultHAEvent, o.HAEventTimeout},
		{"defaultStreamCheck", defaultStreamCheck, o.StreamCheck},
		{"defaultHeartbeat", defaultHeartbeat, o.StreamHeartbeat},
		{"defaultStreamRetry", defaultStreamRetry, o.StreamRetry},
		{"defaultGuestBody", int64(defaultGuestBody), o.GuestBodyBytes},
		{"defaultAdminBody", int64(defaultAdminBody), o.AdminBodyBytes},
		{"defaultNearTTL", time.Duration(defaultNearTTL), o.NearTTL},
		{"defaultMaxAccuracyM", defaultMaxAccuracyM, o.MaxAccuracyM},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, option default %v", c.name, c.got, c.want)
		}
	}
}
