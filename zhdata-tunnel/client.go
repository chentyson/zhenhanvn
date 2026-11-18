package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type clientConfig struct {
	listen   string // local socks5 listen addr
	server   string // zhdata server host:port; comma separated for multi-port
	uuid     string
	conns    int // parallel tunnel sessions (1..maxSlots)
	spread   int // max lanes one bulk stream may use (1 disables in-stream spread)
	train    int // seconds between per-session training frames; 0 = off (v0.8 experiment)
	migrate  int // v0.11.12: stalled-lane whole-backlog migration; 0 = off (default, download parity with v0.11.7), 1 = on
	slotbase int // v0.11.20: first slot index (pure addressing; run a second client instance beside the first without server-side takeover conflicts)
	dlconns  int // v0.11.13 split pools: last N sessions carry ONLY downloads (home/owner), first conns-N carry ONLY upload lanes; 0 = shared (v0.11.12)
	verbose  bool
}

// clientApp keeps cfg.conns parallel zhdata sessions (slots 0..conns-1) to
// the server. Local socks connections are assigned to the least loaded
// session. Each slot has independent reconnect/takeover state; the server
// applies takeover per (uuid,slot), so parallel slots do not GOAWAY each other.
//
// v0.4 in-stream parallelism: a single bulk upload stream is spread across
// several sessions so it can fill more of the line's per-flow shaping buckets.
// Every upload frame carries a stream-global sequence number; the server
// reorders by seq before replaying to the backend. Stream ids (sid) are
// globally unique per uuid so one sid can appear on multiple sessions.
type clientApp struct {
	cfg       clientConfig
	listener  net.Listener   // primary SOCKS listener (Addr()/Close reference)
	listeners []net.Listener // primary + dual-stack loopback companions; accept runs on all
	streamID  uint32         // global cStream id counter, shared across all listeners

	mu            sync.Mutex
	conns         []*cConn
	servers       []string            // dial targets; slot i dials servers[i % len(servers)]
	streams       map[uint32]*cStream // all live streams
	sidSeq        uint16              // sid allocator, unique per uuid across sessions
	bulk          int                 // live streams transferring bulk upload; drives the auto spread budget
	lanes         int                 // total spread lanes attached across all streams; hard cap = cfg.conns (never oversubscribe the slot pool)
	rr            uint32              // rotating cursor for home-slot selection (spreads streams over all slots)
	inflightBytes int64               // v0.11: bytes enqueued to sessions but not yet DELIVERED by the server, all upload streams (cap globalWindowBytes)
	dlStart       int                 // v0.11.13 split pools: sessions with idx >= dlStart are download-only (home); below are upload-only lanes; =len(conns) when disabled
	split         bool                // v0.11.13: split pools active (guards hand-built clientApp zero values)

	stopCh  chan struct{}
	stopped atomic.Bool

	train []byte // periodic per-session training payload (keeps shaping buckets + congestion state warm)
}

// cConn is one tunnel session slot and its stream bookkeeping.
type cConn struct {
	app *clientApp
	idx byte

	// guarded by app.mu
	sess    *session
	gen     uint64
	sessMap map[uint16]*cStream // sid -> stream (this session's namespace)
	members map[uint32]*cStream // streams that use this slot (home or spread)

	// lastAct = unix nano of last USER-data frame (dispatch/send; PING/PONG do
	// not count). Idle sessions are recycled because long-lived TCP flows get
	// their shaping bucket degraded by the ISP (measured: stale-session
	// download 0.07MB/s vs fresh-session 5.17MB/s, evening, 2026-11-16).
	lastAct atomic.Int64
}

type cStream struct {
	app   *clientApp
	id    uint32
	sid   uint16   // globally unique per uuid; same sid rides every attached session
	home  *cConn   // owns the stream (downlink flows here)
	conns []*cConn // home + extra spread slots, all carrying this sid

	local net.Conn
	atyp  byte
	addr  []byte
	port  uint16

	bulk     bool // stream has exceeded the bulk threshold (counts toward spread budget)
	attached int  // spread lanes beyond home held by this stream (counts toward global lane cap)

	// v0.11 (route A) upload send window; all fields guarded by app.mu.
	// A frame stays in sentq until the server reports it DELIVERED (tAck's
	// second word = rx.next-1), so the server reassembler can never be fed
	// more than streamWindowBytes out of order. Frames on a lane that stops
	// acking are retransmitted over the other lanes; the server dedupes by seq.
	sentq         []*sentFrame         // pending frames, ascending seq
	ackedSeq      uint32               // server delivered prefix (last in-order frame)
	inflightBytes int64                // bytes in sentq
	frontSince    time.Time            // when sentq[0] became the front (fast hole-retransmit timer)
	finSent       bool                 // v0.11.15: local EOF seen, tClose sent (tail resend exemption)
	reset         bool                 // v0.11.19: server tReset seen; suppress all retransmit for this stream
	finSeqVal     uint32               // v0.11.16: fin seq carried by tClose (for fin re-send)
	finT          time.Time            // v0.11.16: last tClose send time (re-send if unprocessed)
	laneAck       map[*cConn]uint32    // per-lane arrival counter (liveness)
	laneBytes     map[*cConn]int64     // pending bytes currently assigned to a lane
	laneSent      map[*cConn]uint32    // frames sent on this lane (for true in-TCP backlog = laneSent-laneAck)
	laneMigT      map[*cConn]time.Time // last whole-backlog migration time (v0.11.8 stalled-lane fix)
	// v0.11 pacing: in-order delivery of ONE app stream over N lanes can only
	// advance as fast as the lane carrying the next sequence number, so a
	// naive round-robin caps a stream at N x slowest-lane rate. Assign each
	// frame to the lane with the earliest estimated completion (pending/rate);
	// slow lanes then receive proportionally fewer frames and the stream rate
	// becomes ~sum(lane rates) instead of N x min(lane rates).
	laneRate map[*cConn]float64   // EWMA of delivered frames/s per lane
	laneCnt  map[*cConn]uint32    // last arrival count used for the rate EWMA
	laneT    map[*cConn]time.Time // last rate sample time

	rq      chan []byte // remote -> local queue
	done    chan struct{}
	doneOne sync.Once

	bkt    *bktOpen // zhv5: non-nil for bucket member streams (tOpen payload variant)
	grp    *bgrp    // zhv5: leader's bucket tracker (leader streams only)
	noPump bool     // zhv5: member created without a local socket (memory-fed)

	resendCount atomic.Int64 // diagnostics: frames retransmitted on another lane
}

// bktOpen describes a zhv5 bucket member stream: byte range [off,off+ln) of
// the leader stream's upload, keyed by the leader's sid. Sent as tOpen
// payload instead of the address; identical payload on every re-attach so a
// reconnect re-opens the same range.
type bktOpen struct {
	leaderSID uint16
	off       uint64
	ln        uint64
	final     bool
}

// bgrp tracks one bucketed upload on the leader side (guarded by mu).
type bgrp struct {
	mu      sync.Mutex
	ranges  []*bktRange   // remaining unfinalized ranges (ordered)
	total   uint64        // body length (Content-Length)
	doneCnt int           // ranges fully delivered
	left    int           // ranges not yet done
	wake    chan struct{} // buffered 1: progress/fail signal to pumpUpBuckets
	failed  bool

	// Concurrency cap: opening K members at once (K can be 16) swamps the
	// shared send window, so a range that fails and retries/splits can get
	// zero credit (acked=0) behind the wall of in-flight members. Bound
	// active members to ~2x upload lanes; queue the rest and drain on finish.
	maxActive int
	active    int
	queue     []*bktRange
}

type bktRange struct {
	off, ln uint64
	data    []byte
	retries int
	final   bool
}

// sentFrame is one upload DATA frame kept for retransmission until delivered.
type sentFrame struct {
	seq     uint32
	payload []byte // includes the 4B seq prefix; retransmitted verbatim
	lane    *cConn // lane it is currently assigned to; nil once released
}

// ensureLaneLocked initializes per-lane bookkeeping for an attached slot.
// Caller must hold app.mu.
func (s *cStream) ensureLaneLocked(cn *cConn) {
	if s.laneAck == nil {
		s.laneAck = map[*cConn]uint32{}
		s.laneBytes = map[*cConn]int64{}
		s.laneSent = map[*cConn]uint32{}
		s.laneMigT = map[*cConn]time.Time{}
		s.laneRate = map[*cConn]float64{}
		s.laneCnt = map[*cConn]uint32{}
		s.laneT = map[*cConn]time.Time{}
	}
	if _, ok := s.laneAck[cn]; !ok {
		s.laneAck[cn] = 0
		s.laneCnt[cn] = 0
		s.laneT[cn] = time.Now()
		s.laneRate[cn] = laneRateInit
	}
	if _, ok := s.laneBytes[cn]; !ok {
		s.laneBytes[cn] = 0
	}
	if _, ok := s.laneSent[cn]; !ok {
		s.laneSent[cn] = 0
	}
}

