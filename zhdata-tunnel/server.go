package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type serverConfig struct {
	listen     string
	usersPath  string
	backend    string // "direct" or socks addr like 127.0.0.1:12345
	maxClients int
	decoy      string // optional host:port for fallback splice of non-protocol traffic
	verbose    bool
}

type serverApp struct {
	cfg serverConfig
	ks  *keyStore

	lns    []net.Listener
	mu     sync.Mutex
	conns  map[string]*sSession // uuid(+slot) -> current session
	active int
	estab  atomic.Uint64 // sessions established (for tests/observability)

	stmMu   sync.Mutex          // guards streams map + sStream.members + groups map
	streams map[string]*sStream // "uuid#sid" -> stream (GLOBAL per uuid: one app stream may span sessions)
	groups  map[string]*upGroup // zhv5: leader streamKey -> bucket reassembly group

	failMu sync.Mutex
	fails  map[string]*failWin

	dnsMu sync.Mutex // v0.9.1: multi-resolver fallback + cache (system 8.8.8.8 answers NXDOMAIN for many CN/CDN hosts under poison/throttle)
	dns   map[string]dnsEntry

	rstMu sync.Mutex       // v0.9.1: suppress duplicate tReset for late frames of already-dead sids (frame-storm amplification: one dead stream x hundreds of in-flight frames)
	rst   map[string]int64 // streamKey -> unixnano of last reset sent

	stopCh  chan struct{}
	stopped atomic.Bool
}

type dnsEntry struct {
	ips  []string
	expA int64 // unixnano expiry
}

type sSession struct {
	uuid string
	sess *session

	smu     sync.Mutex
	streams map[uint16]*sStream // membership view: sids carried by this session
	closed  bool
}

// sStream is one proxied TCP connection. v0.4: its UPLOAD frames may arrive
// on ANY session the client attached (in-stream spread); each DATA payload
// carries a 4-byte sequence number and is reassembled to order before being
// replayed to the backend. Downlink still flows through the owner session.
type sStream struct {
	owner *sSession
	uuid  string
	sid   uint16

	mu          sync.Mutex // reassembler + backend fields
	backend     net.Conn
	upq         chan []byte   // async upstream write queue (dispatch never blocks on a slow backend)
	dialDone    chan struct{} // closed when backend is set and pre-dial frames flushed
	rx          reassembler
	rxWake      chan struct{}               // fires when stream state advances (wakes parked dispatch lanes)
	laneHi      map[*sSession]*laneAckState // per-member upload frame counts (tAck credit)
	written     int64                       // forensic: bytes written upstream for this stream
	lastParkLog time.Time                   // rate-limit park logging (st.mu)
	holeSince   time.Time                   // first moment rx.next made no progress while rx.buf had data (st.mu); v0.11 retransmit watchdog

	// zhv5 bucket upload. Member streams (grpKey != "") carry one byte range
	// of the leader's body: ordered bytes accumulate in rangeBuf and are
	// handed to the group on member completion; they have no backend of their
	// own. Leader streams hold post-header upload chunks in holdBuf while a
	// group is open, so backend writes stay in byte order.
	grpKey   string
	grpOff   uint64
	grpLen   uint64
	grpFinal bool
	grp      *upGroup // leader: current bucket group (st.mu)
	grpDone  bool     // leader: current group finished (st.mu)
	rangeBuf []byte
	hold     bool
	holdBuf  [][]byte
	// v0.11.4: downlink flow control. dlPending = bytes enqueued toward the
	// owner session but not yet handed to the socket (guarded by st.mu). This
	// keeps at most ~1MB of a stream in the tunnel+kernel, so a LAN-speed
	// backend cannot bury a WAN TCP under a 50MB burst (bufferbloat freeze).
	dlPending int64
	dlWake    chan struct{} // fired when dlPending frees or the stream closes
	closed    bool
	finDone   bool // CloseWrite already issued to backend

	members map[*sSession]bool // sessions carrying this stream's frames (guarded by stmMu)
}

// reassembler orders seq-tagged upload frames within one stream.
// next: next frame number to emit to the backend. buf holds out-of-order
// frames waiting for a hole to fill; bufN bounds the reorder window.
type reassembler struct {
	next uint32 // next sequence number to emit (first frame = 1)
	buf  map[uint32][]byte
	bufN int
	fin  uint32 // finSeq from tClose (0 = not received)
}

type failWin struct {
	n    int
	from time.Time
}

func newServerApp(cfg serverConfig) (*serverApp, error) {
	ks, err := newKeyStore(cfg.usersPath)
	if err != nil {
		return nil, err
	}
	if cfg.maxClients <= 0 {
		cfg.maxClients = 100
	}
	lns := []net.Listener(nil)
	for _, spec := range splitCSV(cfg.listen) {
		ln, err := net.Listen("tcp", spec)
		if err != nil {
			for _, l := range lns {
				l.Close()
			}
			return nil, err
		}
		lns = append(lns, ln)
	}
	a := &serverApp{
		cfg:     cfg,
		ks:      ks,
		lns:     lns,
		conns:   map[string]*sSession{},
		streams: map[string]*sStream{},
		groups:  map[string]*upGroup{},
		fails:   map[string]*failWin{},
		dns:     map[string]dnsEntry{},
		rst:     map[string]int64{},
		stopCh:  make(chan struct{}),
	}
	addrs := make([]string, 0, len(lns))
	for _, ln := range lns {
		addrs = append(addrs, ln.Addr().String())
	}
	log.Printf("[server] listening on %v, users=%d, backend=%s, max-clients=%d",
		addrs, len(ks.snapshot()), cfg.backend, cfg.maxClients)
	for _, ln := range lns {
		go a.acceptLoop(ln)
	}
	go a.watchKeys()
	go a.gapWatchLoop()
	return a, nil
}

