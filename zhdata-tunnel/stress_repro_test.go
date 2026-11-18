package main

// stress_repro_test.go — deterministic regression tests for the upload collapse
// that used to happen on high-RTT (~170-240ms), lossy/reordering lines.
//
// History: one app stream was spread over many sessions (lanes); one stalled
// lane left a hole in the server reassembler (rx.next small, rx.bufN growing),
// and when rx.bufN crossed maxReorderBytes the old code PARKED the session
// reader — freezing every stream sharing that session (cross-stream HOL) until
// gapWaitTO=15s, then broadcasting tReset. v0.10 removed the park: an overflow
// now resets only the affected stream and the reader keeps dispatching.
//
// These tests pin the new contract:
//  1. reorder overflow -> dispatch returns immediately, stream is reset;
//  2. a stream overflow does NOT block other streams on the same session;
//  3. client per-lane credit bookkeeping still works (static window).
//
// Whitebox on purpose: builds sStream/reassembler state and drives
// serverApp.dispatch() directly (plus one real session read loop), so the tests
// are deterministic and finish in well under a second. No WAN traffic.

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// stressNewServerApp builds the minimum serverApp needed to drive dispatch():
// no listeners, no key store.
func stressNewServerApp() *serverApp {
	return &serverApp{
		cfg:     serverConfig{},
		conns:   map[string]*sSession{},
		streams: map[string]*sStream{},
		fails:   map[string]*failWin{},
		dns:     map[string]dnsEntry{},
		rst:     map[string]int64{},
		stopCh:  make(chan struct{}),
	}
}

// stressIdleSession is a session with no reader/writer goroutines: enough for
// sendCtrl() traffic (ctrlQ is buffered) and for tests to close/release it.
func stressIdleSession(t *testing.T, name string) *session {
	t.Helper()
	c1, c2 := net.Pipe()
	s := newSession(name, c1, nopStream{}, nopStream{})
	t.Cleanup(func() {
		s.close()
		c2.Close()
	})
	return s
}

// stressShim wraps an idle session in the server-side per-connection state.
func stressShim(t *testing.T, uuid, name string) *sSession {
	t.Helper()
	return &sSession{
		uuid:    uuid,
		sess:    stressIdleSession(t, name),
		streams: map[uint16]*sStream{},
	}
}

// stressStream constructs the sStream that a tOpen would create and registers
// it exactly like dispatch() does.
func stressStream(a *serverApp, owner *sSession, sid uint16) *sStream {
	st := &sStream{
		owner:    owner,
		uuid:     owner.uuid,
		sid:      sid,
		upq:      make(chan []byte, 128),
		dialDone: make(chan struct{}),
		rx:       reassembler{next: 1, buf: map[uint32][]byte{}},
		rxWake:   make(chan struct{}, maxSlots),
		laneHi:   map[*sSession]*laneAckState{owner: {}},
		members:  map[*sSession]bool{owner: true},
	}
	owner.smu.Lock()
	owner.streams[sid] = st
	owner.smu.Unlock()
	a.stmMu.Lock()
	a.streams[streamKey(owner.uuid, sid)] = st
	a.stmMu.Unlock()
	return st
}

// stressAttach mirrors the in-stream spread branch of tOpen: an additional
// session becomes a member/lane of an already-open stream.
func stressAttach(a *serverApp, st *sStream, sh *sSession) {
	a.stmMu.Lock()
	st.members[sh] = true
	a.stmMu.Unlock()
	st.mu.Lock()
	st.laneHi[sh] = &laneAckState{}
	st.mu.Unlock()
	sh.smu.Lock()
	sh.streams[st.sid] = st
	sh.smu.Unlock()
}

// stressDataPayload builds a client DATA frame payload: 4B stream-global
// sequence prefix + data bytes.
func stressDataPayload(seq uint32, dataLen int) []byte {
	p := make([]byte, 4+dataLen)
	binary.BigEndian.PutUint32(p[:4], seq)
	return p
}

// fillReorderWindow lands seq 2..windowFrames+1 (16KiB each) on the fast lane
// while seq 1 stays lost on the stalled lane, so rx.next stays 1 and rx.bufN
// reaches exactly maxReorderBytes.
func fillReorderWindow(a *serverApp, st *sStream, fast *sSession) (windowFrames int) {
	chunk := maxPayload
	windowFrames = maxReorderBytes / chunk
	for seq := uint32(2); seq <= uint32(windowFrames+1); seq++ {
		a.dispatch(fast, &Frame{Typ: tData, SID: st.sid, Payload: stressDataPayload(seq, chunk)})
	}
	return windowFrames
}