func newClientApp(cfg clientConfig) (*clientApp, error) {
	// key comparison is case-insensitive (server users list is normalized to
	// lowercase); normalize here so "Iammaster1125" == "iammaster1125".
	cfg.uuid = strings.ToLower(strings.TrimSpace(cfg.uuid))
	if cfg.conns < 1 {
		cfg.conns = 1
	}
	if cfg.conns > maxSlots {
		cfg.conns = maxSlots
	}
	if cfg.spread < 1 {
		cfg.spread = 8 // v0.5 pull credit: robust across uneven lanes
	}
	if cfg.spread > cfg.conns {
		cfg.spread = cfg.conns
	}
	// v0.11.13 split pools: last dlconns sessions ONLY own streams (download +
	// control), first conns-dlconns sessions ONLY carry upload lanes/spares.
	// Upload bursts, hole resends and migrations can no longer queue in front
	// of a download session's TCP ACKs (measured: shared-pool NEW instance dl
	// 0.1-0.5MB/s vs idle-pool OLD 1.6-6.6MB/s at identical minutes).
	// v0.11.18: -1 (unset) = auto half-and-half split (conns/2).
	if cfg.dlconns < 0 {
		cfg.dlconns = cfg.conns / 2
	}
	// v0.11.20: slot addressing window (two instances must not share slots).
	if cfg.slotbase < 0 {
		cfg.slotbase = 0
	}
	if cfg.slotbase+cfg.conns > 64 {
		cfg.slotbase = 64 - cfg.conns
		if cfg.slotbase < 0 {
			cfg.slotbase = 0
		}
	}
	if cfg.dlconns > cfg.conns-1 {
		cfg.dlconns = cfg.conns - 1
	}
	dlStart := cfg.conns
	if cfg.dlconns > 0 {
		dlStart = cfg.conns - cfg.dlconns
	}
	split := cfg.dlconns > 0
	lns, err := socksListeners(cfg.listen)
	if err != nil {
		return nil, err
	}
	servers := splitCSV(cfg.server)
	if len(servers) == 0 {
		return nil, fmt.Errorf("no server address")
	}
	a := &clientApp{
		cfg:       cfg,
		listener:  lns[0],
		listeners: lns,
		servers:   servers,
		streams:   map[uint32]*cStream{},
		stopCh:    make(chan struct{}),
		dlStart:   dlStart,
		split:     split,
	}
	for i := 0; i < cfg.conns; i++ {
		a.conns = append(a.conns, &cConn{
			app:     a,
			idx:     byte(i + cfg.slotbase),
			sessMap: map[uint16]*cStream{},
			members: map[uint32]*cStream{},
		})
	}
	for _, ln := range a.listeners {
		go a.acceptLoop(ln)
	}
	go a.stallSweepLoop()
	go a.sessionRecycleLoop()
	for _, cn := range a.conns {
		go cn.sessionLoop()
	}
	a.train = make([]byte, trainPayload)
	if cfg.train > 0 {
		go a.trainLoop()
	}
	return a, nil
}

// trainLoop pushes one padding frame per live session on each tick. The
// non-blocking push means an already-busy session is skipped: training only
// spends bytes when the session is idle, which is exactly when its TCP
// congestion state and the ISP's per-flow shaping bucket go cold.
func (a *clientApp) trainLoop() {
	t := time.NewTicker(time.Duration(a.cfg.train) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.stopCh:
			return
		case <-t.C:
		}
		a.mu.Lock()
		for _, cn := range a.conns {
			sess := cn.sess
			if sess == nil {
				continue
			}
			for k := 0; k < trainFrames; k++ {
				select {
				case sess.loQ <- Frame{Typ: tData, SID: 0, Payload: a.train}:
				default:
				}
			}
		}
		a.mu.Unlock()
	}
}

func (a *clientApp) Addr() string { return a.listener.Addr().String() }

// socksListeners binds the SOCKS endpoint and, for loopback/wildcard hosts,
// also binds the companion IP family so that clients using `localhost`
// (which Windows often resolves to IPv6 ::1) connect just like 127.0.0.1.
// The primary address must bind; companion binds are best-effort (skipped if
// that family is unavailable) so the client never fails to start over them.
func socksListeners(addr string) ([]net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	lns := []net.Listener{ln}
	host, port, perr := net.SplitHostPort(addr)
	if perr != nil {
		return lns, nil
	}
	var companion string
	switch host {
	case "127.0.0.1":
		companion = net.JoinHostPort("::1", port)
	case "::1":
		companion = net.JoinHostPort("127.0.0.1", port)
	case "", "0.0.0.0":
		companion = net.JoinHostPort("::", port)
	}
	if companion != "" {
		if ln2, err := net.Listen("tcp", companion); err == nil {
			lns = append(lns, ln2)
		} else {
			log.Printf("[client] companion listen %s unavailable: %v", companion, err)
		}
	}
	return lns, nil
}

func (a *clientApp) Close() {
	if a.stopped.CompareAndSwap(false, true) {
		close(a.stopCh)
		for _, ln := range a.listeners {
			ln.Close()
		}
		a.mu.Lock()
		for _, cn := range a.conns {
			if cn.sess != nil {
				cn.sess.close()
			}
		}
		a.mu.Unlock()
	}
}

func (a *clientApp) logf(format string, v ...any) {
	if a.cfg.verbose {
		log.Printf("[client] "+format, v...)
	}
}

// ---------------- local socks side ----------------

func (a *clientApp) acceptLoop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if !a.stopped.Load() {
				log.Printf("[client] accept: %v", err)
			}
			return
		}
		c.SetDeadline(deadline(handshakeTimeout))
		atyp, addr, port, err := socksAccept(c)
		c.SetDeadline(noDeadline())
		if err != nil {
			a.logf("socks accept: %v", err)
			c.Close()
			continue
		}
		s := &cStream{
			app:   a,
			id:    atomic.AddUint32(&a.streamID, 1),
			local: c,
			atyp:  atyp, addr: addr, port: port,
			rq:   make(chan []byte, 64),
			done: make(chan struct{}),
		}
		a.mu.Lock()
		a.streams[s.id] = s
		s.sid = a.allocSidLocked()
		home := a.pickConnLocked()
		s.home = home
		s.conns = []*cConn{home}
		home.members[s.id] = s
		s.ensureLaneLocked(home)
		if home.sess != nil {
			home.openStreamLocked(s)
		}
		a.mu.Unlock()
		go s.pumpUp()   // local -> remote (spreads over conns, survives reconnects)
		go s.pumpDown() // remote -> local
	}
}

// dlPool reports whether cn is a download-only session (v0.11.13 split pools).
// Read-only field, no lock needed.
func (a *clientApp) dlPool(cn *cConn) bool { return a.split && int(cn.idx) >= a.dlStart }

// pickConnLocked chooses the slot with the fewest assigned streams, breaking
// ties with a rotating cursor. Without the rotation every new stream picked
// conns[0] while all slots were idle, so a browser's 4-6 streams all landed on
// the first few slots: those long-lived TCP flows accumulated loss/reordering
// history (client-side CUBIC cwnd collapse, which we cannot tune on Windows)
// while the other ~26 sessions sat unused. Rotating spreads new streams over
// all slots, so every stream starts on a TCP flow with fresh congestion state,
// like the fresh connections the raw-TCP baseline uses.
// Caller holds a.mu.
func (a *clientApp) pickConnLocked() *cConn {
	n := len(a.conns)
	start := int(a.rr % uint32(n))
	a.rr++
	// v0.11.13: with split pools homes come only from the download pool.
	if lo := a.dlStart; a.split && lo < n {
		span := n - lo
		c0 := lo + start%span
		best := a.conns[c0]
		for i := 1; i < span; i++ {
			cn := a.conns[lo+(c0-lo+i)%span]
			if len(cn.members) < len(best.members) {
				best = cn
			}
		}
		return best
	}
	best := a.conns[start]
	for i := 1; i < n; i++ {
		cn := a.conns[(start+i)%n]
		if len(cn.members) < len(best.members) {
			best = cn
		}
	}
	return best
}

// allocSidLocked returns a sid unused in any current session. Caller holds a.mu.
func (a *clientApp) allocSidLocked() uint16 {
	for n := 0; n < 0xFFFF; n++ {
		a.sidSeq++
		if a.sidSeq == 0 {
			a.sidSeq = 1
		}
		sid := a.sidSeq
		inUse := false
		for _, cn := range a.conns {
			if _, dup := cn.sessMap[sid]; dup {
				inUse = true
				break
			}
		}
		if !inUse {
			return sid
		}
	}
	return 0 // sid space exhausted (pathological)
}

// ---------------- per-slot session management ----------------

// openStreamLocked registers stream s's sid in this slot's session and sends
// OPEN. Caller holds a.mu.
func (cn *cConn) openStreamLocked(s *cStream) {
	if s.sid == 0 || cn.sess == nil {
		return
	}
	cn.sessMap[s.sid] = s
	// Opening a stream is activity: keep this session off the idle-recycle
	// victim list long enough for the stream's handshake to complete, so a
	// brand-new connection is not torn down mid-open (the SOCKS "-1" stall).
	// Idle streams past the idle timeout are still recycled, which refreshes
	// decayed TCP flows and keeps downloads fast (the sessMap-based "never
	// recycle a stream-attached session" guard pinned idle sessions, their
	// flows decayed, and downloads dropped ~4x: 60M -> 15M).
	cn.lastAct.Store(time.Now().UnixNano())
	if s.bkt != nil { // zhv5 member stream: range descriptor instead of address
		cn.sess.sendCtrl(Frame{Typ: tOpen, SID: s.sid, Payload: encodeBucketMember(s.bkt.leaderSID, s.bkt.off, s.bkt.ln, s.bkt.final)})
		return
	}
	cn.sess.sendCtrl(Frame{Typ: tOpen, SID: s.sid, Payload: encodeAddrPort(s.atyp, s.addr, s.port)})
}

// currentSidLocked resolves the sid of stream s in this slot's session.
// Caller holds a.mu.
func (cn *cConn) currentSidLocked(s *cStream) uint16 {
	for sid, st := range cn.sessMap {
		if st == s {
			return sid
		}
	}
	return 0
}