// gapWatchLoop resets upload streams whose reorder hole was not filled by the
// client's retransmission within holeTimeout (both copies of some frames were
// lost, or the peer vanished). Before v0.11 this was the 15s reader park, which
// froze every stream on the session; now a stuck stream dies alone.
func (a *serverApp) gapWatchLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.stopCh:
			return
		case <-t.C:
		}
		now := time.Now()
		var stuck []*sStream
		type holeInfo struct {
			next, minBuf uint32
			bufN         int
			members      int
		}
		hinfo := map[*sStream]holeInfo{}
		a.stmMu.Lock()
		for _, st := range a.streams {
			st.mu.Lock()
			hit := !st.holeSince.IsZero() && now.Sub(st.holeSince) > holeTimeout && st.rx.bufN > 0 && !st.closed
			if hit {
				hi := holeInfo{next: st.rx.next, bufN: st.rx.bufN, members: len(st.members)}
				for sq := range st.rx.buf {
					if hi.minBuf == 0 || sq < hi.minBuf {
						hi.minBuf = sq
					}
				}
				hinfo[st] = hi
				st.holeSince = time.Time{} // reset it once
			}
			st.mu.Unlock()
			if hit {
				stuck = append(stuck, st)
			}
		}
		a.stmMu.Unlock()
		for _, st := range stuck {
			hi := hinfo[st]
			a.logf("reorder hole unfilled sid=%d (>%v no retransmit) next=%d minBuf=%d bufN=%d members=%d -> reset stream",
				st.sid, holeTimeout, hi.next, hi.minBuf, hi.bufN, hi.members)
			a.closeStream(st, true)
		}
	}
}

// splitCSV splits a comma-separated list, trimming spaces, dropping empties.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// watchKeys hot-reloads the user key sources every keyReloadInterval.
func (a *serverApp) watchKeys() {
	t := time.NewTicker(keyReloadInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			a.ks.reloadIfChanged()
		case <-a.stopCh:
			return
		}
	}
}

func (a *serverApp) Addr() string { return a.lns[0].Addr().String() }

// Addrs lists all bound listen addresses.
func (a *serverApp) Addrs() []string {
	out := make([]string, 0, len(a.lns))
	for _, ln := range a.lns {
		out = append(out, ln.Addr().String())
	}
	return out
}

func (a *serverApp) Close() {
	if a.stopped.CompareAndSwap(false, true) {
		close(a.stopCh)
		for _, ln := range a.lns {
			ln.Close()
		}
		a.mu.Lock()
		for _, sh := range a.conns {
			sh.closeAll(a)
		}
		a.mu.Unlock()
	}
}

func (a *serverApp) logf(format string, v ...any) {
	if a.cfg.verbose {
		log.Printf("[server] "+format, v...)
	}
}

func (a *serverApp) acceptLoop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if !a.stopped.Load() {
				log.Printf("[server] accept: %v", err)
			}
			return
		}
		go a.handle(c)
	}
}

func (a *serverApp) handle(c net.Conn) {
	tuneTCP(c)
	raddr, _, _ := net.SplitHostPort(c.RemoteAddr().String())
	c.SetDeadline(deadline(handshakeTimeout))
	buf := make([]byte, handshakeLen)
	if _, err := io.ReadFull(c, buf); err != nil {
		a.logf("handshake read from %s: %v", raddr, err)
		c.Close()
		return
	}
	c.SetDeadline(noDeadline())
	salt, enc := buf[:16], buf[16:48]

	uuid, rx := "", cipherStream(nil)
	var slot byte
	for _, u := range a.ks.snapshot() {
		ctr := newCTR(u, salt)
		pt := make([]byte, 32)
		ctr.XORKeyStream(pt, enc)
		if string(pt[:4]) == magic || string(pt[:4]) == "zhv4" { // zhv5 = bucket upload; zhv4 stays compatible (never opens bucket members)
			uuid, rx = u, ctr
			slot = pt[4] // handshake slot byte (0 = single-session legacy)
			break
		}
	}
	if uuid == "" {
		if !a.allowFail(raddr) {
			a.logf("handshake rate limited from %s", raddr)
			c.Close()
			return
		}
		if a.cfg.decoy != "" && looksHTTP(buf[:8]) {
			a.spliceDecoy(c, buf)
			return
		}
		a.logf("unknown handshake from %s", raddr)
		c.Close()
		return
	}

	// takeover + max clients. Sessions are keyed per (uuid,slot): a client
	// with -conns N holds N independent sessions that never take each other
	// over; slot 0 keeps the legacy single-session semantics.
	if slot >= maxSlots {
		slot = 0
	}
	connKey := uuid
	if slot > 0 {
		connKey = uuid + "#" + string(rune('0'+slot))
	}
	a.mu.Lock()
	if a.active >= a.cfg.maxClients {
		a.mu.Unlock()
		log.Printf("[server] max clients reached, rejecting %s from %s", uuid, raddr)
		c.Close()
		return
	}
	old := a.conns[connKey]
	a.active++
	a.mu.Unlock()

	if old != nil {
		log.Printf("[server] takeover uuid=%s slot=%d (old conn from different client)", short(uuid), slot)
		old.sess.sendCtrl(Frame{Typ: tGoaway})
		go func() {
			time.Sleep(300 * time.Millisecond)
			old.closeAll(a)
		}()
	}

	sess := newSession("srv:"+short(uuid), c, newCTR(uuid, salt), rx)
	sh := &sSession{uuid: uuid, sess: sess, streams: map[uint16]*sStream{}}
	a.mu.Lock()
	a.conns[connKey] = sh
	a.mu.Unlock()
	a.estab.Add(1)
	log.Printf("[server] session established uuid=%s slot=%d from %s", short(uuid), slot, raddr)

	err := sess.run(func(f *Frame) { a.dispatch(sh, f) })
	sh.closeAll(a)
	a.mu.Lock()
	a.active--
	if a.conns[connKey] == sh {
		delete(a.conns, connKey)
	}
	a.mu.Unlock()
	log.Printf("[server] session ended uuid=%s slot=%d: %v", short(uuid), slot, err)
}

