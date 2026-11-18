// spd: speedtest.net-style upload load generator.
//
// It reproduces what the real speedtest.net browser/CLI client does for the
// upload phase: repeated HTTP POST requests to
//   http://<ookla-server>:8080/upload.php?x=<nonce>
// with a Content-Length body of zeroes. Unlike zbench's long-lived PUT /up
// streams, this opens a NEW application stream for every request (the python
// speedtest-cli does the same: urllib sends Connection: close), which is the
// shape the tunnel sees during a real speedtest run.
//
//	go run ./tools/spd -socks 127.0.0.1:49888 -host la02.speedtest.windstream.net -streams 8 -size 5000000 -t 30
//	go run ./tools/spd -socks none -host <ip> -port 8080 -streams 8 -size 5000000 -t 30
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

var lg = log.New(os.Stderr, "", 0)

func socksDial(socks, host string, port uint16) (net.Conn, error) {
	if socks == "none" {
		return net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 20*time.Second)
	}
	c, err := net.DialTimeout("tcp", socks, 10*time.Second)
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(10 * time.Second))
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

func postOnce(c net.Conn, host string, size int, body []byte, deadline time.Time) (int, error) {
	req := fmt.Sprintf("POST /upload.php?x=%d.%d HTTP/1.1\r\nHost: %s\r\nUser-Agent: Mozilla/5.0\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		time.Now().UnixMilli(), rand.Intn(1000), host, size)
	c.SetWriteDeadline(deadline)
	if _, err := io.WriteString(c, req); err != nil {
		return 0, err
	}
	sent := 0
	for sent < size {
		n := len(body)
		if size-sent < n {
			n = size - sent
		}
		w, err := c.Write(body[:n])
		sent += w
		if err != nil {
			return sent, err
		}
	}
	// drain the small reply; not needed for measurement, but keeps the peer clean
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	io.Copy(io.Discard, c)
	return sent, nil
}

func main() {
	socks := flag.String("socks", "127.0.0.1:49888", "socks5 endpoint or 'none'")
	host := flag.String("host", "la02.speedtest.windstream.net", "ookla server host")
	port := flag.Int("port", 8080, "ookla server port")
	streams := flag.Int("streams", 8, "concurrent upload requests")
	size := flag.Int("size", 5000000, "bytes per POST")
	t := flag.Int("t", 30, "seconds")
	runs := flag.Int("runs", 1, "runs")
	cooldown := flag.Int("cooldown", 10, "seconds between runs")
	flag.Parse()

	body := make([]byte, 256*1024)
	rand.New(rand.NewSource(1)).Read(body)
	deadline := time.Now().Add(time.Duration(*t) * time.Second)

	for r := 1; r <= *runs; r++ {
		var n atomic.Int64
		var wg sync.WaitGroup
		deadline = time.Now().Add(time.Duration(*t) * time.Second)
		stop := make(chan struct{})
		for i := 0; i < *streams; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				for time.Now().Before(deadline) {
					c, err := socksDial(*socks, *host, uint16(*port))
					if err != nil {
						lg.Printf("s%d dial err: %v\n", i, err)
						time.Sleep(200 * time.Millisecond)
						continue
					}
					w, err := postOnce(c, *host, *size, body, deadline)
					n.Add(int64(w))
					c.Close()
					if err != nil {
						lg.Printf("s%d post err after %d bytes: %v\n", i, w, err)
					}
				}
			}(i)
		}
		var samples []float64
		prev := int64(0)
		var timeline []float64
		for time.Now().Before(deadline) {
			select {
			case <-stop:
			default:
			}
			time.Sleep(time.Second)
			cur := n.Load()
			mbps := float64(cur-prev) * 8 / 1e6
			prev = cur
			samples = append(samples, mbps)
			timeline = append(timeline, mbps)
		}
		close(stop)
		wg.Wait()
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
		total := n.Load()
		fmt.Printf("run %d: p10=%.1f p50=%.1f min=%.1f max=%.1f mean=%.1f Mbps (total %.1f MB, n=%d)\n",
			r, pct(0.10), pct(0.50), pct(0.0), pct(1.0),
			float64(total)*8/float64(*t)/1e6, float64(total)/1e6, len(samples))
		fmt.Print("timeline: ")
		for i, v := range timeline {
			fmt.Printf("%d:%.0f ", i, v)
		}
		fmt.Println()
		if r < *runs {
			time.Sleep(time.Duration(*cooldown) * time.Second)
		}
	}
}
