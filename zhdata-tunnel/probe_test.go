package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// probe: bare socks handshake against a client listener with NO session at all
func TestSocksHandshakeNoSession(t *testing.T) {
	cli, err := newClientApp(clientConfig{listen: "127.0.0.1:0", server: "127.0.0.1:1", uuid: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	c, err := socksDial(cli.Addr(), atypDomain, []byte("example.com"), 80)
	if err != nil {
		t.Fatalf("socks handshake should complete even without session: %v", err)
	}
	c.Close()
}

// probe: takeover, let churn settle, then traffic
func TestTakeoverSettle(t *testing.T) {
	const uuid = "77777777-0000-0000-0000-000000000000"
	target := newProbeTarget(t)
	host, port := splitHostPort(t, target.URL)
	srv, err := newServerApp(serverConfig{
		listen:    "127.0.0.1:0",
		usersPath: usersFileWith(t, uuid),
		backend:   "direct",
		verbose:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cli1, err := newClientApp(clientConfig{listen: "127.0.0.1:0", server: srv.Addr(), uuid: uuid, verbose: true})
	if err != nil {
		t.Fatal(err)
	}
	defer cli1.Close()
	waitFor(t, func() bool { return srv.estab.Load() >= 1 }, "cli1 session")
	cli2, err := newClientApp(clientConfig{listen: "127.0.0.1:0", server: srv.Addr(), uuid: uuid, verbose: true})
	if err != nil {
		t.Fatal(err)
	}
	defer cli2.Close()
	waitFor(t, func() bool { return srv.estab.Load() >= 2 }, "takeover")
	cli1.Close()
	time.Sleep(2 * time.Second)
	t.Logf("socks addr=%s", cli2.Addr())
	body, err := httpThroughTunnel(t, cli2.Addr(), host, port)
	if err != nil {
		t.Fatalf("traffic after settle: %v", err)
	}
	_ = body
	if !strings.Contains(body, "hello") {
		t.Fatalf("bad body %q", body)
	}
}

func newProbeTarget(t *testing.T) *httptest.Server {
	t.Helper()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))
	t.Cleanup(target.Close)
	return target
}