func streamKey(uuid string, sid uint16) string {
	return uuid + "#" + strconv.FormatUint(uint64(sid), 10)
}

// wakeAll hands a wake token to every parked dispatch lane of this stream
// (buffered chan sized maxSlots = one token per possible parked session;
// a single token would thunder-herd and re-park, deadlocking the lane set).
func (st *sStream) wakeAll() {
	for i := 0; i < cap(st.rxWake); i++ {
		select {
		case st.rxWake <- struct{}{}:
		default:
			return
		}
	}
}

// laneAckState tracks how many upload frames one session has delivered for a
// stream. tAck payload = count | deliveredPrefix (v0.11):
//   - count: arrivals on this lane (client uses it only as a liveness signal);
//   - deliveredPrefix: rx.next-1, i.e. how many frames the stream has emitted
//     in order to the backend. The client only releases retransmit buffers /
//     its send window on this prefix, which bounds the reassembler window.
//
// Acks are sent every ackEvery arrivals, and at least ackMinInterval apart when
// delivered progress needs reporting early (short streams / sparse lanes).
type laneAckState struct {
	count         uint32
	acked         uint32
	lastAck       time.Time
	lastDelivered uint32
}

// upGroup reassembles zhv5 bucket uploads: byte ranges of the leader's
// upload delivered by member streams are written to the leader's backend in
// offset order. A stalled or reset range kills only that range (the client
// retries/splits it); the leader and the other ranges are unaffected.
// Guarded by its own mu; the groups map itself lives under stmMu.
type upGroup struct {
	mu       sync.Mutex
	leader   *sStream
	bodyLen  uint64 // total body bytes (known once the final range is attached)
	doneOff  uint64
	pending  map[uint64][]byte // off -> complete range bytes, not yet written
	complete bool
}

func (a *serverApp) attachBucketMember(sh *sSession, sid, lsid uint16, off, ln uint64, fin bool) {
	lkey := streamKey(sh.uuid, lsid)
	a.stmMu.Lock()
	if sh.closed {
		a.stmMu.Unlock()
		sh.sess.sendCtrl(Frame{Typ: tReset, SID: sid})
		return
	}
	leader := a.streams[lkey]
	if leader == nil || leader.closed {
		a.stmMu.Unlock()
		if a.resetOnce(lkey) {
			a.logf("tReset sid=%d reason=unknown-leader (%d)", sid, lsid)
		}
		sh.sess.sendCtrl(Frame{Typ: tReset, SID: sid})
		return
	}
	g := a.groups[lkey]
	if g == nil || g.complete {
		g = &upGroup{leader: leader, pending: map[uint64][]byte{}}
		a.groups[lkey] = g
	}
	if fin && off+ln > g.bodyLen {
		g.bodyLen = off + ln
	}
	if existing := a.streams[streamKey(sh.uuid, sid)]; existing != nil {
		// member sid re-opened on this session (reconnect): re-attach only
		existing.members[sh] = true
		a.stmMu.Unlock()
		existing.mu.Lock()
		existing.laneHi[sh] = &laneAckState{}
		existing.mu.Unlock()
		sh.smu.Lock()
		sh.streams[sid] = existing
		sh.smu.Unlock()
		return
	}
	st := &sStream{
		owner:    sh,
		uuid:     sh.uuid,
		sid:      sid,
		upq:      make(chan []byte, 128),
		dialDone: make(chan struct{}),
		dlWake:   make(chan struct{}, 1),
		rx:       reassembler{next: 1, buf: map[uint32][]byte{}},
		rxWake:   make(chan struct{}, maxSlots),
		laneHi:   map[*sSession]*laneAckState{sh: {}},
		members:  map[*sSession]bool{sh: true},
		grpKey:   lkey,
		grpOff:   off,
		grpLen:   ln,
		grpFinal: fin,
	}
	close(st.dialDone) // members have no backend dial
	a.streams[streamKey(sh.uuid, sid)] = st
	a.stmMu.Unlock()
	leader.mu.Lock()
	if leader.grp == nil || leader.grpDone {
		leader.grp = g
		leader.grpDone = false
	}
	leader.hold = true // post-header leader bytes wait for range order
	leader.mu.Unlock()
	sh.smu.Lock()
	sh.streams[sid] = st
	sh.smu.Unlock()
	sh.sess.sendCtrl(Frame{Typ: tOpenAck, SID: sid})
	a.logf("bucket member sid=%d leader=%d off=%d len=%d final=%v attached", sid, lsid, off, ln, fin)
}

// completeMember hands a finished member range to its group, then closes the
// member stream quietly (no reset).
func (a *serverApp) completeMember(st *sStream) {
	st.mu.Lock()
	if st.closed || st.finDone || st.grpKey == "" {
		st.mu.Unlock()
		return
	}
	st.finDone = true
	buf := st.rangeBuf
	st.rangeBuf = nil
	key, off := st.grpKey, st.grpOff
	st.mu.Unlock()
	a.stmMu.Lock()
	g := a.groups[key]
	a.stmMu.Unlock()
	if g == nil {
		return
	}
	a.grpDeliver(g, off, buf)
	a.closeStream(st, false)
}