// TestStressReorderOverflowResetsOnlyTheStream proves that crossing the server
// reorder window no longer blocks the session reader: dispatch returns at once
// and closes just that stream (the app retries; other streams keep flowing).
func TestStressReorderOverflowResetsOnlyTheStream(t *testing.T) {
	const uuid = "stress-overflow-uuid"
	const sid = 7

	a := stressNewServerApp()
	owner := stressShim(t, uuid, "stream-owner")
	stalled := stressShim(t, uuid, "lane-1-stalled")
	fast := stressShim(t, uuid, "lane-2-fast")

	st := stressStream(a, owner, sid)
	stressAttach(a, st, stalled) // seq 1 never arrives on this lane
	stressAttach(a, st, fast)

	windowFrames := fillReorderWindow(a, st, fast)
	st.mu.Lock()
	bufN, next := st.rx.bufN, st.rx.next
	st.mu.Unlock()
	if bufN != maxReorderBytes || next != 1 {
		t.Fatalf("precondition: bufN=%d next=%d, want %d/1", bufN, next, maxReorderBytes)
	}

	// One more 16KiB frame crosses the window: dispatch must return quickly
	// (no reader park) and reset the stream.
	done := make(chan struct{})
	go func() {
		a.dispatch(fast, &Frame{Typ: tData, SID: sid, Payload: stressDataPayload(uint32(windowFrames+2), maxPayload)})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch blocked on reorder overflow: the session reader is parked again")
	}

	a.stmMu.Lock()
	_, stillThere := a.streams[streamKey(uuid, sid)]
	a.stmMu.Unlock()
	if stillThere {
		t.Fatal("stream still registered after reorder overflow; expected reset")
	}
	st.mu.Lock()
	closed := st.closed
	st.mu.Unlock()
	if !closed {
		t.Fatal("stream not marked closed after reorder overflow")
	}
	if frames := windowFrames + 1; frames <= 1 {
		t.Fatalf("model invalid: only %d buffered frames", frames)
	}
}

// TestStressOverflowDoesNotBlockOtherStreams proves the cross-stream HOL
// cascade is gone: with a REAL session read loop, stream A overflows the window
// and is reset, and stream B's frame queued behind A on the same wire is still
// read and dispatched immediately (the old code froze the reader for 15s).
func TestStressOverflowDoesNotBlockOtherStreams(t *testing.T) {
	const uuid = "stress-cascade-uuid"
	const sidA, sidB = 0xA1, 0xB2

	a := stressNewServerApp()
	owner := stressShim(t, uuid, "A-owner")

	srvConn, cliConn := net.Pipe()
	salt := randBytes(16)
	srvSess := newSession("srv-shared", srvConn, newCTR(uuid, salt), newCTR(uuid, salt))
	cliSess := newSession("cli-shared", cliConn, newCTR(uuid, salt), newCTR(uuid, salt))
	t.Cleanup(func() {
		srvSess.close()
		cliSess.close()
	})
	shared := &sSession{uuid: uuid, sess: srvSess, streams: map[uint16]*sStream{}}

	// Stream A spreads over owner + shared; seq 1 is lost (stalled lane).
	stA := stressStream(a, owner, sidA)
	stressAttach(a, stA, shared)

	// Stream B shares the same session, queued after A's flood.
	stB := stressStream(a, shared, sidB)

	go srvSess.run(func(f *Frame) { a.dispatch(shared, f) })
	go cliSess.run(func(*Frame) {})

	windowFrames := maxReorderBytes / maxPayload
	for seq := uint32(2); seq <= uint32(windowFrames+2); seq++ {
		cliSess.sendData(Frame{Typ: tData, SID: sidA, Payload: stressDataPayload(seq, maxPayload)})
	}
	cliSess.sendData(Frame{Typ: tData, SID: sidB, Payload: stressDataPayload(1, 8)})

	// A must be reset (overflow) rather than parked.
	waitFor(t, func() bool {
		stA.mu.Lock()
		closed := stA.closed
		stA.mu.Unlock()
		return closed
	}, "stream A to be reset on reorder overflow")

	// B must be dispatched right after A's reset frame, never blocked.
	waitFor(t, func() bool {
		stB.mu.Lock()
		defer stB.mu.Unlock()
		return stB.rx.bufN > 0
	}, "stream B to be dispatched after A's overflow reset")
}

