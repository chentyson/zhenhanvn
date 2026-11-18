package main

import (
	"bufio"
	"bytes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDeriveKeysStable(t *testing.T) {
	salt := []byte("0123456789abcdef")
	k1, iv1 := deriveKeys("uuid-a", salt)
	k2, iv2 := deriveKeys("uuid-a", salt)
	if !bytes.Equal(k1, k2) || !bytes.Equal(iv1, iv2) {
		t.Fatal("deriveKeys not deterministic")
	}
	if len(k1) != 32 || len(iv1) != 16 {
		t.Fatal("bad key/iv length")
	}
	k3, _ := deriveKeys("uuid-b", salt)
	if bytes.Equal(k1, k3) {
		t.Fatal("different uuid must give different key")
	}
}

func TestFrameRoundTrip(t *testing.T) {
	f := &Frame{Typ: tData, SID: 0x1234, Payload: []byte("hello zhdata")}
	buf := encodeFrame(f)
	ident := func(b []byte) {}
	_ = ident
	rx := cipher.Stream(nopStream{})
	got, err := readFrameEnc(bytes.NewReader(buf), rx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Typ != f.Typ || got.SID != f.SID || string(got.Payload) != string(f.Payload) {
		t.Fatalf("frame mismatch: %+v", got)
	}
}

type nopStream struct{}

func (nopStream) XORKeyStream(dst, src []byte) { copy(dst, src) }

func TestLoadUsers(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "u1.txt")
	os.WriteFile(p1, []byte("# comment\nAAA-BBB\n\nccc\n"), 0o644)
	u, err := loadUsers(p1)
	if err != nil || len(u) != 2 || u[0] != "aaa-bbb" {
		t.Fatalf("plain users: %v %v", u, err)
	}
	p2 := filepath.Join(dir, "u2.json")
	os.WriteFile(p2, []byte(`{"users":[{"uuid":"U1","name":"a"},{"uuid":"u2","name":"b"}]}`), 0o644)
	u, err = loadUsers(p2)
	if err != nil || len(u) != 2 || u[1] != "u2" {
		t.Fatalf("json users: %v %v", u, err)
	}
}

// ---- helpers ----

