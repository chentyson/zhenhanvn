package main

import (
	"io"
	"net"
	"testing"
	"time"
)

func ts() string { return time.Now().Format("15:04:05.000") }

func TestSocksRawPair(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.SetDeadline(time.Now().Add(10 * time.Second))
		atyp, addr, port, err := socksAccept(c)
		if err != nil {
			t.Logf("raw socksAccept: %v", err)
			c.Close()
			return
		}
		t.Logf("raw socksAccept ok: atyp=%d addr=%s port=%d", atyp, addr, port)
		time.Sleep(2 * time.Second)
		c.Close()
	}()
	c, err := socksDial(ln.Addr().String(), atypDomain, []byte("example.com"), 80)
	if err != nil {
		t.Fatalf("raw socksDial: %v", err)
	}
	c.Close()
}

// mimic socksAccept step by step with timestamps, and socksDial too.
func TestSocksPairInstrumented(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		x, err := ln.Accept()
		if err != nil {
			t.Logf("accept err: %v", err)
			return
		}
		t.Logf("%s S: accepted", ts())
		b := make([]byte, 2)
		n, err := io.ReadFull(x, b)
		t.Logf("%s S: read2 n=%d err=%v b=%v", ts(), n, err, b)
		if err != nil {
			return
		}
		m := make([]byte, int(b[1]))
		if len(m) > 0 {
			n, err = io.ReadFull(x, m)
			t.Logf("%s S: readMethods n=%d err=%v m=%v", ts(), n, err, m)
		}
		_, err = x.Write([]byte{5, 0})
		t.Logf("%s S: reply write err=%v", ts(), err)
		time.Sleep(2 * time.Second)
		x.Close()
	}()

	c, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s C: dialed", ts())
	_, err = c.Write([]byte{5, 1, 0})
	t.Logf("%s C: greeting write err=%v", ts(), err)
	c.SetDeadline(time.Now().Add(5 * time.Second))
	r := make([]byte, 2)
	n, err := io.ReadFull(c, r)
	t.Logf("%s C: read reply n=%d err=%v r=%v", ts(), n, err, r)
	c.Close()
	<-done
	if err != nil {
		t.Fatalf("instrumented socks pair stalled: %v", err)
	}
}