func (a *serverApp) grpDeliver(g *upGroup, off uint64, buf []byte) {
	var flush [][]byte
	var finish bool
	g.mu.Lock()
	if !g.complete && off >= g.doneOff {
		if _, have := g.pending[off]; !have {
			g.pending[off] = buf
		}
		for {
			b, ok := g.pending[g.doneOff]
			if !ok {
				break
			}
			delete(g.pending, g.doneOff)
			g.doneOff += uint64(len(b))
			if len(b) > 0 {
				flush = append(flush, b)
			}
		}
		if g.bodyLen > 0 && g.doneOff >= g.bodyLen {
			g.complete = true
			finish = true
		}
	}
	leader := g.leader
	g.mu.Unlock()

	for _, b := range flush {
		select {
		case leader.upq <- b:
		case <-time.After(streamStallTO):
			// Backend stalled: same contract as the legacy path — reset this
			// stream only, never silently drop ordered bytes.
			a.logf("bucket group backend stalled leader=%d", leader.sid)
			a.closeStream(leader, true)
			return
		}
	}
	if finish {
		a.logf("bucket group done leader=%d body=%d", leader.sid, g.doneOff)
		leader.mu.Lock()
		leader.hold = false
		leader.grpDone = true
		hb := leader.holdBuf
		leader.holdBuf = nil
		eofNow := leader.rx.fin != 0 && leader.rx.next >= leader.rx.fin && !leader.finDone
		bc := leader.backend
		if eofNow {
			leader.finDone = true
		}
		leader.mu.Unlock()
		for _, b := range hb {
			select {
			case leader.upq <- b:
			case <-time.After(streamStallTO):
				a.logf("bucket group hold-flush stalled leader=%d", leader.sid)
				a.closeStream(leader, true)
				return
			}
		}
		if eofNow && bc != nil {
			if tc, ok := bc.(*net.TCPConn); ok {
				tc.CloseWrite()
			}
		}
	}
}

