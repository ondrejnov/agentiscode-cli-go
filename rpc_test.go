package agentiscode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRPCHeadersPayloadAndNormalization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Error(r.Method, r.URL.Path, r.Header)
		}
		var p Object
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if p["jsonrpc"] != "2.0" || p["id"] != "req-1" {
			t.Error(p)
		}
		if serviceMethods[str(p["method"])] {
			if r.Header.Get(ServiceAuthHeader) != "service-secret" || r.Header.Get(AuthHeader) != "" {
				t.Error("wrong service auth headers")
			}
		} else {
			if r.Header.Get(AuthHeader) != "user-secret" || r.Header.Get(ServiceAuthHeader) != "" {
				t.Error("wrong user auth headers")
			}
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","result":{"ok":true}}`)
	}))
	defer srv.Close()
	c, err := NewClient(" "+srv.URL+"/ ", "user-secret", "service-secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, method := range []string{"run.adapter_event", "task.start_run", "run.store_session_id", "task.add_agent_comment", "session.store_activity_log"} {
		r, err := c.CallWithID(context.Background(), method, Object{"kind": "test"}, "req-1")
		if err != nil {
			t.Fatal(err)
		}
		equalJSON(t, r, Object{"ok": true})
	}
	for _, endpoint := range []string{"", " ", "ftp://example.com", "relative"} {
		if _, err := NewClient(endpoint, "", "", 0); err == nil {
			t.Fatalf("accepted %q", endpoint)
		}
	}
	if s, err := NormalizeEndpoint(srv.URL + "/api/"); err != nil || s != srv.URL+"/api" {
		t.Fatal(s, err)
	}
}
func TestRPCFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"http", `{"error":"denied"}`, "Agentis returned HTTP 403", 403},
		{"rpc", `{"error":{"code":-32000,"message":"boom"}}`, "boom", 200},
		{"invalid", `not json`, "Agentis returned an invalid JSON-RPC response", 200},
		{"array", `[]`, "Agentis returned an invalid JSON-RPC response", 200},
		{"generic", `{"error":true}`, "Agentis returned a JSON-RPC error", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			c, _ := NewClient(srv.URL, "", "", time.Second)
			defer c.Close()
			_, err := c.Call(context.Background(), "test", nil)
			var rpcErr *RPCError
			if !errors.As(err, &rpcErr) || rpcErr.Message != tc.want || rpcErr.StatusCode != tc.status {
				t.Fatal(err)
			}
		})
	}
}
func TestRPCTimeoutAndRedirectIsolation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	c, _ := NewClient(srv.URL, "", "", 20*time.Millisecond)
	defer c.Close()
	start := time.Now()
	if _, err := c.Call(context.Background(), "test", nil); err == nil {
		t.Fatal("missing timeout")
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout too slow")
	}
	forwarded := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = true }))
	defer dest.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	c2, _ := NewClient(redirect.URL, "secret", "service", time.Second)
	defer c2.Close()
	if _, err := c2.Call(context.Background(), "session.store_activity_log", nil); err == nil || forwarded {
		t.Fatal("redirect credential isolation failed", err)
	}
}
