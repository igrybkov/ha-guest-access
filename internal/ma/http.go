package ma

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrUnauthorized means MA refused the token (401 or 403). It wraps
// ErrUnavailable, so callers that only hide the party button need not
// check it; the admin UI uses it to warn about the token.
var ErrUnauthorized = fmt.Errorf("token refused: %w", ErrUnavailable)

// maxResponse caps how much of an MA reply is read. A join URL is short.
const maxResponse = 64 << 10

// HTTPClient calls MA's JSON-RPC endpoint (POST <url>/api), as in
// music_assistant/controllers/webserver/controller.py,
// _handle_jsonrpc_api_command. MA replies with the bare command result as
// JSON: a string, or null when party guest access is off.
type HTTPClient struct {
	endpoint string
	token    string
	timeout  time.Duration // Timeout; tests shorten it
	http     *http.Client
}

var _ Client = (*HTTPClient)(nil)

// New returns a Client for the MA at baseURL, such as
// http://d5369777-music-assistant:8095, with a long-lived MA token. It
// returns a nil Client, and no error, when baseURL or token is empty: MA
// is optional, and a nil Client means never call it. baseURL must be http
// or https.
func New(baseURL, token string) (Client, error) {
	return NewWithTimeout(baseURL, token, Timeout)
}

// NewWithTimeout is New with its own bound on every call. A timeout of 0
// or less means Timeout.
func NewWithTimeout(baseURL, token string, timeout time.Duration) (Client, error) {
	if timeout <= 0 {
		timeout = Timeout
	}
	if baseURL == "" || token == "" {
		return nil, nil
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, errors.New("music assistant url must be an http or https URL")
	}
	endpoint, err := url.JoinPath(baseURL, "api")
	if err != nil {
		return nil, fmt.Errorf("music assistant url: %w", err)
	}
	return &HTTPClient{
		endpoint: endpoint,
		token:    token,
		timeout:  timeout,
		http: &http.Client{
			// A redirect could send the token to another host. MA does not
			// redirect its API, so treat one as a failure.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// tokenCheckCommand is the command CheckToken sends. In the MA server it
// is "auth/me" (music_assistant/controllers/webserver/auth.py,
// AuthenticationManager.get_current_user_info): it needs an authenticated
// token, asks for no scope, and only reads the caller's user record. A
// bad token gets 401 from _authenticate_api_command in
// music_assistant/controllers/webserver/controller.py. "party/url" is no
// good here: it creates a join code and a guest user when none is active.
const tokenCheckCommand = "auth/me"

// PartyURL runs "party/url". Every failure wraps ErrUnavailable. Errors
// never hold the token or the join URL, which carries a join code. It may
// make MA create a join code, so call it only for a guest's tap.
func (c *HTTPClient) PartyURL(ctx context.Context) (string, error) {
	raw, err := c.call(ctx, "party/url")
	if err != nil {
		return "", err
	}
	return parsePartyURL(raw)
}

// CheckToken runs tokenCheckCommand and drops the reply. It changes
// nothing in MA.
func (c *HTTPClient) CheckToken(ctx context.Context) error {
	_, err := c.call(ctx, tokenCheckCommand)
	return err
}

// call sends one command with no arguments and returns the raw reply.
// 401 and 403 are ErrUnauthorized; every other failure wraps
// ErrUnavailable.
func (c *HTTPClient) call(ctx context.Context, command string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"message_id": messageID(), "command": command, "args": map[string]any{}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, errors.Unwrap(err)) // *url.Error repeats the URL
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrUnauthorized
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read: %v", ErrUnavailable, err)
	}
	if len(raw) > maxResponse {
		return nil, fmt.Errorf("%w: reply over %d bytes", ErrUnavailable, maxResponse)
	}
	return raw, nil
}

// parsePartyURL reads MA's reply to "party/url". Only an absolute http or
// https URL with a host is accepted; null (guest access off), an empty
// string, any other JSON, or another scheme such as javascript: is
// ErrUnavailable.
func parsePartyURL(raw []byte) (string, error) {
	var s *string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%w: reply is not a JSON string", ErrUnavailable)
	}
	if s == nil {
		return "", fmt.Errorf("%w: party guest access is off", ErrUnavailable)
	}
	u, err := url.Parse(*s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		strings.ContainsAny(*s, " \t\r\n\"'<>\\") {
		return "", fmt.Errorf("%w: reply is not an http or https URL", ErrUnavailable)
	}
	return *s, nil
}

func messageID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return hex.EncodeToString(b)
}