func (a *serverApp) dispatch(sh *sSession, f *Frame) {
	switch f.Typ {
	case tOpen:
		if lsid, off, ln, fin, ok := decodeBucketMember(f.Payload); ok {
			a.attachBucketMember(sh, f.SID, lsid, off, ln, fin)
			return
		}
		atyp, addr, port, err := decodeAddrPort(f.Payload)
		if err != nil {
			a.logf("tReset sid=%d reason=bad-topen (%v)", f.SID, err)
			sh.sess.sendCtrl(Frame{Typ: tReset, SID: f.SID})
			return
		}
		key := streamKey(sh.uuid, f.SID)
		a.stmMu.Lock()
		if sh.closed {
			a.stmMu.Unlock()
			a.logf("tReset sid=%d reason=session-closed", f.SID)
			sh.sess.sendCtrl(Frame{Typ: tReset, SID: f.SID})
			return
		}
		st := a.streams[key]
		if st == nil {
			if len(a.streams) >= maxStreams {
				a.stmMu.Unlock()
				a.logf("tReset sid=%d reason=max-streams", f.SID)
				sh.sess.sendCtrl(Frame{Typ: tReset, SID: f.SID})
				return
			}
			st = &sStream{
				owner:    sh,
				uuid:     sh.uuid,
				sid:      f.SID,
				upq:      make(chan []byte, 128),
				dialDone: make(chan struct{}),
				dlWake:   make(chan struct{}, 1),
				rx:       reassembler{next: 1, buf: map[uint32][]byte{}},
				rxWake:   make(chan struct{}, maxSlots),
				laneHi:   map[*sSession]*laneAckState{sh: {}},
				members:  map[*sSession]bool{sh: true},
			}
			a.streams[key] = st
			a.stmMu.Unlock()
			sh.smu.Lock()
			sh.streams[f.SID] = st
			sh.smu.Unlock()
			go a.openBackend(sh, st, atyp, addr, port)
			return
		}
		// in-stream spread: this session attaches an already-open stream
		st.members[sh] = true
		a.stmMu.Unlock()
		st.mu.Lock()
		st.laneHi[sh] = &laneAckState{} // fresh counters per attach/reopen
		st.mu.Unlock()
		sh.smu.Lock()
		sh.streams[f.SID] = st
		sh.smu.Unlock()

	case tData:
		if f.SID == 0 {
			return // v0.8 training padding: not a stream, discard silently
		}
		if len(f.Payload) < 4 {
			a.logf("tReset sid=%d reason=short-tdata", f.SID)
			sh.sess.sendCtrl(Frame{Typ: tReset, SID: f.SID})
			return
		}
		seq := binary.BigEndian.Uint32(f.Payload[:4])
		data := f.Payload[4:]
		sh.smu.Lock()
		st := sh.streams[f.SID]
		sh.smu.Unlock()
		if st == nil {
			if a.resetOnce(streamKey(sh.uuid, f.SID)) {
				a.logf("tReset sid=%d reason=unknown-stream (tdata for closed/unopened sid) seq=%d", f.SID, seq)
				sh.sess.sendCtrl(Frame{Typ: tReset, SID: f.SID})
			}
			return
		}
		for {
			st.mu.Lock()
			if st.closed {
				st.mu.Unlock()
				return
			}
			rx := &st.rx
			if seq < rx.next {
				st.mu.Unlock()
				return // already emitted (duplicate)
			}
			if rx.next == 1 && len(rx.buf) == 0 && seq > gapSkip {
				a.logf("reopen gap sid=%d seq %d->%d (frames lost with dead session)", f.SID, rx.next, seq)
				rx.next = seq
			}
			if _, dup := rx.buf[seq]; !dup {
				rx.buf[seq] = data
				rx.bufN += len(data)
			}
			var ready [][]byte
			if st.backend != nil || st.grpKey != "" {
				for {
					b, ok := rx.buf[rx.next]
					if !ok {
						break
					}
					delete(rx.buf, rx.next)
					rx.bufN -= len(b)
					rx.next++
					ready = append(ready, b)
				}
				if len(ready) > 0 {
					// progress: wake every lane parked on this stream
					st.wakeAll()
				}
			}
			// v0.11: track hole age for the retransmit watchdog. A hole that
			// survives holeTimeout means the client could not resend those
			// frames; the stream is then reset instead of hanging forever.
			// Only count real holes: frames buffered while the backend is still
			// dialing are NOT a hole (they are flushed in order by openBackend),
			// and treating them as one reset every multi-frame request 12s in.
			if len(ready) > 0 {
				st.holeSince = time.Time{}
			} else if (st.backend != nil || st.grpKey != "") && rx.bufN > 0 && st.holeSince.IsZero() {
				st.holeSince = time.Now()
			}
			finNow := (st.backend != nil || st.grpKey != "") && rx.fin != 0 && rx.next >= rx.fin && !st.finDone && (st.grp == nil || st.grpDone)
			if finNow && st.grpKey == "" {
				st.finDone = true
			}
			// zhv5 leader hold: while a bucket group is open, leader bytes
			// past the header wait for the ranges to reach byte order.
			if st.grpKey == "" && st.hold && len(ready) > 0 {
				st.holdBuf = append(st.holdBuf, ready...)
				ready = nil
			}
			// zhv5 member: ordered range bytes accumulate in rangeBuf (bounded
			// by the announced range + slack); the group receives them on fin.
			memberOverflow := false
			if st.grpKey != "" {
				for _, b := range ready {
					if int64(len(st.rangeBuf))+int64(len(b)) > int64(st.grpLen)+64*1024 {
						memberOverflow = true
						break
					}
					st.rangeBuf = append(st.rangeBuf, b...)
				}
				ready = nil
			}
			// v0.11 accounting: lane arrival counter (liveness detection on the
			// client) plus the stream's DELIVERED prefix (rx.next-1). The client
			// only releases its send window on the prefix, so the reorder buffer
			// can never exceed the client's streamWindowBytes.
			la := st.laneHi[sh]
			if la == nil {
				la = &laneAckState{}
				st.laneHi[sh] = la
			}
			la.count++
			deliveredNow := rx.next - 1
			needAck := false
			if la.count-la.acked >= ackEvery {
				needAck = true
			} else if deliveredNow != la.lastDelivered && time.Since(la.lastAck) >= ackMinInterval {
				// Delivery progress must reach the client even when few frames
				// arrive on this lane; otherwise a stream could stall with a
				// full window while its frames were already delivered.
				needAck = true
			}
			if needAck {
				la.acked = la.count
				la.lastAck = time.Now()
				la.lastDelivered = deliveredNow
			}
			ackN := la.count
			// Reorder window overflow. NEVER block the session reader here:
			// a park used to freeze every stream sharing this session (and
			// all of the affected stream's other lanes), turning one lost
			// frame into a 15s whole-pool stall + tReset storm. The client
			// retransmits holes now, so this is only a hard safety net.
			overflow := seq >= rx.next && rx.bufN > maxReorderBytes
			logOverflow := false
			if overflow && time.Since(st.lastParkLog) > 5*time.Second {
				st.lastParkLog = time.Now()
				logOverflow = true
			}
			ackBufN := rx.bufN
			nextNow := rx.next
			upq := st.upq
			bc := st.backend
			st.mu.Unlock()
			if logOverflow {
				a.logf("reorder overflow sid=%d bufN=%d next=%d -> reset stream (reader unblocked)", f.SID, ackBufN, nextNow)
			}
			if needAck {
				p := make([]byte, 8)
				binary.BigEndian.PutUint32(p, ackN)
				binary.BigEndian.PutUint32(p[4:], deliveredNow)
				// credit is monotonic state, not ordered data: ctrlQ keeps it
				// flowing even when the session's downlink loQ is congested.
				sh.sess.sendCtrl(Frame{Typ: tAck, SID: f.SID, Payload: p})
			}
			if memberOverflow {
				a.logf("bucket member overflow sid=%d want=%d", f.SID, st.grpLen)
				a.closeStream(st, true)
				return
			}
			if st.grpKey != "" {
				// zhv5 member: no backend write path here; completion by fin.
				if finNow {
					a.completeMember(st)
				}
				return
			}
			stalled := false
			for _, b := range ready {
				select {
				case upq <- b:
				default:
					// Queue full: the app outruns this backend. Backpressure
					// the session (TCP window closes toward the client) rather
					// than destroying a perfectly valid stream. Only a hard
					// stall resets, and only this stream.
					select {
					case upq <- b:
					case <-sh.sess.done:
						return
					case <-time.After(streamStallTO):
						a.logf("backend stalled sid=%d", f.SID)
						stalled = true
					}
				}
				if stalled {
					break
				}
			}
			if stalled {
				a.closeStream(st, true)
				return
			}
			if finNow && bc != nil {
				if tc, ok := bc.(*net.TCPConn); ok {
					tc.CloseWrite()
				}
			}
			if overflow {
				a.closeStream(st, true)
				return
			}
			return
		}

	case tClose: // client half-close (payload = finSeq) -> CloseWrite backend after reorder
		var fin uint32
		if len(f.Payload) >= 4 {
			fin = binary.BigEndian.Uint32(f.Payload[:4])
		}
		sh.smu.Lock()
		st := sh.streams[f.SID]
		sh.smu.Unlock()
		if st == nil {
			return
		}
		st.mu.Lock()
		if st.rx.fin == 0 {
			st.rx.fin = fin
		}
		finNow := (st.backend != nil || st.grpKey != "") && st.rx.next >= st.rx.fin && !st.finDone && (st.grp == nil || st.grpDone)
		if finNow && st.grpKey == "" {
			st.finDone = true
		}
		memberDone := st.grpKey != "" && finNow
		bc := st.backend
		la := st.laneHi[sh]
		if la == nil {
			la = &laneAckState{}
			st.laneHi[sh] = la
		}
		ackCount, ackDelivered := la.count, st.rx.next-1
		st.mu.Unlock()
		// Report final delivery on the FIN path: a sparse tail (or a single-frame
		// request) may otherwise never reach ackEvery, leaving the client's
		// retransmit window waiting for an ack that never comes.
		p := make([]byte, 8)
		binary.BigEndian.PutUint32(p, ackCount)
		binary.BigEndian.PutUint32(p[4:], ackDelivered)
		sh.sess.sendCtrl(Frame{Typ: tAck, SID: f.SID, Payload: p})
		if memberDone {
			a.completeMember(st)
		}
		if finNow && !memberDone && bc != nil {
			if tc, ok := bc.(*net.TCPConn); ok {
				tc.CloseWrite()
			}
		}

	case tReset:
		sh.smu.Lock()
		st := sh.streams[f.SID]
		sh.smu.Unlock()
		if st != nil {
			a.closeStream(st, false)
		}
	}
}

