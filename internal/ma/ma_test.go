package ma

import (
	"context"
	"testing"
)

type fake struct{ url string }

func (f fake) PartyURL(context.Context) (string, error) { return f.url, nil }
func (fake) CheckToken(context.Context) error           { return nil }

func TestClientInterface(t *testing.T) {
	var c Client = fake{url: "http://ma.local/party/abc"}
	if u, err := c.PartyURL(context.Background()); err != nil || u == "" {
		t.Fatalf("PartyURL = %q, %v", u, err)
	}
}
