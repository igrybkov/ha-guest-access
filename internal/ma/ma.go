// Package ma talks to Music Assistant for the party link. It is optional:
// with no URL or token set, the app never builds a Client and never calls
// MA.
package ma

import (
	"context"
	"errors"
	"time"
)

// Timeout bounds every MA call. A slow MA only hides the party button.
const Timeout = 2 * time.Second

// ErrUnavailable wraps every failure: MA down, timed out, or the token
// refused. Callers hide the party button and log it.
var ErrUnavailable = errors.New("music assistant unavailable")

// Client is the part of the MA API the app uses.
type Client interface {
	// PartyURL returns the guest party URL (MA's "party/url" command,
	// JSON-RPC over POST /api). It is fetched fresh on each guest page
	// load.
	PartyURL(ctx context.Context) (string, error)
	// CheckToken reports whether MA takes the token, with a command that
	// changes nothing in MA. It returns nil, ErrUnauthorized, or another
	// error that wraps ErrUnavailable.
	CheckToken(ctx context.Context) error
}
