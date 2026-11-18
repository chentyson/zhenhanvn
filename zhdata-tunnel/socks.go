package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"
)

var dbgOn = os.Getenv("ZHD_DEBUG") != ""

func dbg(format string, v ...any) {
	if dbgOn {
		log.Printf("%s "+format, append([]any{time.Now().Format("15:04:05.000")}, v...)...)
	}
}

// socksAccept performs a SOCKS5 CONNECT handshake as the server side
// (used by the client tunnel's local listener).
func socksAccept(c net.Conn) (atyp byte, addr []byte, port uint16, err error) {
	dbg("socksAccept:enter")
	var b [2]byte
	if _, err = io.ReadFull(c, b[:]); err != nil {
		return
	}
	dbg("socksAccept:greeting b=%v", b)
	if b[0] != 5 {
		err = fmt.Errorf("socks: bad version %d", b[0])
		return
	}
	if b[1] > 0 {
		methods := make([]byte, b[1])
		if _, err = io.ReadFull(c, methods); err != nil {
			return
		}
	}
	if _, err = c.Write([]byte{5, 0}); err != nil {
		return
	}
	dbg("socksAccept:reply-sent")
	var h [4]byte
	if _, err = io.ReadFull(c, h[:]); err != nil {
		return
	}
	dbg("socksAccept:request h=%v", h)
	if h[0] != 5 || h[1] != 1 {
		err = fmt.Errorf("socks: unsupported command ver=%d cmd=%d", h[0], h[1])
		return
	}
	atyp = h[3]
	switch atyp {
	case atypIPv4:
		addr = make([]byte, 4)
	case atypIPv6:
		addr = make([]byte, 16)
	case atypDomain:
		var l [1]byte
		if _, err = io.ReadFull(c, l[:]); err != nil {
			return
		}
		addr = make([]byte, int(l[0]))
	default:
		err = fmt.Errorf("socks: bad atyp %d", atyp)
		return
	}
	if _, err = io.ReadFull(c, addr); err != nil {
		return
	}
	var pb [2]byte
	if _, err = io.ReadFull(c, pb[:]); err != nil {
		return
	}
	port = uint16(pb[0])<<8 | uint16(pb[1])
	if _, err = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	dbg("socksAccept:done atyp=%d port=%d", atyp, port)
	return
}

// socksDial connects to a backend through a SOCKS5 proxy (server side -> sing-box).
func socksDial(backend string, atyp byte, addr []byte, port uint16) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", backend, backendDialTimeout)
	if err != nil {
		return nil, err
	}
	if err = c.SetDeadline(deadline(backendDialTimeout)); err != nil {
		c.Close()
		return nil, err
	}
	if _, err = c.Write([]byte{5, 1, 0}); err != nil {
		c.Close()
		return nil, err
	}
	dbg("socksDial:greeting-sent")
	var b [2]byte
	if _, err = io.ReadFull(c, b[:]); err != nil {
		c.Close()
		return nil, err
	}
	dbg("socksDial:reply b=%v", b)
	if b[0] != 5 || b[1] != 0 {
		c.Close()
		return nil, fmt.Errorf("socks: backend requires auth (rep=%d)", b[1])
	}
	req := []byte{5, 1, 0}
	req = append(req, encodeAddrPort(atyp, addr, port)...)
	if _, err = c.Write(req); err != nil {
		c.Close()
		return nil, err
	}
	dbg("socksDial:request-sent")
	var h [4]byte
	if _, err = io.ReadFull(c, h[:]); err != nil {
		c.Close()
		return nil, err
	}
	dbg("socksDial:connreply h=%v", h)
	if h[1] != 0 {
		c.Close()
		return nil, fmt.Errorf("socks: backend connect failed rep=%d", h[1])
	}
	switch h[3] {
	case atypIPv4:
		_, err = io.ReadFull(c, make([]byte, 6))
	case atypIPv6:
		_, err = io.ReadFull(c, make([]byte, 20))
	case atypDomain:
		var l [1]byte
		if _, err = io.ReadFull(c, l[:]); err == nil {
			_, err = io.ReadFull(c, make([]byte, int(l[0])+2))
		}
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	c.SetDeadline(noDeadline())
	return c, nil
}