// lookupHost resolves via a chain of public resolvers (8.8.8.8 -> 1.1.1.1 ->
// 223.5.5.5) with a 5-minute cache. The host's default resolver (8.8.8.8)
// intermittently returns "no such host" for CN/CDN domains under poisoning
// or rate limiting; background Windows traffic (store/ads/telemetry) then
// storm-fails every dial. Fallback + cache fixes both latency and flapping.
func (a *serverApp) lookupHost(host string) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []string{host}, nil
	}
	now := time.Now().UnixNano()
	a.dnsMu.Lock()
	e, ok := a.dns[host]
	a.dnsMu.Unlock()
	if ok && e.expA > now {
		return e.ips, nil
	}
	var lastErr error
	collect := func(rs string, useDefault bool, to time.Duration) ([]string, error) {
		r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, proto, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: 3 * time.Second}
			return d.DialContext(ctx, "udp", rs)
		}}
		if useDefault {
			r = net.DefaultResolver
		}
		ctx, cancel := context.WithTimeout(context.Background(), to)
		ips, err := r.LookupHost(ctx, host)
		cancel()
		return ips, err
	}
	// Try the host's system resolver first: on this box it resolves public
	// domains (google, cloudflare) in milliseconds and returns IPv4. The
	// public UDP resolvers below are anti-poisoning fallbacks for CN/CDN
	// domains, but their queries intermittently time out (~5 s each), which
	// stalls the FIRST resolution of every domain (cold) to 10-15 s — long
	// enough that SOCKS CONNECT latency tests time out. System-first removes
	// that stall; public resolvers still cover a poisoned/empty system answer.
	ips, derr := collect("", true, 2*time.Second)
	if derr != nil {
		lastErr = derr
	}
	for _, rs := range []string{"8.8.8.8:53", "1.1.1.1:53", "223.5.5.5:53"} {
		if len(ips) > 0 {
			break
		}
		got, err := collect(rs, false, 5*time.Second)
		if err == nil && len(got) > 0 {
			ips = got
			break
		}
		if err != nil {
			lastErr = err
		}
	}
	if len(ips) > 0 {
		a.dnsMu.Lock()
		if len(a.dns) > 4096 {
			for k, old := range a.dns {
				if old.expA < now {
					delete(a.dns, k)
				}
			}
		}
		a.dns[host] = dnsEntry{ips: ips, expA: now + int64(5*time.Minute)}
		a.dnsMu.Unlock()
		return ips, nil
	}
	return nil, lastErr
}

// resetOnce rate-limits "unknown-stream" resets: a dead sid with hundreds of
// late in-flight frames must not send hundreds of resets back.
func (a *serverApp) resetOnce(key string) bool {
	now := time.Now().UnixNano()
	a.rstMu.Lock()
	defer a.rstMu.Unlock()
	if t, ok := a.rst[key]; ok && now-t < int64(10*time.Second) {
		return false
	}
	if len(a.rst) > 4096 {
		for k, t := range a.rst {
			if now-t > int64(60*time.Second) {
				delete(a.rst, k)
			}
		}
	}
	a.rst[key] = now
	return true
}

