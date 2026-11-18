// udpflood: minimal UDP throughput probe to test whether the ISP polices
// UDP the same way it polices TCP (hy2 rides QUIC/UDP at a stable 50M;
// our TCP sessions oscillate 0-160M). Run -listen on the far side.
package main

import (
	"flag"
	"fmt"
	"net"
	"time"
)

func main() {
	dst := flag.String("dst", "", "host:port to flood (client mode)")
	rate := flag.Int("rate", 6, "target Mbps")
	dur := flag.Int("dur", 8, "seconds")
	lport := flag.String("listen", "", "listen on :port (server mode)")
	dgrams := flag.Int("dgram", 1400, "datagram size")
	flag.Parse()

	if *lport != "" {
		c, err := net.ListenPacket("udp", ":"+*lport)
		if err != nil {
			fmt.Println("listen:", err)
			return
		}
		defer c.Close()
		buf := make([]byte, 2048)
		start := time.Now()
		var got, bytes int
		last := start
		for {
			_, _, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			got++
			bytes += *dgrams
			if n := time.Since(last); n >= time.Second {
				fmt.Printf("t=%.0fs rx=%.1f Mbps (cum %d dg)\n", time.Since(start).Seconds(), float64(bytes)*8/1e6/n.Seconds(), got)
				last = time.Now()
				bytes = 0
			}
		}
	}

	udpAddr, err := net.ResolveUDPAddr("udp", *dst)
	if err != nil {
		fmt.Println("resolve:", err)
		return
	}
	c, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	defer c.Close()
	payload := make([]byte, *dgrams)
	interval := time.Duration(float64(*dgrams) * 8 / float64(*rate) / 1e6 * float64(time.Second))
	start := time.Now()
	seq := 0
	for time.Since(start) < time.Duration(*dur)*time.Second {
		seq++
		copy(payload, []byte(fmt.Sprintf("seq=%09d", seq)))
		if _, err := c.Write(payload); err != nil {
			fmt.Println("write:", err)
			return
		}
		// deadline pacing: cumulative time.Sleep overshoots on Windows and
		// under-offers; sleep only when ahead of the byte clock.
		if d := start.Add(time.Duration(float64(seq) * float64(interval))).Sub(time.Now()); d > 0 {
			time.Sleep(d)
		}
	}
	fmt.Printf("sent %d datagrams (%.1f MB) in %ds = %.1f Mbps offered\n", seq, float64(seq*(*dgrams))/1e6, *dur, float64(seq*(*dgrams))*8/1e6/float64(*dur))
}