func usersFileWith(t *testing.T, uuids ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "users.json")
	var b strings.Builder
	b.WriteString(`{"users":[`)
	for i, u := range uuids {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"uuid":"` + u + `"}`)
	}
	b.WriteString("]}")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timeout: " + msg)
}

// httpThroughTunnel performs a plain HTTP GET via the client socks listener.
func httpThroughTunnel(t *testing.T, socksAddr, targetHost string, targetPort uint16) (string, error) {
	t.Helper()
	c, err := socksDial(socksAddr, atypDomain, []byte(targetHost), targetPort)
	if err != nil {
		return "", err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(8 * time.Second))
	if _, err = c.Write([]byte("GET / HTTP/1.0\r\n\r\n")); err != nil {
		return "", err
	}
	b, err := io.ReadAll(c)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func TestEndToEnd(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("pong-body"))
	}))
	defer target.Close()
	host, port := splitHostPort(t, target.URL)

	srv, err := newServerApp(serverConfig{
		listen:    "127.0.0.1:0",
		usersPath: usersFileWith(t, uuid),
		backend:   "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	cli, err := newClientApp(clientConfig{
		listen: "127.0.0.1:0",
		server: srv.Addr(),
		uuid:   uuid,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	waitFor(t, func() bool { return srv.estab.Load() >= 1 }, "session established")

	body, err := httpThroughTunnel(t, cli.Addr(), host, port)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "pong-body") {
		t.Fatalf("unexpected response: %q", body)
	}
}

func TestWrongUUIDRejected(t *testing.T) {
	const good = "aaaaaaaa-0000-0000-0000-000000000000"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("should-not-see"))
	}))
	defer target.Close()
	host, port := splitHostPort(t, target.URL)

	srv, err := newServerApp(serverConfig{
		listen:    "127.0.0.1:0",
		usersPath: usersFileWith(t, good),
		backend:   "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	cli, err := newClientApp(clientConfig{
		listen: "127.0.0.1:0",
		server: srv.Addr(),
		uuid:   "deadbeef-0000-0000-0000-000000000000",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	time.Sleep(500 * time.Millisecond)
	if srv.estab.Load() != 0 {
		t.Fatal("wrong uuid should not establish session")
	}
	body, err := httpThroughTunnel(t, cli.Addr(), host, port)
	if err == nil && strings.Contains(body, "should-not-see") {
		t.Fatal("traffic unexpectedly succeeded with wrong uuid")
	}
}

func TestTakeover(t *testing.T) {
	const uuid = "99999999-0000-0000-0000-000000000000"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("takeover-ok"))
	}))
	defer target.Close()
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

	// cli1 receives GOAWAY (takeover happened) and stops pumping data
	// through the old session: at most one live session per uuid.
	waitFor(t, func() bool { return srv.estab.Load() >= 2 }, "takeover churn")
	// simulate roaming: the old client stops, the new one keeps the uuid
	cli1.Close()
	t.Logf("cli1 closed; cli2 socks addr=%s", cli2.Addr())
	waitFor(t, func() bool {
		srv.mu.Lock()
		n := len(srv.conns)
		srv.mu.Unlock()
		return n == 1
	}, "exactly one session after old client stopped")

	body, err := httpThroughTunnel(t, cli2.Addr(), host, port)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "takeover-ok") {
		t.Fatalf("unexpected response: %q", body)
	}
	srv.mu.Lock()
	n := len(srv.conns)
	act := srv.active
	srv.mu.Unlock()
	if n != 1 || act != 1 {
		t.Fatalf("expected exactly 1 session/active, got %d/%d", n, act)
	}
}

func TestKeyCaseInsensitive(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("case-ok"))
	}))
	defer target.Close()
	host, port := splitHostPort(t, target.URL)

	srv, err := newServerApp(serverConfig{
		listen:    "127.0.0.1:0",
		usersPath: usersFileWith(t, "MixedCase-Iammaster1125"),
		backend:   "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cli, err := newClientApp(clientConfig{
		listen: "127.0.0.1:0",
		server: srv.Addr(),
		uuid:   "mixedcase-iammaster1125",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	waitFor(t, func() bool { return srv.estab.Load() >= 1 }, "mixed-case session")
	body, err := httpThroughTunnel(t, cli.Addr(), host, port)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "case-ok") {
		t.Fatalf("bad body %q", body)
	}
}

func TestBulkDownloadOrdering(t *testing.T) {
	// Bulk body, handler returns immediately after writing: CLOSE must not
	// overtake buffered DATA (regression: tClose via ctrlQ truncated streams).
	body := make([]byte, 3<<20)
	for i := range body {
		body[i] = byte(i*31 + i>>8)
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer target.Close()
	host, port := splitHostPort(t, target.URL)

	srv, err := newServerApp(serverConfig{
		listen:    "127.0.0.1:0",
		usersPath: usersFileWith(t, "bulk-user"),
		backend:   "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cli, err := newClientApp(clientConfig{
		listen: "127.0.0.1:0",
		server: srv.Addr(),
		uuid:   "bulk-user",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	waitFor(t, func() bool { return srv.estab.Load() >= 1 }, "bulk session")

	got, err := httpThroughTunnel(t, cli.Addr(), host, port)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < len(body) {
		t.Fatalf("short read: %d < %d bytes", len(got), len(body))
	}
	mid := string(body[len(body)/2 : len(body)/2+128])
	tail := string(body[len(body)-128:])
	if !strings.Contains(got, mid) || !strings.Contains(got, tail) {
		t.Fatalf("body corrupted/truncated: got %d bytes", len(got))
	}
}

func TestMultiConns(t *testing.T) {
	// client -conns 3: three independent sessions (slots 0-2), no mutual
	// takeover; streams spread and all succeed.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("multi-body"))
	}))
	defer target.Close()
	host, port := splitHostPort(t, target.URL)

	srv, err := newServerApp(serverConfig{
		listen:    "127.0.0.1:0",
		usersPath: usersFileWith(t, "multi-conn-user"),
		backend:   "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cli, err := newClientApp(clientConfig{
		listen: "127.0.0.1:0",
		server: srv.Addr(),
		uuid:   "multi-conn-user",
		conns:  3,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	waitFor(t, func() bool { return srv.estab.Load() >= 3 }, "three slot sessions")
	// no takeover war: all 3 slots stay established simultaneously
	time.Sleep(500 * time.Millisecond)
	srv.mu.Lock()
	live := len(srv.conns)
	srv.mu.Unlock()
	if live != 3 {
		t.Fatalf("expected 3 coexisting sessions, got %d", live)
	}
	for i := 0; i < 4; i++ {
		got, err := httpThroughTunnel(t, cli.Addr(), host, port)
		if err != nil {
			t.Fatalf("req %d: %v", i, err)
		}
		if !strings.Contains(got, "multi-body") {
			t.Fatalf("req %d: bad body %q", i, got)
		}
	}
	errc := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			got, err := httpThroughTunnel(t, cli.Addr(), host, port)
			if err == nil && !strings.Contains(got, "multi-body") {
				err = fmt.Errorf("bad body")
			}
			errc <- err
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-errc; err != nil {
			t.Fatalf("concurrent req %d: %v", i, err)
		}
	}
}

func TestUploadSlowBackendNoHoL(t *testing.T) {
	// One stream uploads to a backend that stalls after 1 byte; another
	// stream's request must still complete quickly (dispatch must not block
	// on a slow backend Write).
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.CopyN(io.Discard, r.Body, 1)
		time.Sleep(15 * time.Second)
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fast-ok"))
	}))
	defer fast.Close()
	slowHost, slowPort := splitHostPort(t, slow.URL)
	fastHost, fastPort := splitHostPort(t, fast.URL)

	srv, err := newServerApp(serverConfig{
		listen:    "127.0.0.1:0",
		usersPath: usersFileWith(t, "hol-user"),
		backend:   "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cli, err := newClientApp(clientConfig{
		listen: "127.0.0.1:0",
		server: srv.Addr(),
		uuid:   "hol-user",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	waitFor(t, func() bool { return srv.estab.Load() >= 1 }, "hol session")

	// flood a slow upload (32MB body, backend stops consuming after 1B)
	go func() {
		up, err := socksDial(cli.Addr(), atypDomain, []byte(slowHost), slowPort)
		if err != nil {
			return
		}
		defer up.Close()
		up.Write([]byte("POST /x HTTP/1.1\r\nHost: x\r\nContent-Length: 33554432\r\n\r\n"))
		chunk := make([]byte, 16384)
		for i := 0; i < 2048; i++ {
			if _, err := up.Write(chunk); err != nil {
				return
			}
		}
	}()
	time.Sleep(400 * time.Millisecond) // let the stall begin

	start := time.Now()
	got, err := httpThroughTunnel(t, cli.Addr(), fastHost, fastPort)
	if err != nil {
		t.Fatalf("fast req failed while slow upload in flight: %v", err)
	}
	if !strings.Contains(got, "fast-ok") {
		t.Fatalf("bad body %q", got)
	}
	if el := time.Since(start); el > 6*time.Second {
		t.Fatalf("head-of-line blocking: fast request took %v behind stalled upload", el)
	}
}

func TestMultiPortListen(t *testing.T) {
	// server listens on two ports; client slots dial port i for slot i
	// (per-port shaping buckets -> independent throughput per slot).
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("mp-ok"))
	}))
	defer target.Close()
	host, port := splitHostPort(t, target.URL)

	srv, err := newServerApp(serverConfig{
		listen:    "127.0.0.1:0,127.0.0.1:0",
		usersPath: usersFileWith(t, "mp-user"),
		backend:   "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if len(srv.Addrs()) != 2 {
		t.Fatalf("expected 2 listeners, got %v", srv.Addrs())
	}
	cli, err := newClientApp(clientConfig{
		listen: "127.0.0.1:0",
		server: strings.Join(srv.Addrs(), ","),
		uuid:   "mp-user",
		conns:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	waitFor(t, func() bool { return srv.estab.Load() >= 2 }, "two slot sessions on two ports")
	got, err := httpThroughTunnel(t, cli.Addr(), host, port)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "mp-ok") {
		t.Fatalf("bad body %q", got)
	}
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestSessionDataFIFO(t *testing.T) {
	// A DATA frame must never jump ahead of an earlier DATA frame on the
	// same session, even when a slow link keeps both queued: queue-choice
	// by frame size (hiQ for small) reorders mixed-size streams (the
	// speedtest-upload corruption bug).
	ca, cb := net.Pipe()
	uuid := "fifo-user"
	salt := randBytes(16)
	sa := newSession("a", ca, newCTR(uuid, salt), newCTR(uuid, salt))
	defer sa.close()
	go sa.run(func(*Frame) {})

	sa.sendData(Frame{Typ: tData, SID: 1, Payload: bytes.Repeat([]byte{0xAA}, 16384)})
	sa.sendData(Frame{Typ: tData, SID: 2, Payload: bytes.Repeat([]byte{0xCC}, 16384)})
	sa.sendData(Frame{Typ: tData, SID: 3, Payload: bytes.Repeat([]byte{0xBB}, 100)})

	rx := newCTR(uuid, salt)
	got := []uint16{}
	for i := 0; i < 3; i++ {
		time.Sleep(120 * time.Millisecond) // keep sender's queues backlogged
		f, err := readFrameEnc(cb, rx)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		got = append(got, f.SID)
	}
	if got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("DATA frames delivered out of order: %v (want [1 2 3])", got)
	}
}

func TestUploadMixedChunkOrder(t *testing.T) {
	// Interactive-priority (hiQ) must never reorder DATA within a stream:
	// writes arrive as mixed small (TLS-record-sized) and large chunks;
	// the backend must observe byte-exact stream order.
	const total = 4 << 20 // 4MB
	want := make([]byte, 0, total)
	var parts [][]byte
	off := 0
	for off < total {
		n := 1300
		if (off/1300)%3 == 0 {
			n = 16384
		}
		if off+n > total {
			n = total - off
		}
		chunk := make([]byte, n)
		for i := range chunk {
			chunk[i] = byte((off+i)*131 + (off+i)>>8)
		}
		want = append(want, chunk...)
		parts = append(parts, chunk)
		off += n
	}
	sum := sha256Hex(want)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.Write([]byte("read-err"))
			return
		}
		w.Write([]byte(sha256Hex(body) + ":" + strconv.Itoa(len(body))))
	}))
	defer target.Close()
	host, port := splitHostPort(t, target.URL)

	srv, err := newServerApp(serverConfig{
		listen:    "127.0.0.1:0",
		usersPath: usersFileWith(t, "mix-order-user"),
		backend:   "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cli, err := newClientApp(clientConfig{
		listen: "127.0.0.1:0",
		server: srv.Addr(),
		uuid:   "mix-order-user",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	waitFor(t, func() bool { return srv.estab.Load() >= 1 }, "mix session")

	c, err := socksDial(cli.Addr(), atypDomain, []byte(host), port)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	fmt.Fprintf(c, "POST /h HTTP/1.0\r\nContent-Length: %d\r\n\r\n", total)
	for _, p := range parts {
		if _, err := c.Write(p); err != nil {
			t.Fatalf("write: %v", err)
		}
		time.Sleep(10 * time.Millisecond) // force separate reads per chunk
	}
	resp, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read resp: %v", err)
	}
	got := string(resp)
	if !strings.Contains(got, sum) || !strings.Contains(got, ":"+strconv.Itoa(total)) {
		t.Fatalf("stream order corrupted: want sha %s len %d, target saw %q", sum, total, got)
	}
}

func TestParallelBulkUpload(t *testing.T) {
	// speedtest-like: 5 concurrent POST floods to a fast target; every
	// stream must complete with the body fully and correctly received.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.Write([]byte("read-err"))
			return
		}
		w.Write([]byte(sha256Hex(body) + ":" + strconv.Itoa(len(body))))
	}))
	defer target.Close()
	host, port := splitHostPort(t, target.URL)

	srv, err := newServerApp(serverConfig{
		listen:    "127.0.0.1:0",
		usersPath: usersFileWith(t, "par-up-user"),
		backend:   "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cli, err := newClientApp(clientConfig{
		listen: "127.0.0.1:0",
		server: srv.Addr(),
		uuid:   "par-up-user",
		conns:  3,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	waitFor(t, func() bool { return srv.estab.Load() >= 3 }, "3 sessions")

	const chunk = 16384
	const nchunks = 1200 // ~19.6MB per stream
	seed := make([]byte, chunk)
	for i := range seed {
		seed[i] = byte(i * 7)
	}
	var wg sync.WaitGroup
	for u := 0; u < 5; u++ {
		wg.Add(1)
		go func(u int) {
			defer wg.Done()
			c, err := socksDial(cli.Addr(), atypDomain, []byte(host), port)
			if err != nil {
				t.Errorf("up %d: dial: %v", u, err)
				return
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(60 * time.Second))
			const n = chunk * nchunks
			fmt.Fprintf(c, "POST /h HTTP/1.0\r\nContent-Length: %d\r\n\r\n", n)
			for i := 0; i < nchunks; i++ {
				if _, err := c.Write(seed); err != nil {
					t.Errorf("up %d: write %d: %v", u, i, err)
					return
				}
			}
			resp, err := io.ReadAll(c)
			if err != nil {
				t.Errorf("up %d: read resp: %v", u, err)
				return
			}
			want := sha256Hex(bytes.Repeat(seed, nchunks))
			if !strings.Contains(string(resp), want+":"+strconv.Itoa(n)) {
				t.Errorf("up %d: body corrupt: got %q", u, string(resp))
			}
		}(u)
	}
	wg.Wait()
}

func TestSpreadRebalanceFairness(t *testing.T) {
	// Whitebox early-hogging regression: one stream hogs every lane while it
	// is the only bulk stream; when latecomers arrive, rebalanceLocked must
	// trim it toward the fair share (detaching only fully-acked lanes) and
	// keep attached/lanes/members bookkeeping consistent. (The end-to-end
	// variant was nondeterministic on loopback: streams finish before the
	// waiter can observe the mid-flight transient.)
	a, err := newClientApp(clientConfig{
		listen: "127.0.0.1:0",
		server: "127.0.0.1:1", // never dials successfully; sessions stay nil
		uuid:   "rebal-user",
		conns:  8,
		spread: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	a.mu.Lock()
	s := &cStream{
		app:  a,
		id:   1,
		sid:  1,
		bulk: true, // rebalanceLocked only trims bulk streams
	}
	s.home = a.conns[0]
	s.conns = append(s.conns, a.conns...) // home + 7 attached: hogging the pool
	s.attached = len(s.conns) - 1
	for _, cn := range s.conns {
		cn.members[s.id] = s
		s.ensureLaneLocked(cn)
		s.laneBytes[cn] = 0 // fully delivered: detachable
	}
	a.streams[s.id] = s
	a.bulk = 1
	a.lanes = s.attached
	a.mu.Unlock()

	a.mu.Lock()
	a.bulk = 4 // three latecomers became bulk
	a.rebalanceLocked(2)
	leftover := len(s.conns)
	lanes := a.lanes
	members := 0
	for _, cn := range a.conns {
		if _, ok := cn.members[s.id]; ok {
			members++
		}
	}
	a.mu.Unlock()

	if leftover > 2 {
		t.Fatalf("hog not trimmed: conns=%d want <=2", leftover)
	}
	if lanes != leftover-1 {
		t.Fatalf("lane accounting drift: lanes=%d attached=%d", lanes, leftover-1)
	}
	if members != leftover {
		t.Fatalf("member bookkeeping drift: members=%d conns=%d", members, leftover)
	}
}

func TestProbeGetRejected(t *testing.T) {
	const uuid = "88888888-0000-0000-0000-000000000000"
	srv, err := newServerApp(serverConfig{
		listen:    "127.0.0.1:0",
		usersPath: usersFileWith(t, uuid),
		backend:   "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	c, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte("GET / HTTP/1.1\r\nHost: probe\r\n\r\n"))
	b, err := bufio.NewReader(c).Peek(1)
	if err == nil && len(b) > 0 {
		t.Fatalf("probe got data back: %q", b)
	}
}

func splitHostPort(t *testing.T, url string) (string, uint16) {
	t.Helper()
	u := strings.TrimPrefix(url, "http://")
	host, ps, err := net.SplitHostPort(u)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.ParseUint(ps, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	return host, uint16(p)
}

// TestRetransmitSurvivesSessionKill is the end-to-end proof of route A: an
// 8MB upload is spread over two lanes; one lane's TCP session is killed
// mid-flight. The frames in flight on it are lost, but the client retransmits
// them on the surviving lane and the target must receive the exact body.
// Without retransmission this test fails (hole -> reset / truncated body).
func TestRetransmitSurvivesSessionKill(t *testing.T) {
	// This guards the LEGACY per-stream send-window / retransmit path that
	// still carries small and interactive uploads (Content-Length below the
	// bucket threshold, so the body is NOT bucketed). Keep the body under
	// 2*bucketMin so peekHTTPCL leaves this stream on the legacy spread path.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.Write([]byte("read-err"))
			return
		}
		w.Write([]byte(sha256Hex(body) + ":" + strconv.Itoa(len(body))))
	}))
	defer target.Close()
	host, port := splitHostPort(t, target.URL)

	const uuid = "rtx-user"
	srv, err := newServerApp(serverConfig{
		listen:    "127.0.0.1:0",
		usersPath: usersFileWith(t, uuid),
		backend:   "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cli, err := newClientApp(clientConfig{
		listen: "127.0.0.1:0",
		server: srv.Addr(),
		uuid:   uuid,
		conns:  2,
		spread: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	waitFor(t, func() bool { return srv.estab.Load() >= 2 }, "2 sessions")

	const chunk = 2048
	const nchunks = 460 // ~920KB: below 2*bucketMin (legacy path); paced ~460ms so a spread lane is reliably observed
	seed := make([]byte, chunk)
	for i := range seed {
		seed[i] = byte(i*13 + 7)
	}
	sum := sha256Hex(bytes.Repeat(seed, nchunks))

	c, err := socksDial(cli.Addr(), atypDomain, []byte(host), port)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(60 * time.Second))
	fmt.Fprintf(c, "POST /h HTTP/1.0\r\nContent-Length: %d\r\n\r\n", chunk*nchunks)

	writeErr := make(chan error, 1)
	go func() {
		for i := 0; i < nchunks; i++ {
			if _, err := c.Write(seed); err != nil {
				writeErr <- err
				return
			}
			time.Sleep(time.Millisecond) // pace so frames are in flight when the lane dies
		}
		writeErr <- nil
	}()

	killed := false
	// Kill a non-owner SPREAD lane of the single legacy stream mid-upload:
	// the frames in that lane's TCP are lost; the client must retransmit them
	// on the surviving lane (per-stream send window) and the body must arrive
	// byte-exact. grpKey=="" ensures we target the legacy (non-bucketed) path.
	waitFor(t, func() bool {
		srv.stmMu.Lock()
		defer srv.stmMu.Unlock()
		for _, st := range srv.streams {
			if st.grpKey != "" || st.owner == nil || len(st.members) < 2 {
				continue // legacy stream spread over >=2 lanes
			}
			for sh := range st.members {
				if sh == st.owner {
					continue // kill a non-owner spread lane, not the home
				}
				st.mu.Lock()
				n := uint32(0)
				if la := st.laneHi[sh]; la != nil {
					n = la.count
				}
				st.mu.Unlock()
				if n < 8 {
					continue
				}
				sh.sess.close() // the spread lane's TCP dies mid-upload
				killed = true
				return true
			}
		}
		return false
	}, "a spread lane to receive frames before being killed")
	if !killed {
		t.Fatal("no lane was killed")
	}

	if err := <-writeErr; err != nil {
		t.Fatalf("upload write failed: %v", err)
	}
	resp, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read resp: %v", err)
	}
	if !strings.Contains(string(resp), sum) {
		t.Fatalf("body corrupted after member-lane kill: target saw %q, want sha %s", string(resp), sum)
	}
}