func (a *serverApp) openBackend(sh *sSession, st *sStream, atyp byte, addr []byte, port uint16) {
	var bc net.Conn
	var err error
	if a.cfg.backend == "direct" {
		host := addrHost(atyp, addr)
		ips, lerr := a.lookupHost(host)
		if lerr != nil {
			a.logf("backend dial %s failed: lookup: %v", addrStr(atyp, addr, port), lerr)
			a.closeStream(st, true)
			return
		}
		// Prefer IPv4 before IPv6. Many VPS hosts (this one included) have no
		// working IPv6 egress: a resolver that returns AAAA records first
		// (google, cloudflare, …) would make us SYN-send to unreachable IPv6
		// and stall backendDialTimeout per address, so domain backends time
		// out even though the same host is reachable over IPv4. IPv6 stays as
		// a fallback for IPv6-only targets.
		dial := func(list []string) {
			for _, ip := range list {
				bc, err = net.DialTimeout("tcp", net.JoinHostPort(ip, fmt.Sprint(port)), backendDialTimeout)
				if err == nil {
					return
				}
			}
		}
		var v4, v6 []string
		for _, ip := range ips {
			if strings.ContainsRune(ip, ':') {
				v6 = append(v6, ip)
			} else {
				v4 = append(v4, ip)
			}
		}
		dial(v4)
		if err != nil {
			dial(v6)
		}
	} else {
		bc, err = socksDial(a.cfg.backend, atyp, addr, port)
	}
	if err != nil {
		a.logf("backend dial %s failed: %v", addrStr(atyp, addr, port), err)
		a.closeStream(st, true)
		return
	}
	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		bc.Close()
		return
	}
	st.backend = bc
	// replay pre-dial frames strictly in sequence order
	var werr error
	for {
		b, ok := st.rx.buf[st.rx.next]
		if !ok {
			break
		}
		if st.hold { // zhv5: a bucket group opened while the dial was running
			delete(st.rx.buf, st.rx.next)
			st.rx.bufN -= len(b)
			st.rx.next++
			st.holdBuf = append(st.holdBuf, b)
			continue
		}
		bc.SetWriteDeadline(deadline(backendWriteTO))
		if _, err = bc.Write(b); err != nil {
			werr = err
			break
		}
		st.written += int64(len(b))
		delete(st.rx.buf, st.rx.next)
		st.rx.bufN -= len(b)
		st.rx.next++
	}
	finNow := werr == nil && st.rx.fin != 0 && st.rx.next >= st.rx.fin && !st.finDone && (st.grp == nil || st.grpDone)
	if finNow && st.grpKey == "" {
		st.finDone = true
	}
	close(st.dialDone) // after this, dispatch enqueues to upq; no orphan frames
	// The pre-dial flush delivered everything buffered before the backend was
	// ready: any hole tracker started meanwhile is stale.
	st.holeSince = time.Time{}
	// Report delivery progress from the pre-dial flush: without this the client
	// keeps the flushed frames unacked until more traffic arrives (sparse
	// request/response streams would retransmit them needlessly).
	var preAck []byte
	if la := st.laneHi[sh]; la != nil {
		preDelivered := st.rx.next - 1
		if preDelivered > la.lastDelivered {
			preAck = make([]byte, 8)
			binary.BigEndian.PutUint32(preAck, la.count)
			binary.BigEndian.PutUint32(preAck[4:], preDelivered)
			la.lastDelivered = preDelivered
			la.acked = la.count
			la.lastAck = time.Now()
		}
	}
	st.mu.Unlock()
	if preAck != nil {
		sh.sess.sendCtrl(Frame{Typ: tAck, SID: st.sid, Payload: preAck})
	}
	// wake any lane parked pre-dial (nothing could emit until now)
	st.wakeAll()
	if werr != nil {
		a.logf("backend pre-flush err sid=%d: %v", st.sid, werr)
		a.closeStream(st, true)
		return
	}
	if finNow {
		if tc, ok := bc.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}
	sh.sess.sendCtrl(Frame{Typ: tOpenAck, SID: st.sid})
	a.logf("backend connected %s (%s)", addrStr(atyp, addr, port), a.cfg.backend)
	go a.pumpUp(st, sh)

	// pump backend -> client (downlink flows through the owner session)
	// v0.11.4: writer credits the stream's downlink window once a frame has
	// actually reached the socket, so the pump below stops reading the backend
	// when ~1MB is already in flight. Without this a LAN-speed backend (fsrv,
	// CDN) floods a 200ms WAN TCP with tens of MB (loQ 16MB + kernel), causing
	// bufferbloat, loss, RTO backoff freezes (15s) and write-timeout kills.
	sh.sess.onData = func(f Frame) {
		sh.smu.Lock()
		t := sh.streams[f.SID]
		sh.smu.Unlock()
		if t == nil {
			return
		}
		t.mu.Lock()
		t.dlPending -= int64(len(f.Payload))
		if t.dlPending < 0 {
			t.dlPending = 0
		}
		t.mu.Unlock()
		select {
		case t.dlWake <- struct{}{}:
		default:
		}
	}
	buf := make([]byte, maxPayload)
	total := 0
	var rerr error
	for {
		bc.SetReadDeadline(noDeadline())
		n, err := bc.Read(buf)
		if n > 0 {
			total += n
			payload := make([]byte, n)
			copy(payload, buf[:n])
			st.mu.Lock()
			for !st.closed && st.dlPending+int64(len(payload)) > downWindowBytes {
				st.mu.Unlock()
				select {
				case <-st.dlWake:
				case <-time.After(100 * time.Millisecond):
				}
				st.mu.Lock()
			}
			open := !st.closed
			if open {
				st.dlPending += int64(len(payload))
			}
			st.mu.Unlock()
			if !open {
				rerr = fmt.Errorf("stream gone")
				break
			}
			sh.sess.sendData(Frame{Typ: tData, SID: st.sid, Payload: payload})
		}
		if err != nil {
			rerr = err
			break
		}
	}
	// backend EOF/error: report final delivery, then notify client. The client
	// may still hold frames it has not seen acked yet; without this final tAck
	// it would keep retransmitting them into a stream that is already closing
	// (wasted uplink + unknown-stream resets at every request end).
	st.mu.Lock()
	laEnd := st.laneHi[sh]
	if laEnd == nil {
		laEnd = &laneAckState{}
		st.laneHi[sh] = laEnd
	}
	finalDelivered := st.rx.next - 1
	finalCount := laEnd.count
	st.mu.Unlock()
	if st.owner == sh {
		p := make([]byte, 8)
		binary.BigEndian.PutUint32(p, finalCount)
		binary.BigEndian.PutUint32(p[4:], finalDelivered)
		sh.sess.sendCtrl(Frame{Typ: tAck, SID: st.sid, Payload: p})
	}
	sh.sess.sendOrdered(Frame{Typ: tClose, SID: st.sid})
	a.logf("backend pump done sid=%d bytes=%d err=%v", st.sid, total, rerr)
	a.closeStream(st, false)
}