// waitOpen blocks until stream s has a live session+sid on slot cn.
func (cn *cConn) waitOpen(s *cStream) bool {
	for {
		cn.app.mu.Lock()
		sess := cn.sess
		sid := cn.currentSidLocked(s)
		cn.app.mu.Unlock()
		if sess != nil && sid != 0 {
			return true
		}
		select {
		case <-s.done:
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (cn *cConn) dialSession() (*session, error) {
	a := cn.app
	target := a.servers[int(cn.idx)%len(a.servers)]
	c, err := net.DialTimeout("tcp", target, handshakeTimeout)
	if err != nil {
		return nil, err
	}
	tuneTCP(c)
	c.SetDeadline(deadline(handshakeTimeout))
	salt := randBytes(16)
	tx := newCTR(a.cfg.uuid, salt)
	hp := make([]byte, 32)
	copy(hp, magic)
	hp[4] = cn.idx // session slot: server keys takeover per (uuid,slot)
	copy(hp[5:], randBytes(8))
	tx.XORKeyStream(hp, hp) // tx consumed 32 keystream bytes; frames continue after
	if _, err = c.Write(append(salt, hp...)); err != nil {
		c.Close()
		return nil, err
	}
	c.SetDeadline(noDeadline())
	rx := newCTR(a.cfg.uuid, salt)
	return newSession("client->"+target, c, tx, rx), nil
}

func (cn *cConn) sessionLoop() {
	a := cn.app
	backoff := time.Second
	goawayStreak := 0
	for {
		select {
		case <-a.stopCh:
			return
		default:
		}
		sess, err := cn.dialSession()
		if err != nil {
			a.logf("slot %d dial: %v (retry in %v)", cn.idx, err, backoff)
			if !sleepSelect(a.stopCh, backoff) {
				return
			}
			backoff = minDur(backoff*2, 30*time.Second)
			continue
		}
		a.mu.Lock()
		// Frames queued on the previous (dead) connection of this lane are gone;
		// collect them before the lane looks alive again, then re-send over the
		// surviving lanes (the server dedupes by seq).
		type laneLost struct {
			s      *cStream
			frames []*sentFrame
		}
		var lost []laneLost
		for _, s := range cn.members {
			if s.home == cn || s.laneBytes[cn] <= 0 {
				continue
			}
			var frs []*sentFrame
			for _, fr := range s.sentq {
				if fr.lane == cn {
					frs = append(frs, fr)
				}
			}
			if len(frs) > 0 {
				lost = append(lost, laneLost{s: s, frames: frs})
			}
		}
		cn.sess = sess
		cn.gen++
		cn.sessMap = map[uint16]*cStream{}
		for _, s := range cn.members {
			s.ensureLaneLocked(cn)
			// Fresh session: the server restarts this lane's arrival counter and
			// the old rate estimate says nothing about the new TCP connection.
			s.laneSent[cn] = 0
			s.laneAck[cn] = 0
			s.laneCnt[cn] = 0
			s.laneMigT[cn] = time.Time{}
			s.laneT[cn] = time.Now()
			s.laneRate[cn] = laneRateInit
			cn.openStreamLocked(s)
		}
		gen := cn.gen
		nstreams := len(cn.members)
		a.mu.Unlock()
		for _, l := range lost {
			for _, fr := range l.frames {
				l.s.resendFrame(fr)
			}
		}
		backoff = time.Second
		if a.cfg.conns == 1 {
			log.Printf("[client] session established (gen=%d, reopen streams=%d)", gen, nstreams)
		} else {
			log.Printf("[client] session established slot=%d (gen=%d, reopen streams=%d)", cn.idx, gen, nstreams)
		}
		estStart := time.Now()
		cn.lastAct.Store(estStart.UnixNano())
		err = sess.run(func(f *Frame) {
			cn.lastAct.Store(time.Now().UnixNano())
			t0 := time.Now()
			cn.dispatch(gen, f)
			if d := time.Since(t0); d > 500*time.Millisecond && a.cfg.verbose {
				log.Printf("[client] dispatch slow slot=%d typ=%d sid=%d took=%v", cn.idx, f.Typ, f.SID, d.Round(time.Millisecond))
			}
		})
		a.mu.Lock()
		if cn.gen == gen {
			cn.sess = nil
			cn.sessMap = map[uint16]*cStream{}
		}
		// A dead session cannot be resumed mid-stream: the server-side backend
		// connection and reassembler state died with it, so "re-opening" the
		// stream would dial the target again and splice a fresh response into
		// a half-consumed app socket (downloads hang/corrupt) or silently lose
		// the middle of an upload. Fail the streams homed on this slot fast so
		// the app retries; spread lanes (home elsewhere) keep the stream alive.
		var dead []*cStream
		for _, s := range cn.members {
			if s.home == cn {
				dead = append(dead, s)
			}
		}
		a.mu.Unlock()
		for _, s := range dead {
			a.logf("slot %d lost: failing stream %d (sid=%d) instead of resuming corrupt", cn.idx, s.id, s.sid)
			s.teardown()
		}
		switch err {
		case errGoaway:
			// another connection holds this (uuid,slot). If our session died
			// fast, we are the losing side of a takeover war: back off
			// exponentially instead of churn-reconnecting at 1/s.
			if time.Since(estStart) < 15*time.Second {
				goawayStreak++
			} else {
				goawayStreak = 0
			}
			shift := goawayStreak
			if shift > 6 {
				shift = 6
			}
			d := minDur(time.Duration(1<<shift)*time.Second, 60*time.Second)
			log.Printf("[client] slot %d taken over (goaway), reconnect in %v", cn.idx, d)
			if !sleepSelect(a.stopCh, d) {
				return
			}
		default:
			goawayStreak = 0
			a.logf("slot %d session ended: %v (retry in %v)", cn.idx, err, backoff)
			if !sleepSelect(a.stopCh, backoff) {
				return
			}
			backoff = minDur(backoff*2, 30*time.Second)
		}
	}
}

func (cn *cConn) dispatch(gen uint64, f *Frame) {
	a := cn.app
	a.mu.Lock()
	if gen != cn.gen {
		a.mu.Unlock()
		return
	}
	s := cn.sessMap[f.SID]
	sess := cn.sess
	a.mu.Unlock()

	switch f.Typ {
	case tOpenAck:
		a.logf("open ack slot=%d sid=%d", cn.idx, f.SID)
	case tData:
		if s == nil {
			if sess != nil {
				sess.sendCtrl(Frame{Typ: tReset, SID: f.SID})
			}
			return
		}
		select {
		case s.rq <- f.Payload:
		case <-s.done:
		default:
			// The app-side reader is momentarily behind. Wait a short, bounded
			// time (a browser JS stall is normally <100ms), then fail ONLY this
			// stream. Never block the session reader indefinitely: that used to
			// freeze every stream on the slot until the peer's 15s write
			// timeout killed the whole TCP session (and the app hung).
			select {
			case s.rq <- f.Payload:
			case <-s.done:
			case <-time.After(2 * time.Second):
				a.logf("downlink queue full sid=%d slot=%d for 2s -> fail stream (app not reading)", f.SID, cn.idx)
				if sess != nil {
					sess.sendCtrl(Frame{Typ: tReset, SID: f.SID})
				}
				s.teardown()
			}
		}
	case tClose:
		if s != nil {
			// nil sentinel rides the same FIFO lane as DATA so we never
			// CloseWrite the local socket before queued chunks are flushed.
			select {
			case s.rq <- nil:
			case <-s.done:
			default:
				// Bounded wait like DATA: a slow app reader must not stall the
				// whole session reader (peer write timeout -> session death).
				select {
				case s.rq <- nil:
				case <-s.done:
				case <-time.After(2 * time.Second):
					a.logf("downlink close queue full sid=%d slot=%d for 2s -> fail stream", f.SID, cn.idx)
					s.teardown()
				}
			}
		}
	case tReset:
		if s != nil {
			s.app.logf("tReset received sid=%d slot=%d -> teardown local conn", f.SID, cn.idx)
			// v0.11.19: the server reclaimed this stream (hole watchdog /
			// backend death). Stop ALL retransmit/migration/fin re-sends for
			// it: before this the tail resend + redoFin looped every sweep
			// against a dead sid, burning uplink on tReset unknown-stream
			// storms (118 in 10 min during one speedtest).
			s.app.mu.Lock()
			s.reset = true
			s.app.mu.Unlock()
			s.teardown()
		}
	case tAck:
		if s != nil && len(f.Payload) >= 4 {
			var delivered uint32
			if len(f.Payload) >= 8 {
				// v0.11: second word is the server's delivered prefix (rx.next-1),
				// not the old reorder-pressure byte count.
				delivered = binary.BigEndian.Uint32(f.Payload[4:])
			}
			s.applyAck(cn, binary.BigEndian.Uint32(f.Payload), delivered)
		}
	}
}

// ---------------- stream pumps ----------------

func (s *cStream) teardown() {
	s.doneOne.Do(func() {
		close(s.done)
		if s.local != nil { // zhv5 bucket members are memory-fed (no local socket)
			s.local.Close()
		}
		s.app.mu.Lock()
		// Release the upload window accounting held by pending frames.
		for _, fr := range s.sentq {
			n := int64(len(fr.payload))
			s.inflightBytes -= n
			if fr.lane != nil {
				s.laneBytes[fr.lane] -= n
			}
			fr.lane = nil
		}
		s.app.inflightBytes -= s.inflightBytes
		if s.app.inflightBytes < 0 {
			s.app.inflightBytes = 0
		}
		s.sentq = nil
		s.inflightBytes = 0
		if s.bulk {
			s.bulk = false
			if s.app.bulk > 0 {
				s.app.bulk--
			}
		}
		if s.attached > 0 {
			s.app.lanes -= s.attached
			if s.app.lanes < 0 {
				s.app.lanes = 0
			}
			s.attached = 0
		}
		delete(s.app.streams, s.id)
		for _, cn := range s.conns {
			delete(cn.members, s.id)
			for sid, st := range cn.sessMap {
				if st == s {
					delete(cn.sessMap, sid)
				}
			}
		}
		s.app.mu.Unlock()
	})
}

func (s *cStream) closeWriteLocal() {
	s.app.logf("stream %d closeWriteLocal", s.id)
	if tc, ok := s.local.(*net.TCPConn); ok {
		tc.CloseWrite()
	} else {
		s.teardown()
	}
}

// dbgLog prints per-stream upload pacing state (ZHD_DEBUG2=1): bytes/frames
// pushed into lanes, average frame size, delivered prefix, window usage, and
// every lane's pending bytes / ack counter / measured delivery rate.
func (s *cStream) dbgLog(sent, frames int64, t0 time.Time) {
	s.app.mu.Lock()
	var b strings.Builder
	for _, cn := range s.conns {
		fmt.Fprintf(&b, " s%d:pend%dB/ack%d/sent%d/r%.0f", cn.idx, s.laneBytes[cn], s.laneAck[cn], s.laneSent[cn], s.laneRate[cn])
	}
	lanes := len(s.conns)
	inflight, acked := s.inflightBytes, s.ackedSeq
	s.app.mu.Unlock()
	avg := int64(0)
	if frames > 0 {
		avg = sent / frames
	}
	log.Printf("[dbg] sid=%d sent=%d frames=%d avg=%dB lanes=%d inflight=%dB ackedSeq=%d el=%s%s",
		s.sid, sent, frames, avg, lanes, inflight, acked, time.Since(t0).Round(time.Second), b.String())
}

// pickLaneSendLocked picks the live lane whose backlog will drain first:
// score = (true in-TCP backlog frames + 1) / measured lane rate. The backlog is
// laneSent-laneAck (frames handed to the lane's TCP minus frames it has
// already delivered to the server): this is the queue delay a frame assigned
// now will actually experience. (A lane's "unreleased" window bytes are NOT a
// good score: when one hole stalls stream delivery, every lane's frames stay
// unreleased, so that metric would deprioritise the healthy lanes exactly when
// they are needed.) Slow lanes get proportionally fewer frames, so an in-order
// multi-lane stream advances at ~sum(lane rates) instead of N x min(lane rate).
// Returns (nil, true) when lanes exist but none is usable yet.
// Caller must hold app.mu.
func (s *cStream) pickLaneSendLocked() (*cConn, bool) {
	var best *cConn
	bestETA := float64(1 << 62)
	live := false
	now := time.Now()
	for _, cn := range s.conns {
		if cn.sess == nil || cn.currentSidLocked(s) == 0 {
			continue
		}
		// v0.11.13: split pools — upload frames avoid download-pool
		// sessions; only home stays eligible (carries the tiny request
		// frames of streams that have no upload lanes yet).
		if cn != s.home && s.app.dlPool(cn) {
			continue
		}
		live = true
		r := s.laneRate[cn]
		backlog := int64(s.laneSent[cn]) - int64(s.laneAck[cn])
		if backlog < 0 {
			backlog = 0
		}
		// A lane is suspect only when it has frames IN FLIGHT that have not
		// been delivered for a while. An idle lane (nothing outstanding) is
		// NOT slow: v0.11.1 demoted any lane without recent ack progress, so
		// every lane not currently loaded starved (no frames -> no acks ->
		// stale laneT -> min rate -> no frames), collapsing an 8-lane stream
		// to ~1 lane (tunnel x1 12.6M vs raw x1 20.4M).
		if backlog > 0 && now.Sub(s.laneT[cn]) > 400*time.Millisecond {
			r = laneRateMin
		}
		if r < laneRateMin {
			r = laneRateMin
		}
		eta := (float64(backlog) + 1) / r
		if eta < bestETA {
			best, bestETA = cn, eta
		}
	}
	return best, live
}

// applyAck records a lane's arrival counter and delivery rate, and the server's
// delivered-seq prefix. Frames with seq <= delivered are released from the
// window; this is the reliable-delivery signal that makes fan-out safe.
func (s *cStream) applyAck(cn *cConn, count, delivered uint32) {
	s.app.mu.Lock()
	defer s.app.mu.Unlock()
	if count > s.laneAck[cn] {
		now := time.Now()
		dt := now.Sub(s.laneT[cn]).Seconds()
		delta := int(count) - int(s.laneCnt[cn])
		s.laneT[cn] = now // keep "last progress" fresh even for short gaps
		if dt >= 0.02 && delta > 0 {
			r := float64(delta) / dt
			old := s.laneRate[cn]
			if old <= 0 {
				old = r
			}
			// EWMA: react within a few acks but do not chase single bursts.
			s.laneRate[cn] = 0.6*old + 0.4*r
		}
		if delta > 0 {
			s.laneCnt[cn] = count
		}
		s.laneAck[cn] = count
	}
	if delivered > s.ackedSeq {
		s.ackedSeq = delivered
		s.releaseDeliveredLocked()
	}
}

// releaseDeliveredLocked frees every queued frame the server has delivered in
// order. Caller holds app.mu.
func (s *cStream) releaseDeliveredLocked() {
	released := false
	for len(s.sentq) > 0 && s.sentq[0].seq <= s.ackedSeq {
		fr := s.sentq[0]
		s.sentq[0] = nil
		s.sentq = s.sentq[1:]
		n := int64(len(fr.payload))
		s.inflightBytes -= n
		if fr.lane != nil {
			s.laneBytes[fr.lane] -= n
			fr.lane = nil
		}
		s.app.inflightBytes -= n
		if s.app.inflightBytes < 0 {
			s.app.inflightBytes = 0
		}
		released = true
	}
	if released && len(s.sentq) > 0 {
		s.frontSince = time.Now() // a new front frame starts the hole timer
	}
}

// sendFrame enqueues one seq-tagged DATA frame, respecting the per-stream and
// global unacked windows (server-delivered feedback). It blocks (with short
// polls) while the window is full, which is the true end-to-end backpressure.
// Returns false if the stream was torn down meanwhile.
func (s *cStream) sendFrame(seq uint32, payload []byte) bool {
	n := int64(len(payload))
	fr := &sentFrame{seq: seq, payload: payload}
	for {
		s.app.mu.Lock()
		over := s.inflightBytes+n > streamWindowBytes || s.app.inflightBytes+n > globalWindowBytes
		if over {
			s.app.mu.Unlock()
			select {
			case <-s.done:
				return false
			case <-time.After(2 * time.Millisecond):
			}
			continue
		}
		cn, live := s.pickLaneSendLocked()
		if cn == nil {
			s.app.mu.Unlock()
			if !live {
				if !s.waitOpenAny() {
					return false
				}
			} else {
				if !sleepSelect(s.done, time.Millisecond) {
					return false
				}
			}
			continue
		}
		// Reserve the window slot before enqueueing so accounting is exact.
		s.inflightBytes += n
		s.app.inflightBytes += n
		s.laneBytes[cn] += n
		s.laneSent[cn]++
		cn.lastAct.Store(time.Now().UnixNano())
		fr.lane = cn
		wasEmpty := len(s.sentq) == 0
		s.sentq = append(s.sentq, fr)
		if wasEmpty {
			s.frontSince = time.Now()
		}
		s.app.mu.Unlock()

		if cn.sess.trySendData(Frame{Typ: tData, SID: s.sid, Payload: payload}) {
			return true
		}
		// Lane queue is full (slow lane): give the slot back and pick another.
		s.app.mu.Lock()
		if last := len(s.sentq) - 1; last >= 0 && s.sentq[last] == fr {
			s.sentq[last] = nil
			s.sentq = s.sentq[:last]
		}
		s.inflightBytes -= n
		s.app.inflightBytes -= n
		s.laneBytes[cn] -= n
		s.laneSent[cn]--
		fr.lane = nil
		s.app.mu.Unlock()
		if !sleepSelect(s.done, time.Millisecond) {
			return false
		}
	}
}

// stallSweepLoop periodically runs stall detection over streams with in-flight
// upload data. One app-wide sweep instead of a ticker goroutine per stream
// (browsers/speedtests open hundreds of short streams).
func (a *clientApp) stallSweepLoop() {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-a.stopCh:
			return
		case <-t.C:
		}
		a.mu.Lock()
		sts := make([]*cStream, 0, len(a.streams))
		for _, s := range a.streams {
			// v0.11.16: also sweep streams whose data is fully acked but the
			// FIN is unprocessed (inflightBytes==0): the fin frame sat in a
			// dead lane for 20s (finish slow ackedSeq==lastSeq) and the
			// server eventually reset the stream (hole unfilled -> Ookla
			// stream aborted). Tails must be kept alive by resend/migration.
			if s.inflightBytes > 0 || s.finSent {
				sts = append(sts, s)
			}
		}
		a.mu.Unlock()
		for _, s := range sts {
			select {
			case <-s.done:
				continue
			default:
			}
			s.checkStalls()
		}
	}
}

// sessionIdleRecycle: how long a session may carry no user data before we
// re-dial it. ISP shapers degrade long-lived TCP flows (evenings: stale
// sessions delivered 0.07MB/s downloads while a fresh TCP did 6MB/s; hy2 UDP
// untouched). Idle -> close -> sessionLoop reconnects (1s backoff). PING/PONG
// are handled inside session.run and deliberately do NOT refresh lastAct.
const sessionIdleRecycle = 25 * time.Second

// sessionRecycleLoop closes the single most-idle established session every
// sweep so the pool continuously refreshes its TCP flows without ever
// disrupting sessions that are actively transferring.
func (a *clientApp) sessionRecycleLoop() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.stopCh:
			return
		case <-t.C:
		}
		now := time.Now()
		var victim *cConn
		var vt time.Time
		// v0.11.17: upload-pool sessions recycle on a SHORTER idle timer (10s):
		// speedtest-style gaps (10-25s) left the whole upload pool on decayed
		// buckets while raw baselines dialed fresh flows (deep-valley raw x4
		// 13.1M vs tunnel 2.1M, streams 26s vs raw 8s). Downloads keep 25s.
		idleTO := func(cn *cConn) time.Duration {
			if a.split && !a.dlPool(cn) {
				return 10 * time.Second
			}
			return sessionIdleRecycle
		}
		a.mu.Lock()
		for _, cn := range a.conns {
			if cn.sess == nil {
				continue
			}
			tt := time.Unix(0, cn.lastAct.Load())
			if now.Sub(tt) < idleTO(cn) {
				continue
			}
			if victim == nil || tt.Before(vt) {
				victim, vt = cn, tt
			}
		}
		idle := 0
		for _, cn := range a.conns {
			if cn.sess != nil && now.Sub(time.Unix(0, cn.lastAct.Load())) >= idleTO(cn) {
				idle++
			}
		}
		a.mu.Unlock()
		if victim != nil {
			if a.cfg.verbose {
				log.Printf("[client] recycle: slot %d idle %v -> redial (%d idle sessions)", victim.idx, now.Sub(vt).Round(time.Second), idle)
			}
			victim.sess.close() // sessionLoop reconnects with backoff
		}
	}
}

