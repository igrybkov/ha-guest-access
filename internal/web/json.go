package web

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"time"
)

// Guest-facing messages. They are safe to show and never carry details.
const (
	msgBadRequest = "Bad request."
	msgForbidden  = "Not allowed."
	msgNotFound   = "Not found."
	msgGone       = "This link is not active."
	msgTooMany    = "Too many requests. Try again later."
	msgPassword   = "Password required."
	msgWrongPass  = "Wrong password."
	msgUsed       = "This was already used."
	msgHAFailed   = "Could not reach the house. Try again."
	msgInternal   = "Something went wrong."
	msgTooLarge   = "Request too large."
	msgJSONOnly   = "Send JSON."
	msgOrigin     = "Cross-site request refused."
)

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

// tooMany answers 429 with Retry-After in whole seconds, at least 1.
func tooMany(w http.ResponseWriter, retry time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(retry)))
	writeError(w, http.StatusTooManyRequests, msgTooMany)
}

func retrySeconds(d time.Duration) int {
	return int(max(1, math.Ceil(d.Seconds())))
}

// errBodyTooLarge marks a body over the cap.
var errBodyTooLarge = errors.New("body too large")

// decodeStrict reads exactly one JSON value from rd into v. Unknown
// fields and trailing data are refused. Numbers decode as float64 into
// any-typed fields (no UseNumber), which is what authz expects.
func decodeStrict(rd io.Reader, v any) error {
	dec := json.NewDecoder(rd)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errBodyTooLarge
		}
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errBodyTooLarge
		}
		return errors.New("trailing data after JSON value")
	}
	return nil
}

// decodeBody decodes r's body with a cap of limit bytes.
func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	return decodeStrict(http.MaxBytesReader(w, r.Body, limit), v)
}

// isJSON reports whether r says its body is JSON.
func isJSON(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}
