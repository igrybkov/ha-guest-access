package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSelfInfo(t *testing.T) {
	const token = "test-token"
	for _, c := range []struct {
		name    string
		status  int
		body    string
		want    string
		wantErr string
	}{
		{"ok", 200, `{"result":"ok","data":{"slug":"1a2b3c4d_guest_access","ingress_url":"/api/hassio_ingress/x/","ingress_panel":true}}`, "1a2b3c4d_guest_access", ""},
		{"local app", 200, `{"result":"ok","data":{"slug":"local_guest_access"}}`, "local_guest_access", ""},
		{"error status", 403, `{"result":"error"}`, "", "403"},
		{"error result", 200, `{"result":"error","message":"x"}`, "", "result"},
		{"no slug", 200, `{"result":"ok","data":{}}`, "", "slug"},
		{"slug with markdown", 200, `{"result":"ok","data":{"slug":"a)[x](https://evil"}}`, "", "slug"},
		{"slug with a slash", 200, `{"result":"ok","data":{"slug":"a/../b"}}`, "", "slug"},
		{"not json", 200, `<html>`, "", "self info"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/addons/self/info" || r.Header.Get("Authorization") != "Bearer "+token {
					http.Error(w, "no", http.StatusUnauthorized)
					return
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			info, err := Client{URL: srv.URL, Token: token}.SelfInfo(context.Background())
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				if strings.Contains(err.Error(), token) {
					t.Fatal("the error holds the token")
				}
				return
			}
			if err != nil || info.Slug != c.want {
				t.Fatalf("SelfInfo = %+v, %v; want slug %q", info, err, c.want)
			}
		})
	}
}

func TestPanelPath(t *testing.T) {
	if got := PanelPath("local_guest_access", "/pending"); got != "/app/local_guest_access/pending" {
		t.Fatalf("PanelPath = %q", got)
	}
}