// checkStalls handles two cases:
//  1. a lane whose session DIED: its in-flight frames are gone forever, so they
//     are re-sent over the surviving lanes and the dead lane is detached once
//     empty;
//  2. a reorder hole: the front (lowest undelivered) frame blocks delivery of
//     everything behind it, so it is fast-retransmitted after ~2 RTT.
//
// A live-but-slow lane is deliberately NOT migrated: TCP does not lose bytes on
// a live connection, its frames will arrive, and migrating them used to pile
// whole windows onto one lane / create a moving hole (livelock). Only lost
// frames (dead session) or an actual hole need action.
func (s *cStream) checkStalls() {
	s.app.mu.Lock()
	if s.reset { // v0.11.19: server reclaimed the stream; nothing to salvage
		s.app.mu.Unlock()
		return
	}
	s.app.mu.Unlock()
	now := time.Now()
	var frames []*sentFrame
	var dead []*cConn
	s.app.mu.Lock()
	for _, cn := range s.conns {
		if cn.sess != nil || cn == s.home {
			// live lane (slowness is handled by the hole path), or the home
			// session whose death makes sessionLoop fail the whole stream.
			continue
		}
		for _, fr := range s.sentq {
			if fr.lane == cn {
				frames = append(frames, fr)
			}
		}
		dead = append(dead, cn)
	}
	// v0.11.8: stalled-lane migration. A session that stays connected but whose
	// TCP delivers nothing (one flow shaped to ~0 by the ISP) parks a backlog
	// that blocks deliveredPrefix for the WHOLE stream; the 4-frame hole burst
	// then drains it at ~4 frames/400ms (~0.4MB/s) — measured Ookla uploads
	// took 22-31s with tails stuck (ackedSeq 443/511 for 20s). Once a lane
	// shows no delivery progress for a while, move its entire pending backlog
	// to the faster lanes at once (server dedupes by seq).
	// v0.11.11 gate: BULK streams only (queue >= 64 frames ~ 1MB). Applying
	// migration to 1-2 frame streams (GET request frames etc.) re-sent frames
	// to already-closed server streams: 227 tReset unknown-stream + finish
	// slow x9 per minute vs baseline, and the browser saw "download test
	// error". Small streams keep the v0.11.7 hole logic untouched.
	// v0.11.12: OFF by default (-migrate 1 to enable). Migration re-injects up
	// to a whole 6MB window onto sessions shared with concurrent downloads;
	// on the shaped uplink that delays those downloads' TCP ACKs and users
	// measured pure-download slower than v0.11.7. Burst(8) hole drain alone
	// already lifts stuck-lane recovery 0.4->2MB/s.
	// v0.11.14: the gate is per-STREAM volume, not just current queue size:
	// bulk streams finishing (sentq drained to 1-63 frames) still park tails
	// on dead lanes (finish slow ackedSeq=1651/1671 waited=20s). A stream is
	// "bulk" once its lifetime sent total >= 64 frames; GET-sized streams
	// (1-3 frames total) never qualify.
	if s.app.cfg.migrate > 0 && len(s.sentq) >= 2 {
		total := uint32(0)
		for _, cn := range s.conns {
			total += s.laneSent[cn]
		}
		if total >= 64 {
			for _, cn := range s.conns {
				if cn.sess == nil {
					continue
				}
				backlog := int64(s.laneSent[cn]) - int64(s.laneAck[cn])
				if backlog <= 0 {
					continue
				}
				// v0.11.16: migration also fires on SLOW lanes, not only dead
				// ones: deep-valley lanes keep trickling acks (laneT stays
				// fresh) yet drain a 37-frame backlog in 30s+ (finish slow
				// ackedSeq=89/389). If the lane's measured rate cannot clear
				// its backlog within ~2s, move the backlog. Healthy lane:
				// rate 50/s, backlog 37 -> 100 > 37 -> untouched.
				stalled := now.Sub(s.laneT[cn]) > 600*time.Millisecond
				slow := s.laneRate[cn]*2.0 < float64(backlog)
				if !stalled && !slow {
					continue
				}
				if now.Sub(s.laneMigT[cn]) < 2*time.Second {
					continue
				}
				s.laneMigT[cn] = now
				for _, fr := range s.sentq {
					if fr.lane == cn {
						frames = append(frames, fr)
					}
				}
			}
		}
	}
	// Fast hole recovery: re-send the front burst as soon as it has been
	// undelivered for ~2 RTT; if both copies were lost the next sweep retries.
	// Only when frames are queued BEHIND the front: a lone pending frame that
	// the backend has not delivered yet is not a hole, and blindly re-sending
	// it every sweep just floods duplicates.
	// v0.11.15: the old fast-hole rule ("only re-send when frames are queued
	// BEHIND the front") starves stream TAILS: the last frames of a stream
	// have nothing behind them, so a dead lane parking them is drained only
	// by migration — which needs the 600ms+2s gate — and if the whole upload
	// pool goes quiet (speedtest gap at 23:29) even that stalls behind
	// ETA/lane liveness. Measured: finish slow sid=7 ackedSeq=20/394 waited
	// 20s (94% of a stream's tail stuck). Tails resend unconditionally on
	// the 400ms timer; a mid-stream lone front keeps the old suppression.
	if (len(s.sentq) > 1 || s.finSent) && now.Sub(s.frontSince) > holeResendTO {
		n := holeResendBurst
		for i := 0; i < len(s.sentq) && n > 0; i++ {
			if s.sentq[i].lane != nil {
				frames = append(frames, s.sentq[i])
				n--
			}
		}
		s.frontSince = now
		// v0.11.6: a hole on a SINGLE-lane stream (small browser upload stuck
		// behind another stream's backlog on its one session) can never be
		// filled by retransmitting on the same FIFO session. Attach one spare
		// lane so the hole copy leaves over a different session; without this
		// such streams stalled until the 12s hole watchdog reset them (log
		// showed "retransmit ... lanes=1 total=0" loops every sweep).
		if len(frames) > 0 {
			live := 0
			for _, cn := range s.conns {
				if cn.sess != nil && cn.currentSidLocked(s) != 0 {
					live++
				}
			}
			if live < 2 {
				s.app.attachSpareLaneLocked(s)
			}
		}
	}
	s.app.mu.Unlock()

	for _, fr := range frames {
		s.resendFrame(fr)
	}
	// v0.11.16: tClose itself is a single send; if it lands on a lane whose
	// TCP is shaped to ~0 the server never sees fin (ackedSeq==lastSeq,
	// finish slow 20s, then "hole unfilled -> reset stream" aborts the
	// speedtest stream). Re-send fin every ~1.5s while unprocessed; the
	// server dedupes tClose.
	s.app.mu.Lock()
	redoFin := s.finSent && s.finSeqVal > 0 && now.Sub(s.finT) > 1500*time.Millisecond
	fv := s.finSeqVal
	s.app.mu.Unlock()
	if redoFin {
		s.sendFin(fv)
	}
	if len(frames) > 0 && os.Getenv("ZHD_DEBUG2") != "" {
		log.Printf("[client] retransmit sid=%d frames=%d lanes=%d total=%d",
			s.sid, len(frames), len(s.conns), s.resendCount.Load())
	}
	for _, cn := range dead {
		s.app.mu.Lock()
		done := s.laneBytes[cn] <= 0
		s.app.mu.Unlock()
		if done {
			s.detachLane(cn)
		}
	}
}