// stressNewClientApp builds a clientApp whose N slots are already "live" for
// whitebox credit tests.
func stressNewClientApp(t *testing.T, lanes int) *clientApp {
	t.Helper()
	a := &clientApp{
		cfg:     clientConfig{conns: lanes, spread: lanes},
		streams: map[uint32]*cStream{},
		stopCh:  make(chan struct{}),
	}
	for i := 0; i < lanes; i++ {
		a.conns = append(a.conns, &cConn{
			app:     a,
			idx:     byte(i),
			sess:    stressIdleSession(t, fmt.Sprintf("cli-lane-%d", i)),
			sessMap: map[uint16]*cStream{},
			members: map[uint32]*cStream{},
		})
	}
	return a
}

// TestStressUploadWindowAndRetransmit pins the v0.11 (route A) upload contract:
//   - the send window is byte-based and only released by the server's DELIVERED
//     prefix, never by mere lane arrivals;
//   - a lane that stops making progress has its pending frames retransmitted
//     over the other lanes without disturbing the window accounting;
//   - frames stay accounted exactly once across the whole stream lifetime.
func TestStressUploadWindowAndRetransmit(t *testing.T) {
	const lanes = 3
	const chunk = 1000
	frameLen := int64(chunk + 4) // 4B seq prefix
	a := stressNewClientApp(t, lanes)
	const sid = 1
	s := &cStream{
		app:   a,
		id:    1,
		sid:   sid,
		home:  a.conns[0],
		conns: append([]*cConn(nil), a.conns...),
	}
	s.attached = lanes - 1
	for _, cn := range a.conns {
		s.ensureLaneLocked(cn)
		cn.members[s.id] = s
		cn.sessMap[sid] = s
	}
	a.mu.Lock()
	a.streams[s.id] = s
	a.mu.Unlock()

	// 30 frames must be balanced across the 3 lanes by pending bytes.
	for i := uint32(1); i <= 30; i++ {
		if !s.sendFrame(i, stressDataPayload(i, chunk)) {
			t.Fatalf("sendFrame(%d) failed", i)
		}
	}
	a.mu.Lock()
	if want := 30 * frameLen; s.inflightBytes != want || a.inflightBytes != want {
		t.Fatalf("window accounting: stream=%d global=%d want %d", s.inflightBytes, a.inflightBytes, want)
	}
	for _, cn := range s.conns {
		if s.laneBytes[cn] != 10*frameLen {
			t.Fatalf("lane %d pending=%d want %d", cn.idx, s.laneBytes[cn], 10*frameLen)
		}
	}
	a.mu.Unlock()

	// The server reports delivered prefix 10: release exactly frames 1..10.
	s.applyAck(a.conns[0], 10, 10)
	a.mu.Lock()
	if s.ackedSeq != 10 {
		t.Fatalf("ackedSeq=%d want 10", s.ackedSeq)
	}
	if len(s.sentq) != 20 || s.sentq[0].seq != 11 {
		t.Fatalf("sentq len=%d firstSeq=%d want 20/11", len(s.sentq), s.sentq[0].seq)
	}
	if want := 20 * frameLen; s.inflightBytes != want {
		t.Fatalf("inflight after release=%d want %d", s.inflightBytes, want)
	}
	lane0Before := s.laneBytes[a.conns[0]]
	if lane0Before <= 0 {
		t.Fatalf("lane0 pending=%d, expected frames 11..30 still assigned", lane0Before)
	}
	a.mu.Unlock()

	// Fast hole retransmit: the front frame (seq 11) blocks ALL delivery, so
	// once it has been undelivered for holeResendTO it must be re-sent on a
	// different lane; a live-but-slow lane is deliberately NOT migrated (that
	// caused the moving-hole livelock). Window accounting must stay balanced.
	a.mu.Lock()
	s.frontSince = time.Now().Add(-2 * holeResendTO)
	frontBefore := s.sentq[0]
	frontLane := frontBefore.lane
	totalBefore := s.inflightBytes
	a.mu.Unlock()
	s.checkStalls()
	a.mu.Lock()
	totalAfter := s.inflightBytes
	frontLaneAfter := frontBefore.lane
	a.mu.Unlock()
	if frontLaneAfter == frontLane {
		t.Fatalf("front hole frame not retransmitted: still on lane %v", frontLane)
	}
	if totalAfter != totalBefore || totalAfter != 20*frameLen {
		t.Fatalf("window drifted by retransmit: %d -> %d (want %d)", totalBefore, totalAfter, 20*frameLen)
	}

	// A DEAD lane (session gone) is still migrated: its frames must move to
	// the remaining lanes and the lane gets detached once empty.
	deadLane := a.conns[2]
	a.mu.Lock()
	deadLane.sess = nil
	deadPending := s.laneBytes[deadLane]
	a.mu.Unlock()
	if deadPending <= 0 {
		t.Fatalf("test setup: dead lane has no pending frames")
	}
	s.checkStalls()
	a.mu.Lock()
	deadAfter := s.laneBytes[deadLane]
	lanesAfter := len(s.conns)
	totalAfter = s.inflightBytes
	a.mu.Unlock()
	if deadAfter != 0 {
		t.Fatalf("dead lane still owns %d pending bytes", deadAfter)
	}
	if lanesAfter != 2 {
		t.Fatalf("dead lane not detached: %d lanes", lanesAfter)
	}
	if totalAfter != 20*frameLen {
		t.Fatalf("window drifted by dead-lane migration: %d", totalAfter)
	}
	if streamWindowBytes < 2<<20 || globalWindowBytes < streamWindowBytes {
		t.Fatalf("window constants look wrong: stream=%d global=%d", streamWindowBytes, globalWindowBytes)
	}
}