// pumpUp drains one stream's upload queue into its backend on a dedicated
// goroutine. A stalled backend can then never block the session's dispatch
// (other streams' data, OPEN/ACK, control plane) — the head-of-line fix.
func (a *serverApp) pumpUp(st *sStream, sh *sSession) {
	for {
		select {
		case p := <-st.upq:
			st.mu.Lock()
			bc := st.backend
			st.mu.Unlock()
			if bc == nil {
				continue
			}
			bc.SetWriteDeadline(deadline(backendWriteTO))
			n, err := bc.Write(p)
			st.mu.Lock()
			st.written += int64(n)
			st.mu.Unlock()
			if err != nil {
				a.logf("pumpUp write err sid=%d written=%d err=%v", st.sid, st.written, err)
				a.closeStream(st, true)
				return
			}
		case <-sh.sess.done:
			a.logf("pumpUp exit sid=%d written=%d", st.sid, st.written)
			return
		}
	}
}

// closeStream removes a stream from the global map, detaches every member
// session, closes the backend, and (optionally) resets on every member.
func (a *serverApp) closeStream(st *sStream, sendReset bool) {
	a.stmMu.Lock()
	if a.streams[streamKey(st.uuid, st.sid)] != st {
		a.stmMu.Unlock()
		return // already replaced/closed
	}
	delete(a.streams, streamKey(st.uuid, st.sid))
	if st.grpKey == "" {
		delete(a.groups, streamKey(st.uuid, st.sid)) // leader death ends the bucket group
	}
	members := make([]*sSession, 0, len(st.members))
	for m := range st.members {
		members = append(members, m)
	}
	st.members = nil
	a.stmMu.Unlock()

	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return
	}
	st.closed = true
	bc := st.backend
	st.backend = nil
	st.mu.Unlock()
	select {
	case st.dlWake <- struct{}{}: // wake a downlink pump parked on the window
	default:
	}
	st.wakeAll() // release any parked dispatch lanes
	if bc != nil {
		bc.Close()
	}
	if sendReset {
		a.logf("closeStream sid=%d RESET sent written=%d members=%d", st.sid, st.written, len(members))
	}
	for _, m := range members {
		m.smu.Lock()
		if m.streams[st.sid] == st {
			delete(m.streams, st.sid)
		}
		m.smu.Unlock()
		if sendReset && !m.closed {
			m.sess.sendCtrl(Frame{Typ: tReset, SID: st.sid})
		}
	}
}

// detach removes one session from a stream; if the session owned the stream,
// the whole stream dies (downlink flows only through the owner). Otherwise the
// stream lives on the remaining sessions (spread resilience). v0.11: do NOT
// truncate the hole left by the dead lane — the client retransmits those frames
// over the other lanes, and a server-side hole watchdog resets the stream only
// if nothing fills the gap within holeTimeout.
func (a *serverApp) detach(sh *sSession, st *sStream) {
	if st.owner == sh {
		a.closeStream(st, true)
		return
	}
	a.stmMu.Lock()
	delete(st.members, sh)
	empty := len(st.members) == 0
	a.stmMu.Unlock()
	if empty {
		a.closeStream(st, false)
	}
}

func (sh *sSession) closeAll(a *serverApp) {
	sh.smu.Lock()
	sh.closed = true
	sts := make([]*sStream, 0, len(sh.streams))
	for sid, st := range sh.streams {
		sts = append(sts, st)
		delete(sh.streams, sid)
	}
	sh.smu.Unlock()
	for _, st := range sts {
		if a != nil {
			a.detach(sh, st)
		}
	}
	if sh.sess != nil {
		sh.sess.close()
	}
}

// ---------------- anti-probe: per-IP handshake failure rate limit ----------------

func (a *serverApp) allowFail(ip string) bool {
	a.failMu.Lock()
	defer a.failMu.Unlock()
	w := a.fails[ip]
	if w == nil || time.Since(w.from) > time.Minute {
		a.fails[ip] = &failWin{n: 1, from: time.Now()}
		return true
	}
	w.n++
	if len(a.fails) > 4096 { // opportunistic cleanup
		for k, v := range a.fails {
			if time.Since(v.from) > time.Minute {
				delete(a.fails, k)
			}
		}
	}
	return w.n <= 10
}

func looksHTTP(b []byte) bool {
	s := string(b)
	for _, m := range []string{"GET ", "POST ", "HEAD ", "PUT ", "HTTP/1"} {
		if strings.HasPrefix(s, m) {
			return true
		}
	}
	return false
}

func (a *serverApp) spliceDecoy(c net.Conn, head []byte) {
	dc, err := net.DialTimeout("tcp", a.cfg.decoy, 5*time.Second)
	if err != nil {
		c.Close()
		return
	}
	dc.Write(head)
	done := make(chan struct{})
	go func() { io.Copy(dc, c); dc.(*net.TCPConn).CloseWrite(); close(done) }()
	io.Copy(c, dc)
	c.Close()
	dc.Close()
	<-done
}

// ---------------- helpers ----------------

func addrHost(atyp byte, addr []byte) string {
	switch atyp {
	case atypIPv4:
		return net.IPv4(addr[0], addr[1], addr[2], addr[3]).String()
	case atypIPv6:
		return net.IP(addr).String()
	default:
		return string(addr)
	}
}

func addrStr(atyp byte, addr []byte, port uint16) string {
	return fmt.Sprintf("%s:%d", addrHost(atyp, addr), port)
}

func short(uuid string) string {
	if len(uuid) <= 8 {
		return uuid
	}
	return uuid[:8]
}