// attachSpareLaneLocked attaches the least-loaded session not yet carrying this
// stream so a hole has somewhere to be retransmitted. Caller holds app.mu.
func (a *clientApp) attachSpareLaneLocked(s *cStream) bool {
	if a.lanes >= a.cfg.conns {
		return false
	}
	var best *cConn
	for _, cn := range a.conns {
		if _, has := cn.members[s.id]; has {
			continue
		}
		if a.dlPool(cn) { // v0.11.13: spares only from the upload pool
			continue
		}
		if best == nil || len(cn.members) < len(best.members) {
			best = cn
		}
	}
	if best == nil {
		return false
	}
	s.conns = append(s.conns, best)
	s.attached++
	a.lanes++
	best.members[s.id] = s
	s.ensureLaneLocked(best)
	if best.sess != nil {
		best.openStreamLocked(s)
	}
	a.logf("stream %d hole: spare lane attached (lanes now %d, sid=%d)", s.id, len(s.conns), s.sid)
	return true
}

// resendFrame retransmits one pending frame on the live lane that is expected
// to drain first (same ETA score as normal pacing, excluding its current lane).
// Frames stay in the window until delivered, so duplicate copies are harmless
// (the server reassembler dedupes by seq).
func (s *cStream) resendFrame(fr *sentFrame) {
	n := int64(len(fr.payload))
	s.app.mu.Lock()
	if fr.lane == nil { // already released/acked
		s.app.mu.Unlock()
		return
	}
	old := fr.lane
	now := time.Now()
	var best *cConn
	bestETA := float64(1 << 62)
	for _, cn := range s.conns {
		if cn == old || cn.sess == nil || cn.currentSidLocked(s) == 0 {
			continue
		}
		r := s.laneRate[cn]
		backlog := int64(s.laneSent[cn]) - int64(s.laneAck[cn])
		if backlog < 0 {
			backlog = 0
		}
		// Idle lanes stay eligible (see pickLaneSendLocked): only a lane with
		// outstanding frames and no delivery progress is suspect.
		if backlog > 0 && now.Sub(s.laneT[cn]) > 400*time.Millisecond {
			r = laneRateMin
		}
		if r < laneRateMin {
			r = laneRateMin
		}
		eta := (float64(backlog) + 1) / r
		if eta < bestETA {
			best, bestETA = cn, eta
		}
	}
	if best == nil {
		s.app.mu.Unlock()
		return
	}
	s.laneBytes[old] -= n
	s.laneBytes[best] += n
	s.laneSent[best]++
	best.lastAct.Store(time.Now().UnixNano())
	fr.lane = best
	s.app.mu.Unlock()
	if best.sess.trySendData(Frame{Typ: tData, SID: s.sid, Payload: fr.payload}) {
		s.resendCount.Add(1)
		return
	}
	s.app.mu.Lock()
	s.laneBytes[best] -= n
	s.laneBytes[old] += n
	s.laneSent[best]--
	fr.lane = old
	s.app.mu.Unlock()
}

