package main

import (
	"crypto/cipher"
	"log"
	"net"
	"sync"
	"time"
)

type cipherStream = cipher.Stream

const (
	pingInterval       = 30 * time.Second
	idleTimeout        = 90 * time.Second
	writeTimeout       = 30 * time.Second
	handshakeTimeout   = 10 * time.Second
	backendDialTimeout = 10 * time.Second
	backendWriteTO     = 30 * time.Second
	streamStallTO      = 3 * time.Second // one upq enqueue blocked this long = dead backend
	localWriteTO       = 30 * time.Second
	maxSlots           = 64 // client-side parallel session slots (handshake byte)
	maxStreams         = 1024
	maxReorderBytes    = 16 * 1024 * 1024 // v0.8.1: per-stream reassembly window. 8MB parked constantly on desynced lines (harsh-hour TCP shaping skews lanes -> silent park storms -> tunnel hits 0 while raw keeps ~5M); 16MB absorbs skew; bound = active bulk streams x 16MB.
	gapSkip            = 4096             // tolerate lost frames after owner-session death
	holeTimeout        = 12 * time.Second // v0.11: reset a stream whose reorder hole is not filled by retransmission
	// v0.11.4: per-stream DOWNLINK window (server -> client). A LAN-speed backend
	// (fsrv, CDN) hands 50MB in <1s; without a window the server dumps it into a
	// 200ms WAN TCP (16MB loQ + kernel buffers) -> bufferbloat -> loss -> RTO
	// backoff freezes (observed 15s stall + session write-timeout kills).
	// v0.11.6: 1MB starves BBR at 200ms RTT — BBR's inflight target is roughly
	// bandwidth*RTT (5MB+ at 40Mbps) and its periodic bandwidth probe briefly
	// halves delivery; a 1MB app window drains the socket, BBR reads the app
	// limit and collapses (tun dl 1.38MB/s vs raw 5.24 same window, 15:24).
	// BBR paces on its own; the window only bounds bufferbloat, so use 8MB.
	downWindowBytes = 8 << 20
	spreadThreshold = 128 * 1024 // client: attach one more session per N bytes sent
	// v0.11 (route A): uploads use a byte-based sliding window over the server's
	// *delivered* sequence (tAck carries rx.next-1), and a per-stream retransmit
	// buffer. The old per-lane frame-count credit is gone: it could not express
	// "delivered", so a stalled lane left an unfillable hole that only the 16MB
	// reorder window bounded. Now:
	//   - a frame leaves the client window only when the server has it in order
	//     (deliveredSeq), so the reorder buffer is bounded by streamWindowBytes;
	//   - the front (hole) frame is fast-retransmitted after holeResendTO and
	//     frames queued on a dead session are re-sent over the surviving lanes
	//     (the server dedupes by seq). A live-but-slow lane is NOT migrated;
	//   - spread is safe again, so few-connection apps can still fill many
	//     per-flow shaping buckets.
	streamWindowBytes = 6 << 20                // per-stream unacked ceiling; v0.11.5: 2MB pinned at 200ms RTT (12-14Mbps/stream needs ~2.5MB); 6MB still < maxReorderBytes and global 64MB bounds totals
	globalWindowBytes = 64 << 20               // client-wide unacked ceiling across all upload streams
	holeResendTO      = 400 * time.Millisecond // front frame still undelivered -> fast-retransmit it (a hole blocks ALL delivery)
	holeResendBurst   = 8                      // frames resent per fast hole-retransmit sweep (v0.11.11: 16->8, halve duplicate noise for mid-size streams)
	laneRateInit      = 50.0                   // frames/s seed for an unmeasured lane (~6.5Mbps at 16KB frames)
	laneRateMin       = 0.5                    // floor used for completion-time scoring
	ackEvery          = 8                      // tAck at least every N frames per lane
	ackMinInterval    = 10 * time.Millisecond  // ...or sooner when delivered progress needs to be reported
	trainEvery        = 2 * time.Second        // v0.8 (EXPERIMENT, off by default): per-session training padding
	trainPayload      = 16 * 1024              // v0.8: frame size
	trainFrames       = 1                      // v0.8: frames per session per tick
)

func deadline(d time.Duration) time.Time { return time.Now().Add(d) }
func noDeadline() time.Time              { return time.Time{} }

// session is one zhdata connection (TCP + continuous AES-CTR keystream).
// Frames are written through three priority queues: ctrl > small DATA > bulk DATA.
type session struct {
	name      string
	conn      net.Conn
	tx        cipherStream // write direction (independent counter state)
	rx        cipherStream // read direction (independent counter state)
	ctrlQ     chan Frame
	hiQ       chan Frame
	loQ       chan Frame
	done      chan struct{}
	closeOnce sync.Once
	writerEnd chan struct{}
	// onData, when set, is called by writer() after a tData frame has been
	// written to the socket (server: downlink window credit).
	onData func(f Frame)
}

