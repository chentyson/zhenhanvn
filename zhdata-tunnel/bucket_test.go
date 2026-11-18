package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestBucketUploadE2E: 4MB PUT through the tunnel must arrive byte-exact via
// bucket members; headers on the leader, ranges in offset order.
func TestBucketUploadE2E(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.Write([]byte("read-err"))
			return
		}
		h := sha256.Sum256(body)
		w.Write([]byte(hex.EncodeToString(h[:]) + ":" + strconv.Itoa(len(body))))
	}))
	defer target.Close()
	host, port := splitHostPort(t, target.URL)

	srv, err := newServerApp(serverConfig{
		listen:    "127.0.0.1:0",
		usersPath: usersFileWith(t, "bkt-user"),
		backend:   "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cli, err := newClientApp(clientConfig{
		listen:  "127.0.0.1:0",
		server:  srv.Addr(),
		uuid:    "bkt-user",
		conns:   3,
		verbose: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	waitFor(t, func() bool { return srv.estab.Load() >= 3 }, "3 sessions")

	const total = 4 << 20
	body := make([]byte, total)
	for i := range body {
		body[i] = byte(i*31 + i>>8)
	}
	want := sha256.Sum256(body)

	c, err := socksDial(cli.Addr(), atypDomain, []byte(host), port)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	fmt.Fprintf(c, "PUT /up HTTP/1.0\r\nContent-Length: %d\r\n\r\n", total)
	if _, err := c.Write(body); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read resp: %v", err)
	}
	// Response is a full HTTP response; compare only the body after headers.
	got := string(resp)
	if idx := strings.Index(got, "\r\n\r\n"); idx >= 0 {
		got = got[idx+4:]
	}
	if wantSum := hex.EncodeToString(want[:]) + ":" + strconv.Itoa(total); got != wantSum {
		t.Fatalf("bucket upload mismatch:\n got %q\nwant %q", got, wantSum)
	}
}

// TestBucketUploadSingleLaneSlow: one session (no split pools), body written
// in small chunks paced 10ms apart — the shape that exercises the leader-read
// loop accumulating a full bucket before launching any member.
func TestBucketUploadSingleLaneSlow(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h := sha256.Sum256(body)
		w.Write([]byte(hex.EncodeToString(h[:]) + ":" + strconv.Itoa(len(body))))
	}))
	defer target.Close()
	host, port := splitHostPort(t, target.URL)

	srv, err := newServerApp(serverConfig{
		listen:    "127.0.0.1:0",
		usersPath: usersFileWith(t, "bkt1-user"),
		backend:   "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cli, err := newClientApp(clientConfig{
		listen:  "127.0.0.1:0",
		server:  srv.Addr(),
		uuid:    "bkt1-user",
		conns:   1,
		verbose: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	waitFor(t, func() bool { return srv.estab.Load() >= 1 }, "1 session")

	const chunk = 1300
	const nchunks = 1600 // ~2MB
	seed := make([]byte, chunk)
	for i := range seed {
		seed[i] = byte(i*131 + i>>8)
	}
	want := sha256.Sum256(bytes.Repeat(seed, nchunks))

	c, err := socksDial(cli.Addr(), atypDomain, []byte(host), port)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(60 * time.Second))
	fmt.Fprintf(c, "PUT /up HTTP/1.0\r\nContent-Length: %d\r\n\r\n", chunk*nchunks)
	for i := 0; i < nchunks; i++ {
		if _, err := c.Write(seed); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	resp, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read resp: %v", err)
	}
	got := string(resp)
	if idx := strings.Index(got, "\r\n\r\n"); idx >= 0 {
		got = got[idx+4:]
	}
	wantSum := hex.EncodeToString(want[:]) + ":" + strconv.Itoa(chunk*nchunks)
	if got != wantSum {
		t.Fatalf("single-lane bucket mismatch:\n got %q\nwant %q", got, wantSum)
	}
}
