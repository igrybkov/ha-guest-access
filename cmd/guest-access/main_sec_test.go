package main

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
)

// The servers have a header timeout but no read timeout, so a client can
// send a body one byte a minute and hold a goroutine and a connection for
// as long as it likes. A ReadTimeout does not end a streaming response:
// checked on go1.27.2 with a handler that streams for 3 s under a 500 ms
// ReadTimeout (the request context stays live and writes succeed).
// WriteTimeout must stay unset, since it would cut every event stream. The
// header size is also the Go default, 1 MiB per request.
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