// detachLane removes a dead spread lane from the stream (bookkeeping only; the
// server already dropped that session's stream membership).
func (s *cStream) detachLane(cn *cConn) {
	s.app.mu.Lock()
	defer s.app.mu.Unlock()
	if cn == s.home {
		return
	}
	for i, c := range s.conns {
		if c == cn {
			s.conns = append(s.conns[:i], s.conns[i+1:]...)
			delete(cn.members, s.id)
			s.attached--
			if s.attached < 0 {
				s.attached = 0
			}
			s.app.lanes--
			if s.app.lanes < 0 {
				s.app.lanes = 0
			}
			delete(s.laneAck, cn)
			delete(s.laneSent, cn)
			delete(s.laneMigT, cn)
			delete(s.laneBytes, cn)
			delete(s.laneRate, cn)
			delete(s.laneCnt, cn)
			delete(s.laneT, cn)
			s.app.logf("stream %d lane slot %d lost: detached (%d lanes left)", s.id, cn.idx, len(s.conns))
			return
		}
	}
}

// anyLiveLane reports whether at least one attached slot has a live
// session+sid for this stream.
func (s *cStream) anyLiveLane() bool {
	s.app.mu.Lock()
	defer s.app.mu.Unlock()
	for _, cn := range s.conns {
		if cn.sess != nil && cn.currentSidLocked(s) != 0 {
			return true
		}
	}
	return false
}