func newSession(name string, c net.Conn, tx, rx cipherStream) *session {
	return &session{
		name:      name,
		conn:      c,
		tx:        tx,
		rx:        rx,
		ctrlQ:     make(chan Frame, 256),
		hiQ:       make(chan Frame, 1024),
		loQ:       make(chan Frame, 1024),
		done:      make(chan struct{}),
		writerEnd: make(chan struct{}),
	}
}

// tuneTCP enlarges kernel socket buffers so a single session can keep a
// bandwidth-delay product of many MB in flight on high-RTT lossy paths
// (100Mbps x 170ms ~ 2MB BDP; auto-tuning caps lower on several platforms).
func tuneTCP(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(16 << 20)
		_ = tc.SetWriteBuffer(16 << 20)
	}
}

func (s *session) close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.conn.Close()
	})
}

// sendCtrl enqueues a control frame. Callers include openStreamLocked(), which
// runs while the client's app.mu is held, so this must never block: a ctrlQ
// full for 256 tiny frames means the writer is stuck on a 15s socket write
// (peer not reading), i.e. the session is dead. Closing it releases every
// reader instead of freezing all streams under app.mu for the write timeout.
func (s *session) sendCtrl(f Frame) {
	select {
	case s.ctrlQ <- f:
	case <-s.done:
	default:
		log.Printf("[session] %s ctrl queue jammed (writer stuck) -> close", s.name)
		s.close()
	}
}

// sendData enqueues a DATA frame. All DATA rides the one FIFO queue:
// choosing hiQ by frame size would let a small frame jump ahead of a bulk
// frame of the same stream when the link is slow (streams have no sequence
// numbers, so any jump = corruption). Interactivity isolation is done at
// the client level via multi-slot sessions (pickConnLocked), not by queue
// priority. Control frames (ctrlQ) still preempt DATA safely.
func (s *session) sendData(f Frame) {
	select {
	case s.loQ <- f:
	case <-s.done:
	}
}

// trySendData is the non-blocking variant used by the v0.11 upload path: the
// per-stream sender polls lanes and must never block on a full queue (a slow
// lane is skipped and its frames are retransmitted elsewhere if it stalls).
func (s *session) trySendData(f Frame) bool {
	select {
	case s.loQ <- f:
		return true
	case <-s.done:
		return false
	default:
		return false
	}
}

// sendOrdered enqueues on the bulk data path (loQ) so the frame stays in
// order with previously queued DATA. CLOSE must use this: an out-of-band
// CLOSE via ctrlQ overtakes buffered bulk data and truncates downloads.
func (s *session) sendOrdered(f Frame) {
	select {
	case s.loQ <- f:
	case <-s.done:
	}
}

// sendOrderedTimeout is sendOrdered with a deadline, for callers that must not
// hold or stall shared state indefinitely (stream FIN on a congested lane).
func (s *session) sendOrderedTimeout(f Frame, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case s.loQ <- f:
		return true
	case <-s.done:
		return false
	case <-t.C:
		return false
	}
}

func (s *session) writer() {
	defer close(s.writerEnd)
	for {
		var f Frame
		// priority: ctrl > hi > lo; fall through to blocking select when idle
		select {
		case f = <-s.ctrlQ:
		default:
			select {
			case f = <-s.hiQ:
			default:
				select {
				case f = <-s.loQ:
				default:
					select {
					case f = <-s.ctrlQ:
					case f = <-s.hiQ:
					case f = <-s.loQ:
					case <-s.done:
						return
					}
				}
			}
		}
		buf := encodeFrame(&f)
		s.tx.XORKeyStream(buf, buf)
		s.conn.SetWriteDeadline(deadline(writeTimeout))
		t0 := time.Now()
		if _, err := s.conn.Write(buf); err != nil {
			log.Printf("[session] %s write err after %v (frame typ=%d sid=%d len=%d): %v",
				s.name, time.Since(t0).Round(time.Millisecond), f.Typ, f.SID, len(f.Payload), err)
			s.close()
			return
		}
		if f.Typ == tData && s.onData != nil {
			// Server sets this to credit per-stream downlink window once a
			// frame is actually handed to the socket.
			s.onData(f)
		}
	}
}

func (s *session) keepalive() {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.sendCtrl(Frame{Typ: tPing})
		case <-s.done:
			return
		}
	}
}

// run reads and dispatches frames on the calling goroutine until the
// session ends. Control frames (PING/PONG/GOAWAY) are handled here.
func (s *session) run(dispatch func(*Frame)) error {
	go s.writer()
	go s.keepalive()
	var lastErr error
	for {
		s.conn.SetReadDeadline(deadline(idleTimeout))
		f, err := readFrameEnc(s.conn, s.rx)
		if err != nil {
			lastErr = err
			break
		}
		switch f.Typ {
		case tPing:
			s.sendCtrl(Frame{Typ: tPong})
		case tPong:
			// activity only
		case tGoaway:
			lastErr = errGoaway
			s.close()
			break
		default:
			dispatch(f)
		}
		if lastErr != nil {
			break
		}
	}
	s.close()
	<-s.writerEnd
	return lastErr
}
