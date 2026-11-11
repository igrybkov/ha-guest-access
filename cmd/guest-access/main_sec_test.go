package main

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
)

// The servers have a header timeout but no read timeout, so a client can
// send a body one byte a minute and hold a goroutine and a connection for
// as long as it likes. A read timeout covers the request only: event
// streams are responses, so they are not affected (WriteTimeout must stay
// unset). The header size is also the Go default, 1 MiB per request.
func TestSecServerBoundsRequestRead(t *testing.T) {
	s := newServer(context.Background(), http.NotFoundHandler(), slog.New(slog.DiscardHandler))
	if s.ReadTimeout <= 0 {
		t.Error("ReadTimeout is not set: a slow body is never cut off")
	}
	if s.WriteTimeout != 0 {
		t.Error("WriteTimeout is set: it would cut every event stream")
	}
	if s.MaxHeaderBytes == 0 || s.MaxHeaderBytes > 64<<10 {
		t.Errorf("MaxHeaderBytes = %d, want a bound of at most 64 KiB", s.MaxHeaderBytes)
	}
}
