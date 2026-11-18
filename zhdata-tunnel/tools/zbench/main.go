// zbench: speedtest-profile load generator against zhdata's local socks5.
// Mimics what v2rayN's speedtest actually does (which curl -Z does not):
// N staggered bulk PUT streams PLUS a churn of tiny probe streams, sampled
// per-second so we get stability percentiles, not just an average.
//
//	go run ./tools/zbench -runs 3 -t 60 -streams 4 -size 25m -probe-ps 30
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type stats struct{ n atomic.Int64 } // bytes written to tunnel (sampled delta)

// lg logs to the log FILE, never stdout: on Windows, printing to a console
// handle can block for tens of seconds (scroll-lock), which previously froze
// the sampler loop and faked a "0 Mbps then burst" profile.
var lg = log.New(os.Stderr, "", 0)

var runT0 time.Time

func socksDial(socks, host string, port uint16) (net.Conn, error) {
	if socks == "none" { // raw TCP control: same profile straight to target
		return net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 20*time.Second)
	}
	c, err := net.DialTimeout("tcp", socks, 10*time.Second)
	if err != nil {
		return nil, err
	}
	const hsTO = 10 * time.Second // handshake only; body writes must NOT keep this deadline or long PUTs die at t+20s
	c.SetDeadline(time.Now().Add(hsTO))
	hp := net.ParseIP(host)
	v4 := hp.To4()
	var req []byte
	if v4 != nil {
		req = []byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0}
		copy(req[4:], v4)
		req[8] = byte(port >> 8)
		req[9] = byte(port)
	} else {
		req = append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
		req = append(req, byte(port>>8), byte(port))
	}
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		c.Close()
		return nil, err
	}
	b := make([]byte, 2)
	if _, err := io.ReadFull(c, b); err != nil || b[1] != 0 {
		c.Close()
		return nil, fmt.Errorf("socks greeting %v", b)
	}
	if _, err := c.Write(req); err != nil {
		c.Close()
		return nil, err
	}
	resp := make([]byte, 4)
	if _, err := io.ReadFull(c, resp); err != nil {
		c.Close()
		return nil, err
	}
	switch resp[3] {
	case 1:
		io.ReadFull(c, make([]byte, 6))
	case 3:
		n := int(resp[4])
		extra := make([]byte, n+2)
		io.ReadFull(c, extra)
	case 4:
		io.ReadFull(c, make([]byte, 18))
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

func put(c net.Conn, size int, body []byte, s *stats) error {
	fmt.Fprintf(c, "PUT /up HTTP/1.1\r\nHost: up\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", size)
	sent := 0
	nextLog := 4 * 1024 * 1024
	for sent < size {
		n := len(body)
		if size-sent < n {
			n = size - sent
		}
		w, err := c.Write(body[:n])
		sent += w
		if s != nil {
			s.n.Add(int64(w))
		}
		if sent >= nextLog {
			lg.Printf("  progress %.0fMB t=%.0fs\n", float64(sent)/1e6, time.Since(runT0).Seconds())
			nextLog += 4 * 1024 * 1024
		}
		if err != nil {
			return err
		}
	}
	resp := make([]byte, 512)
	for {
		_, err := c.Read(resp)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		break // headers arrive once body is consumed; close after
	}
	c.Close()
	return nil
}

func main() {
	socks := flag.String("socks", "127.0.0.1:49888", "socks5 endpoint")
	host := flag.String("host", "154.12.53.217", "target host")
	ports := flag.String("ports", "8000-8007", "target ports")
	runs := flag.Int("runs", 3, "runs")
	t := flag.Int("t", 60, "seconds per run")
	streams := flag.Int("streams", 4, "concurrent bulk streams")
	sizeS := flag.String("size", "25m", "size per bulk stream")
	probePS := flag.Int("probe-ps", 30, "probe streams per second (0 disables)")
	probeSize := flag.Int("probe-size", 3000, "probe body bytes")
	stagger := flag.Int("stagger", 1500, "avg ms stagger between stream starts")
	cooldown := flag.Int("cooldown", 15, "seconds between runs")
	flag.Parse()

	sizeMB := 25
	fmt.Sscanf(*sizeS, "%dm", &sizeMB)
	size := sizeMB * 1024 * 1024
	body := make([]byte, 64*1024)
	rand.New(rand.NewSource(1)).Read(body)
	probeBody := make([]byte, *probeSize)
	rand.New(rand.NewSource(2)).Read(probeBody)

	var p0, p1 uint16
	fmt.Sscanf(*ports, "%d-%d", &p0, &p1)
	vars := stats{}
	runT0 = time.Now()

	for r := 1; r <= *runs; r++ {
		vars.n.Store(0)
		stop := make(chan struct{})
		var wg sync.WaitGroup
		deadline := time.Now().Add(time.Duration(*t) * time.Second)
		// bulk streams: staggered starts, restart on finish or deadline
		for i := 0; i < *streams; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				for time.Now().Before(deadline) {
					port := p0 + uint16(rand.Intn(int(p1-p0+1)))
					c, err := socksDial(*socks, *host, port)
					if err != nil {
						lg.Printf("w%d dial err: %v\n", i, err)
						break
					}
					lg.Printf("w%d dial ok port=%d t=%.0fs\n", i, port, time.Since(runT0).Seconds())
					if err := put(c, size, body, &vars); err != nil {
						lg.Printf("w%d put err at t=%.0fs: %v\n", i, time.Since(runT0).Seconds(), err)
						break
					}
					lg.Printf("w%d put done t=%.0fs\n", i, time.Since(runT0).Seconds())
					time.Sleep(time.Duration(rand.Intn(*stagger)) * time.Millisecond)
				}
			}(i)
		}
		// probe churn
		if *probePS > 0 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					if time.Now().After(deadline) {
						return
					}
					port := p0 + uint16(rand.Intn(int(p1-p0+1)))
					c, err := socksDial(*socks, *host, port)
					if err != nil {
						continue
					}
					go put(c, len(probeBody), probeBody, &vars)
					time.Sleep(time.Second / time.Duration(*probePS))
				}
			}()
		}
		// per-second samples
		var samples []float64
		prev := vars.n.Load()
		lg.Printf("sampler start prev=%d t=%.0fs\n", prev, time.Since(runT0).Seconds())
		for time.Now().Before(deadline) {
			time.Sleep(time.Second)
			cur := vars.n.Load()
			mbps := float64(cur-prev) * 8 / 1e6
			samples = append(samples, mbps)
			lg.Printf("sampler t=%.0fs cur=%d\n", time.Since(runT0).Seconds(), cur)
			prev = cur
		}
		close(stop)
		_ = wg // goroutines may still be draining; exit after printing (os.Exit below)
		time.Sleep(500 * time.Millisecond)
		// drain: give stragglers a beat then move on
		time.Sleep(500 * time.Millisecond)
		timeline := append([]float64(nil), samples...)
		sort.Float64s(samples)
		pct := func(p float64) float64 {
			if len(samples) == 0 {
				return 0
			}
			i := int(p * float64(len(samples)))
			if i >= len(samples) {
				i = len(samples) - 1
			}
			return samples[i]
		}
		fmt.Printf("run %d: p10=%.1f p50=%.1f min=%.1f max=%.1f Mbps (n=%d samples)\n",
			r, pct(0.10), pct(0.50), pct(0.0), pct(1.0), len(samples))
		fmt.Println("timeline:")
		for i, v := range timeline {
			fmt.Printf("%2d:%.0f ", i, v)
			if i%10 == 9 {
				fmt.Println()
			}
		}
		fmt.Println()
		if r < *runs {
			time.Sleep(time.Duration(*cooldown) * time.Second)
		}
	}
	os.Exit(0)
}
