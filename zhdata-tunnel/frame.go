package main

import (
	"encoding/binary"
	"errors"
	"io"
)

// zhdata record layer (zhv2):
//   len(2B BE, = 4 + payload len) | type(1B) | stream_id(2B BE) | payload
// The whole byte stream after the handshake salt is one continuous AES-CTR
// keystream; frames are parsed inside the decrypted plaintext.
// zhv2: stream ids are unique per uuid ACROSS sessions; client->server DATA
// payloads carry a 4B sequence prefix and tClose carries the 4B finSeq, so
// one app stream may be spread over several sessions (in-stream parallelism).

const (
	tOpen    = 0x01 // client->server: open stream, payload = atyp|addr|port
	tData    = 0x02
	tClose   = 0x03 // half-close (no more data from sender)
	tPing    = 0x04
	tPong    = 0x05
	tGoaway  = 0x06 // server->client: taken over by a newer connection
	tReset   = 0x07 // abort stream
	tOpenAck = 0x08 // server->client: backend dial succeeded
	tAck     = 0x09 // server->client: this session delivered N upload frames (pull credit)

	// zhv5 bucket upload: a bulk HTTP request body is split into byte-range
	// "buckets", each carried by an independent member stream attached to the
	// leader stream (which owns the backend connection). A stalled or reset
	// bucket kills only that range; the client retries/splits it while the
	// other ranges keep flowing — no whole-stream head-of-line death.
	tOpenBucketMember = 0xFE // tOpen payload: FE|leaderSID(2)|off(8)|len(8)|final(1)

	atypIPv4   = 1
	atypDomain = 3
	atypIPv6   = 4

	maxPayload = 16 * 1024
	smallFrame = 1400 // DATA frames up to this size get priority

	handshakeLen = 48     // 16B salt + 32B encrypted magic block
	magic        = "zhv5" // v0.12: bucket-upload frames (zhv4 clients still accepted by the server)
)

// bucket member tOpen payload helpers (see tOpenBucketMember).
func encodeBucketMember(leaderSID uint16, off, ln uint64, final bool) []byte {
	p := make([]byte, 20)
	p[0] = tOpenBucketMember
	binary.BigEndian.PutUint16(p[1:3], leaderSID)
	binary.BigEndian.PutUint64(p[3:11], off)
	binary.BigEndian.PutUint64(p[11:19], ln)
	if final {
		p[19] = 1
	}
	return p
}

func decodeBucketMember(p []byte) (leaderSID uint16, off, ln uint64, final bool, ok bool) {
	if len(p) != 20 || p[0] != tOpenBucketMember {
		return 0, 0, 0, false, false
	}
	return binary.BigEndian.Uint16(p[1:3]), binary.BigEndian.Uint64(p[3:11]),
		binary.BigEndian.Uint64(p[11:19]), p[19] == 1, true
}

var (
	errBadFrame      = errors.New("zhdata: bad frame header")
	errFrameTooLarge = errors.New("zhdata: frame too large")
	errGoaway        = errors.New("zhdata: goaway (taken over)")
)

type Frame struct {
	Typ     byte
	SID     uint16
	Payload []byte
}

// encodeFrame returns the plaintext frame bytes (caller XORs in place).
func encodeFrame(f *Frame) []byte {
	n := len(f.Payload)
	buf := make([]byte, 5+n)
	l := n + 4
	buf[0] = byte(l >> 8)
	buf[1] = byte(l)
	buf[2] = f.Typ
	binary.BigEndian.PutUint16(buf[3:5], f.SID)
	copy(buf[5:], f.Payload)
	return buf
}

// readFrameEnc reads one frame, decrypting incrementally with rx.
func readFrameEnc(r io.Reader, rx cipherStream) (*Frame, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	rx.XORKeyStream(hdr[:], hdr[:])
	n := int(hdr[0])<<8 | int(hdr[1])
	if n < 4 {
		return nil, errBadFrame
	}
	f := &Frame{Typ: hdr[2], SID: binary.BigEndian.Uint16(hdr[3:5])}
	if n > 4 {
		f.Payload = make([]byte, n-4)
		if _, err := io.ReadFull(r, f.Payload); err != nil {
			return nil, err
		}
		rx.XORKeyStream(f.Payload, f.Payload)
	}
	return f, nil
}

// encodeAddrPort: atyp(1) | [len(1) if domain] | addr | port(2)
// (matches the SOCKS5 address encoding, and the zhdata OPEN payload)
func encodeAddrPort(atyp byte, addr []byte, port uint16) []byte {
	n := 1 + len(addr) + 2
	if atyp == atypDomain {
		n++ // domain addresses carry a 1-byte length prefix
	}
	b := make([]byte, 0, n)
	b = append(b, atyp)
	if atyp == atypDomain {
		b = append(b, byte(len(addr)))
	}
	b = append(b, addr...)
	b = append(b, byte(port>>8), byte(port))
	return b
}

func decodeAddrPort(p []byte) (atyp byte, addr []byte, port uint16, err error) {
	if len(p) < 4 {
		err = errBadFrame
		return
	}
	atyp = p[0]
	switch atyp {
	case atypIPv4:
		if len(p) < 7 {
			err = errBadFrame
			return
		}
		addr, port = p[1:5], binary.BigEndian.Uint16(p[5:7])
	case atypIPv6:
		if len(p) < 19 {
			err = errBadFrame
			return
		}
		addr, port = p[1:17], binary.BigEndian.Uint16(p[17:19])
	case atypDomain:
		l := int(p[1])
		if len(p) < 2+l+2 {
			err = errBadFrame
			return
		}
		addr, port = p[2:2+l], binary.BigEndian.Uint16(p[2+l:4+l])
	default:
		err = errBadFrame
	}
	return
}