// nopConn is a net.Conn whose methods are no-ops, for whitebox stream tests
// that only need teardown() to be able to Close() the local socket.
type nopConn struct{}

func (nopConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (nopConn) Write(p []byte) (int, error)      { return len(p), nil }
func (nopConn) Close() error                     { return nil }
func (nopConn) LocalAddr() net.Addr              { return nil }
func (nopConn) RemoteAddr() net.Addr             { return nil }
func (nopConn) SetDeadline(time.Time) error      { return nil }
func (nopConn) SetReadDeadline(time.Time) error  { return nil }
func (nopConn) SetWriteDeadline(time.Time) error { return nil }

// TestStressDownlinkQueueFullFailsStream pins the v0.10.1 downlink contract: a
// stalled app reader must not block the session's frame reader indefinitely.
// The old code blocked forever on a full rq, which froze every stream on the
// slot and (after the peer's 15s write timeout) killed the whole TCP session —
// in the field that produced hung downloads. Now dispatch waits at most ~2s,
// then fails only that stream.
func TestStressDownlinkQueueFullFailsStream(t *testing.T) {
	a := stressNewClientApp(t, 1)
	cn := a.conns[0]

	s := &cStream{
		app:   a,
		id:    42,
		sid:   4242,
		home:  cn,
		conns: []*cConn{cn},
		local: nopConn{},
		rq:    make(chan []byte, 2),
		done:  make(chan struct{}),
	}
	s.ensureLaneLocked(cn)
	a.mu.Lock()
	a.streams[s.id] = s
	cn.members[s.id] = s
	cn.sessMap[s.sid] = s
	cn.gen = 1
	a.mu.Unlock()

	// Fill the downlink queue so the next DATA frame has nowhere to go.
	s.rq <- []byte("x")
	s.rq <- []byte("y")

	start := time.Now()
	done := make(chan struct{})
	go func() {
		cn.dispatch(1, &Frame{Typ: tData, SID: s.sid, Payload: []byte("z")})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch blocked on a full downlink queue: session reader can stall again")
	}
	if waited := time.Since(start); waited > 4*time.Second {
		t.Fatalf("dispatch took %v, want bounded (~2s)", waited)
	}
	select {
	case <-s.done:
	default:
		t.Fatal("stream was not failed after downlink queue stayed full")
	}
	a.mu.Lock()
	_, still := a.streams[s.id]
	a.mu.Unlock()
	if still {
		t.Fatal("stream still registered after downlink queue overflow")
	}
}