// waitOpenAny blocks until at least one attached slot has a live session+sid
// for this stream. Returns false if the stream is done.
func (s *cStream) waitOpenAny() bool {
	for {
		if s.anyLiveLane() {
			return true
		}
		select {
		case <-s.done:
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// trySpread grows the set of slots carrying this stream so a single bulk
// upload fills multiple per-flow shaping buckets. The lane budget is
// AUTO: min(cfg.spread, conns / live bulk streams) 闂?a lone bulk stream
// may fan across all slots, but when a speedtest floods 15+ big streams
// each stream keeps a dedicated lane (spreading would just slice every
// lane into unusable crumbs). Adds one slot per spreadThreshold bytes sent.
// detachLastDrainedLocked trims one spread lane (newest-first) that has no
// pending bytes left (everything it carried was delivered). Pure client-side
// op; the server simply stops seeing frames for this sid on that session.
func (s *cStream) detachLastDrainedLocked() bool {
	for i := len(s.conns) - 1; i >= 1; i-- {
		cn := s.conns[i]
		if s.laneBytes[cn] <= 0 {
			delete(cn.members, s.id)
			s.conns = append(s.conns[:i], s.conns[i+1:]...)
			s.attached--
			if s.attached < 0 {
				s.attached = 0
			}
			s.app.lanes--
			if s.app.lanes < 0 {
				s.app.lanes = 0
			}
			delete(s.laneAck, cn)
			delete(s.laneSent, cn)
			delete(s.laneMigT, cn)
			delete(s.laneBytes, cn)
			delete(s.laneRate, cn)
			delete(s.laneCnt, cn)
			delete(s.laneT, cn)
			s.app.logf("stream %d rebalance: detach slot %d (lanes now %d)", s.id, cn.idx, s.app.lanes)
			return true
		}
	}
	return false
}

// rebalanceLocked enforces the fair per-stream lane budget against EARLY
// HOGGING: a stream that spread while few bulk streams existed keeps its
// lanes when new bulk streams arrive, starving them (latecomers find the
// global lane pool exhausted). Trim everyone toward the current budget.
func (a *clientApp) rebalanceLocked(budget int) {
	for _, st := range a.streams {
		for st.bulk && len(st.conns) > budget {
			if !st.detachLastDrainedLocked() {
				break
			}
		}
	}
}

func (s *cStream) trySpread(sent int64) {
	s.app.mu.Lock()
	marked := false
	if sent >= spreadThreshold && !s.bulk {
		s.bulk = true
		s.app.bulk++
		marked = true
	}
	budget := s.app.cfg.spread
	if s.app.bulk > 0 {
		if b := s.app.cfg.conns / s.app.bulk; b < budget {
			budget = b
		}
	}
	if budget < 1 {
		budget = 1
	}
	if marked {
		s.app.rebalanceLocked(budget) // fair share just shrank: trim hogs immediately
	}
	if len(s.conns) < budget && s.app.lanes >= s.app.cfg.conns {
		s.app.rebalanceLocked(budget) // pool saturated: take from over-budget streams first
	}
	if sent >= spreadThreshold*int64(len(s.conns)) && len(s.conns) < budget && s.app.lanes < s.app.cfg.conns {
		// pick the least-loaded slot not yet carrying this stream
		var best *cConn
		for _, cn := range s.app.conns {
			if _, has := cn.members[s.id]; has {
				continue
			}
			if s.app.dlPool(cn) { // v0.11.13: spread lanes only from upload pool
				continue
			}
			if best == nil || len(cn.members) < len(best.members) {
				best = cn
			}
		}
		if best != nil {
			s.conns = append(s.conns, best)
			s.attached++
			s.app.lanes++
			best.members[s.id] = s
			s.ensureLaneLocked(best)
			if best.sess != nil {
				best.openStreamLocked(s)
			}
			s.app.logf("stream %d spread to %d slots (sid=%d, sent=%d)", s.id, len(s.conns), s.sid, sent)
		}
	}
	s.app.mu.Unlock()
}

// sendFin sends a half-close carrying finSeq (the next seq value) over every
// attached slot that has a live session, so the server can finish reorder and
// CloseWrite the backend.
func (s *cStream) sendFin(finSeq uint32) {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, finSeq)
	// Collect targets under app.mu, but enqueue OUTSIDE the lock: a blocking
	// send on a congested lane used to hold app.mu (and with it every session
	// reader / ack handler) for the socket write timeout.
	s.app.mu.Lock()
	s.finSent = true // v0.11.15: tails resend without the "frames behind" suppression
	s.finSeqVal = finSeq
	s.finT = time.Now() // v0.11.16: tClose re-send timer (a fin lost on a dead lane used to wedge completion 20s until the server's hole watchdog reset the stream)
	targets := make([]*session, 0, len(s.conns))
	for _, cn := range s.conns {
		if cn.sess != nil && cn.currentSidLocked(s) != 0 {
			targets = append(targets, cn.sess)
		}
	}
	s.app.mu.Unlock()
	for _, sess := range targets {
		sess.sendOrderedTimeout(Frame{Typ: tClose, SID: s.sid, Payload: payload}, 2*time.Second)
	}
}

// pumpUp: local -> remote. One goroutine per stream for its whole life;
// chunks are sequence-numbered and fanned out across attached sessions.
// ---------------- zhv5 bucket upload (client) ----------------

const (
	bucketMin   = 512 << 10 // min bucket range size
	bucketMaxK  = 16        // max concurrent ranges per request
	bucketMaxCL = 512 << 20 // don't bucket bodies above this
)

var bktNextID uint32 = 1 << 30 // member stream ids (kept clear of socks-side ids)

var crlfcrlf = []byte("\r\n\r\n")

// pickUpConnLocked picks the least-loaded upload-pool session for a member.
// Caller holds a.mu.
func (a *clientApp) pickUpConnLocked() *cConn {
	n := len(a.conns)
	start := int(a.rr % uint32(n))
	a.rr++
	var best, bestAny *cConn
	for i := 0; i < n; i++ {
		cn := a.conns[(start+i)%n]
		if a.dlPool(cn) {
			continue
		}
		if bestAny == nil || len(cn.members) < len(bestAny.members) {
			bestAny = cn
		}
		if cn.sess == nil { // reconnecting: least-preferred retry home
			continue
		}
		if best == nil || len(cn.members) < len(best.members) {
			best = cn
		}
	}
	if best != nil {
		return best
	}
	if bestAny != nil {
		return bestAny
	}
	return a.conns[start] // no split pools: any session may carry a member
}

// peekHTTPCL buffers the request headers of a keep-alive connection and
// decides whether the body is worth bucketing (POST/PUT/PATCH, Content-Length
// between 2*bucketMin and bucketMaxCL). Returns the raw bytes read, the
// header end offset, Content-Length and eligibility.
// httpReqPrefix reports whether b plausibly begins an HTTP request line.
// Used by peekHTTPCL so it only buffers for a Content-Length sniff on real
// HTTP; TLS records (HTTPS ClientHello) and other binary payloads return
// false and are forwarded upstream immediately.
func httpReqPrefix(b []byte) bool {
	for _, m := range []string{"GET ", "POST ", "PUT ", "PATCH ", "HEAD ", "OPTIONS ", "DELETE ", "CONNECT ", "TRACE "} {
		if bytes.HasPrefix(b, []byte(m)) {
			return true
		}
	}
	// Not enough bytes yet to tell (b shorter than the longest method token):
	// keep sniffing only while b could still become a method token.
	if len(b) < 8 {
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "HEAD", "OPTIONS", "DELETE", "CONNECT", "TRACE"} {
			if strings.HasPrefix(m, string(b)) {
				return true
			}
		}
	}
	return false
}

func (s *cStream) peekHTTPCL(buf []byte) (raw []byte, hdrEnd int, cl uint64, ok bool) {
	// We only buffer to sniff an HTTP Content-Length while the buffered
	// prefix still looks like an HTTP request line. A TLS handshake (HTTPS)
	// or any non-HTTP first payload must NOT stall here: the ClientHello has
	// no CRLF CRLF, and holding it from the backend while we keep Read-ing
	// for a header that will never come deadlocks the handshake (the peer
	// won't send more until it gets the ClientHello) until the read deadline
	// — which made every HTTPS connection time out (~10-15 s). Non-HTTP and
	// HTTP split across slow segments degrade to the legacy path, still
	// correct, just not bucketed.
	for len(raw) < 32*1024 {
		if idx := bytes.Index(raw, crlfcrlf); idx >= 0 {
			hdrEnd = idx + 4
			break
		}
		if len(raw) > 0 && !httpReqPrefix(raw) {
			return raw, 0, 0, false
		}
		s.local.SetReadDeadline(deadline(300 * time.Millisecond))
		n, err := s.local.Read(buf)
		if n > 0 {
			raw = append(raw, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	if hdrEnd == 0 {
		return
	}
	hdr := raw[:hdrEnd]
	sp := bytes.IndexByte(hdr, ' ')
	if sp <= 0 {
		return
	}
	switch string(hdr[:sp]) {
	case "POST", "PUT", "PATCH":
	default:
		return
	}
	for _, line := range bytes.Split(hdr, []byte("\r\n")) {
		if len(line) >= 15 && bytes.EqualFold(line[:15], []byte("Content-Length:")) {
			if v, err := strconv.ParseUint(strings.TrimSpace(string(line[15:])), 10, 64); err == nil {
				cl = v
			}
			break
		}
	}
	if cl >= 2*bucketMin && cl <= bucketMaxCL {
		ok = true
	}
	return
}

// bktSchedule enqueues a range and starts as many as the active-member cap
// allows (queue drained further in bktDone as members finish).
func (a *clientApp) bktSchedule(leader *cStream, br *bktRange) {
	g := leader.grp
	if g == nil {
		return
	}
	g.mu.Lock()
	g.queue = append(g.queue, br)
	var start []*bktRange
	for g.active < g.maxActive && len(g.queue) > 0 {
		r := g.queue[0]
		g.queue = g.queue[1:]
		g.active++
		start = append(start, r)
	}
	g.mu.Unlock()
	for _, r := range start {
		a.bktLaunch(leader, r)
	}
}

// bktLaunch opens one member stream carrying [off,off+ln) of the leader's
// body over the upload pool, and pumps it from memory.
func (a *clientApp) bktLaunch(leader *cStream, br *bktRange) {
	a.mu.Lock()
	sid := a.allocSidLocked()
	if sid == 0 {
		a.mu.Unlock()
		a.bktDone(leader, br, false)
		return
	}
	cn := a.pickUpConnLocked()
	m := &cStream{
		app:  a,
		id:   atomic.AddUint32(&bktNextID, 1),
		sid:  sid,
		home: cn, conns: []*cConn{cn},
		rq:   make(chan []byte, 1),
		done: make(chan struct{}),
		bkt:  &bktOpen{leaderSID: leader.sid, off: br.off, ln: br.ln, final: br.final},
	}
	a.streams[m.id] = m
	cn.members[m.id] = m
	m.ensureLaneLocked(cn)
	if cn.sess != nil {
		cn.openStreamLocked(m)
	}
	a.mu.Unlock()
	go m.pumpMember(leader, br)
}

func (m *cStream) pumpMember(leader *cStream, br *bktRange) {
	var seq uint32
	var i int
	for i < len(br.data) {
		n := maxPayload
		if len(br.data)-i < n {
			n = len(br.data) - i
		}
		seq++
		payload := make([]byte, n+4)
		binary.BigEndian.PutUint32(payload[:4], seq)
		copy(payload[4:], br.data[i:i+n])
		if !m.sendFrame(seq, payload) {
			m.app.bktDone(leader, br, false)
			m.teardown()
			return
		}
		i += n
		m.trySpread(int64(i))
	}
	m.sendFin(seq + 1)
	if m.app.cfg.verbose {
		m.app.logf("BUCKET member done sid=%d off=%d len=%d seq=%d", m.sid, br.off, br.ln, seq)
	}
	deadlineT := time.Now().Add(90 * time.Second)
	ok := false
	var acked uint32
	for {
		m.app.mu.Lock()
		acked = m.ackedSeq
		rs := m.reset
		m.app.mu.Unlock()
		if acked >= seq {
			ok = true
			break
		}
		if rs {
			break
		}
		select {
		case <-m.done:
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadlineT) {
			break
		}
	}
	if !ok {
		m.app.logf("bucket member fail sid=%d off=%d len=%d acked=%d/%d", m.sid, br.off, br.ln, acked, seq)
	}
	m.app.bktDone(leader, br, ok)
	m.teardown()
}

// bktDone records a member outcome, frees its active slot, re-prioritizes
// failed ranges (retry same range, then split it across two smaller ones),
// and drains the queue up to the active cap so retries get send-window credit
// instead of starve-behind-the-wall failures.
func (a *clientApp) bktDone(leader *cStream, br *bktRange, ok bool) {
	g := leader.grp
	if g == nil {
		return
	}
	var start []*bktRange
	g.mu.Lock()
	if g.active > 0 {
		g.active--
	}
	if ok {
		g.left--
	} else {
		br.retries++
		// Under session-recycle churn a range's member can be reset several
		// times in quick succession; failing the whole upload on the 3rd is
		// far too eager. Retry generously and re-split so ranges shrink until
		// they fit surviving lanes; only a range that shrinks below half a
		// bucket AND exhausts its retries fails the upload.
		switch {
		case br.retries >= 8:
			if !g.failed {
				a.logf("bucket range dead leader=%d off=%d len=%d -> upload fails", leader.sid, br.off, br.ln)
			}
			g.failed = true
		case br.ln >= bucketMin/2 && (br.retries == 2 || br.retries == 4 || br.retries == 6):
			h := br.ln / 2
			g.left++ // one range replaced by two smaller ones
			g.queue = append(g.queue,
				&bktRange{off: br.off, ln: h, data: br.data[:h]},
				&bktRange{off: br.off + h, ln: br.ln - h, data: br.data[h:], final: br.final})
		default:
			g.queue = append(g.queue, br)
		}
	}
	for g.active < g.maxActive && len(g.queue) > 0 {
		r := g.queue[0]
		g.queue = g.queue[1:]
		g.active++
		start = append(start, r)
	}
	g.mu.Unlock()
	for _, r := range start {
		a.bktLaunch(leader, r)
	}
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

// pumpUpBuckets handles one bucketed upload on a connection: the leader
// carries only the request header (and the downlink); the body [0,cl) is
// split into byte ranges, each carried by an independent member stream over
// the upload pool. A stalled/reset range kills only that range (retried, then
// split) while the others keep flowing. Once every range is delivered the
// leader sends fin (backend now sees the complete body); the stream is then
// kept alive draining the local socket until EOF so the response can flow
// back on the downlink, exactly like the legacy single-request tail.
func (s *cStream) pumpUpBuckets(raw []byte, hdrEnd int, cl uint64) {
	a := s.app
	var seq uint32
	t0 := time.Now()
	seq++
	payload := make([]byte, hdrEnd+4)
	binary.BigEndian.PutUint32(payload[:4], seq)
	copy(payload[4:], raw[:hdrEnd])
	if !s.sendFrame(seq, payload) {
		s.teardown()
		return
	}
	K := int((cl + bucketMin - 1) / bucketMin)
	if K > bucketMaxK {
		K = bucketMaxK
	}
	if K < 1 {
		K = 1
	}
	B := (cl + uint64(K) - 1) / uint64(K)
	g := &bgrp{total: cl, wake: make(chan struct{}, 1)}
	upLanes := a.dlStart
	if !a.split {
		upLanes = len(a.conns)
	}
	if upLanes < 1 {
		upLanes = 1
	}
	g.maxActive = 2 * upLanes
	if g.maxActive > K {
		g.maxActive = K
	}
	s.grp = g
	a.logf("bucket up sid=%d cl=%d ranges=%d size=%d", s.sid, cl, K, B)
	var off uint64
	data := raw[hdrEnd:]
	readFail := false
	for off < cl && !readFail {
		ln := B
		if cl-off < ln {
			ln = cl - off
		}
		for uint64(len(data)) < ln {
			if !s.waitOpenAny() {
				readFail = true
				break
			}
			s.local.SetReadDeadline(deadline(2 * time.Minute))
			b := make([]byte, maxPayload)
			n, err := s.local.Read(b)
			data = append(data, b[:n]...)
			if os.Getenv("ZHD_BUCKET") != "" {
				a.logf("BUCKET read sid=%d off=%d got=%d have=%d err=%v", s.sid, off, n, len(data), err)
			}
			if err != nil {
				readFail = true
				break
			}
		}
		if readFail || uint64(len(data)) < ln {
			break
		}
		br := &bktRange{off: off, ln: ln, data: data[:ln], final: off+ln == cl}
		g.mu.Lock()
		g.left++
		g.mu.Unlock()
		a.bktSchedule(s, br)
		data = data[ln:] // carry any overshoot into the next range
		off += ln
	}
	if readFail && off < cl {
		a.logf("bucket up read fail sid=%d at off=%d/%d", s.sid, off, cl)
		s.teardown()
		return
	}
	// Wait for every range to be delivered to the server (members run in
	// parallel; a failed range is retried/split inside bktDone).
	for {
		g.mu.Lock()
		done := g.left == 0
		failed := g.failed
		g.mu.Unlock()
		if done || failed {
			break
		}
		select {
		case <-s.done:
			s.teardown()
			return
		case <-g.wake:
		case <-time.After(2 * time.Second):
		}
	}
	if g.failed {
		s.teardown()
		return
	}
	if a.cfg.verbose {
		log.Printf("[client] bucket done sid=%d cl=%d dur=%s", s.sid, cl, time.Since(t0).Round(time.Millisecond))
	}
	// All body bytes are at the backend in order. Close the upload so a
	// backend that keys on EOF sees the complete request, then keep the
	// stream alive draining local until EOF so the response flows back.
	s.sendFin(seq + 1)
	buf := make([]byte, maxPayload)
	for {
		if !s.waitOpenAny() {
			return
		}
		s.local.SetReadDeadline(deadline(2 * time.Minute))
		n, err := s.local.Read(buf)
		if n > 0 {
			seq++
			p := make([]byte, n+4)
			binary.BigEndian.PutUint32(p[:4], seq)
			copy(p[4:], buf[:n])
			if !s.sendFrame(seq, p) {
				return
			}
		}
		if err != nil {
			break
		}
	}
	s.pumpUpTailEOF(&seq, t0, int64(cl), int64(K))
}

// pumpUpTailEOF: the fin/finish/teardown tail shared with the bucketed path
// after the last bucket group completes.
func (s *cStream) pumpUpTailEOF(seq *uint32, t0 time.Time, sent, frames int64) {
	if s.app.cfg.verbose && sent >= spreadThreshold {
		avg := int64(0)
		if frames > 0 {
			avg = sent / frames
		}
		log.Printf("[client] pumpUp done sid=%d sent=%d frames=%d avg=%dB dur=%s",
			s.sid, sent, frames, avg, time.Since(t0).Round(time.Millisecond))
	}
	s.sendFin(*seq + 1)
	deadlineT := time.Now().Add(20 * time.Second)
	waitStart := time.Now()
	var acked uint32
	for {
		s.app.mu.Lock()
		acked = s.ackedSeq
		delivered := acked >= *seq
		s.app.mu.Unlock()
		if delivered || time.Now().After(deadlineT) {
			break
		}
		select {
		case <-s.done:
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
	if waited := time.Since(waitStart); waited > 2*time.Second && s.app.cfg.verbose {
		log.Printf("[client] pumpUp finish slow sid=%d lastSeq=%d ackedSeq=%d waited=%v",
			s.sid, *seq, acked, waited.Round(time.Millisecond))
	}
	s.teardown()
}

func (s *cStream) pumpUp() {
	buf := make([]byte, maxPayload)
	var sent int64
	var frames int64
	var seq uint32 // next sequence number (1-based)
	dbgOn := os.Getenv("ZHD_DEBUG2") != ""
	t0 := time.Now()
	dbgLast := time.Now()
	// zhv5 bucket upload: if this is a POST/PUT/PATCH with a sizable
	// Content-Length, the body is split into up to bucketMaxK byte ranges,
	// each carried by an independent member stream (own window, own server
	// watchdog, retry/split on failure). The leader keeps headers, ordering
	// and the downlink. Small requests and non-HTTP streams are untouched.
	if !s.noPump {
		raw, hdrEnd, cl, bk := s.peekHTTPCL(buf)
		if bk {
			s.pumpUpBuckets(raw, hdrEnd, cl)
			return
		}
		if len(raw) > 0 {
			sent += int64(len(raw))
			seq++
			payload := make([]byte, len(raw)+4)
			binary.BigEndian.PutUint32(payload[:4], seq)
			copy(payload[4:], raw)
			if !s.sendFrame(seq, payload) {
				return
			}
			s.trySpread(sent)
		}
	}
	for {
		// wait until this stream is OPEN on at least one live session
		if !s.waitOpenAny() {
			if s.app.cfg.verbose {
				log.Printf("[client] pumpUp exit stream=%d sent=%d (session gone)", s.id, sent)
			}
			return
		}
		s.local.SetReadDeadline(deadline(2 * time.Minute))
		n, err := s.local.Read(buf)
		if n > 0 {
			sent += int64(n)
			seq++
			frames++
			payload := make([]byte, n+4)
			binary.BigEndian.PutUint32(payload[:4], seq)
			copy(payload[4:], buf[:n])
			// sendFrame applies the delivered-based window (blocks while full)
			// and keeps the frame for retransmission until the server delivered
			// it in order. Data is never dropped: a session-reconnect window
			// just delays the frame until a lane is back.
			if !s.sendFrame(seq, payload) {
				return
			}
			s.trySpread(sent)
			if dbgOn && time.Since(dbgLast) > 2*time.Second {
				dbgLast = time.Now()
				s.dbgLog(sent, frames, t0)
			}
		}
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue // idle read timeout, keep pumping
			}
			// EOF / error: half-close (finSeq = next seq) then teardown.
			// EOF is the NORMAL end of an app stream (speedtests open
			// hundreds of tiny ones); only log abnormal terminations.
			if s.app.cfg.verbose && !streamChurn(err) {
				log.Printf("[client] pumpUp err stream=%d sent=%d err=%v", s.id, sent, err)
			}
			if s.app.cfg.verbose && sent >= spreadThreshold {
				avg := int64(0)
				if frames > 0 {
					avg = sent / frames
				}
				log.Printf("[client] pumpUp done sid=%d sent=%d frames=%d avg=%dB dur=%s",
					s.sid, sent, frames, avg, time.Since(t0).Round(time.Millisecond))
			}
			s.sendFin(seq + 1)
			// Wait (bounded) for the tail to be DELIVERED before tearing down:
			// teardown releases pending frames, so closing immediately would
			// silently drop the end of the upload. The server also reports
			// delivery on the FIN path, so this normally settles in one RTT.
			deadline := time.Now().Add(20 * time.Second)
			waitStart := time.Now()
			var acked uint32
			for {
				s.app.mu.Lock()
				acked = s.ackedSeq
				delivered := acked >= seq
				s.app.mu.Unlock()
				if delivered || time.Now().After(deadline) {
					break
				}
				select {
				case <-s.done:
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
			if waited := time.Since(waitStart); waited > 2*time.Second && s.app.cfg.verbose {
				log.Printf("[client] pumpUp finish slow sid=%d lastSeq=%d ackedSeq=%d waited=%v",
					s.sid, seq, acked, waited.Round(time.Millisecond))
			}
			s.teardown()
			return
		}
	}
}

// pumpDown: remote queue -> local socket.
func (s *cStream) pumpDown() {
	for {
		select {
		case chunk := <-s.rq:
			if chunk == nil {
				s.closeWriteLocal()
				return
			}
			s.local.SetWriteDeadline(deadline(localWriteTO))
			w0 := time.Now()
			_, werr := s.local.Write(chunk)
			if d := time.Since(w0); d > 2*time.Second && s.app.cfg.verbose {
				log.Printf("[client] pumpDown slow sid=%d len=%d took=%v", s.sid, len(chunk), d.Round(time.Millisecond))
			}
			if werr != nil {
				s.app.mu.Lock()
				sess := s.home.sess
				sid := s.sid
				s.app.mu.Unlock()
				if sess != nil && sid != 0 {
					sess.sendCtrl(Frame{Typ: tReset, SID: sid})
				}
				s.teardown()
				return
			}
		case <-s.done:
			return
		}
	}
}

// streamChurn reports errors that are the normal noise of apps (especially
// speedtest probe floods) aborting streams: clean EOF, our own teardown
// racing a pending read (ErrClosed), and app-side RSTs (ECONNRESET).
func streamChurn(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "forcibly closed")
}

func sleepSelect(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
