// qspike: QUIC feasibility spike — prove quic-go over this line delivers
// hy2-like stable multi-stream upload (target: p10 >= 40M of the ~57M UDP pool).
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"flag"
	"fmt"
	"math/big"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
)

func selfSigned() tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "zhdata"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, _ := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, key.Public(), key)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func main() {
	listen := flag.String("listen", "", "server: udp :port")
	dst := flag.String("dst", "", "client: host:port")
	streams := flag.Int("streams", 4, "concurrent upload streams")
	size := flag.Int("size", 25, "MB per stream cycle")
	dur := flag.Int("t", 60, "seconds")
	flag.Parse()

	tlsConf := &tls.Config{InsecureSkipVerify: true}
	var got atomic.Int64

	if *listen != "" {
		udp, err := net.ListenPacket("udp", ":"+*listen)
		if err != nil {
			fmt.Println("listen:", err)
			return
		}
		tr := &quic.Transport{Conn: udp}
		srv, err := tr.Listen(&tls.Config{Certificates: []tls.Certificate{selfSigned()}}, &quic.Config{MaxIdleTimeout: 30 * time.Second})
		if err != nil {
			fmt.Println("srv:", err)
			return
		}
		defer srv.Close()
		go func() {
			for {
				c, err := srv.Accept(context.Background())
				if err != nil {
					return
				}
				go func() {
					for {
						s, err := c.AcceptStream(context.Background())
						if err != nil {
							return
						}
						go func() {
							buf := make([]byte, 64*1024)
							for {
								n, err := s.Read(buf)
								got.Add(int64(n))
								if err != nil {
									return
								}
							}
						}()
					}
				}()
			}
		}()
		start := time.Now()
		prev := int64(0)
		for time.Since(start) < time.Duration(*dur)*time.Second {
			time.Sleep(time.Second)
			cur := got.Load()
			fmt.Printf("t=%.0fs rx=%.1f Mbps\n", time.Since(start).Seconds(), float64(cur-prev)*8/1e6)
			prev = cur
		}
		os.Exit(0)
	}

	udp, err := net.ListenPacket("udp", ":0")
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	tr := &quic.Transport{Conn: udp}
	ctx0, cancel0 := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel0()
	c, err := tr.Dial(ctx0, mustAddr(*dst), tlsConf, &quic.Config{MaxIdleTimeout: 30 * time.Second, KeepAlivePeriod: 10 * time.Second})
	if err != nil {
		fmt.Println("dial conn:", err)
		return
	}
	defer c.CloseWithError(0, "bye")
	body := make([]byte, 64*1024)
	mb := *size
	var sent atomic.Int64
	deadline := time.Now().Add(time.Duration(*dur) * time.Second)
	var wgs int
	done := make(chan struct{}, *streams)
	for i := 0; i < *streams; i++ {
		wgs++
		go func(i int) {
			defer func() { done <- struct{}{} }()
			for time.Now().Before(deadline) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				s, err := c.OpenStreamSync(ctx)
				cancel()
				if err != nil {
					fmt.Println("openstream err:", err)
					return
				}
				total := mb * 1024 * 1024
				for off := 0; off < total; {
					n := len(body)
					if total-off < n {
						n = total - off
					}
					_, err := s.Write(body[:n])
					if err != nil {
						fmt.Println("write err:", err)
						s.Close()
						return
					}
					off += n
					sent.Add(int64(n))
				}
				s.Close()
			}
		}(i)
	}
	start := time.Now()
	prev := int64(0)
	for time.Since(start) < time.Duration(*dur)*time.Second {
		time.Sleep(time.Second)
		cur := sent.Load()
		fmt.Printf("t=%.0fs tx=%.1f Mbps\n", time.Since(start).Seconds(), float64(cur-prev)*8/1e6)
		prev = cur
	}
	c.CloseWithError(0, "bye")
	for range wgs {
		<-done
	}
}

func mustAddr(s string) net.Addr {
	u, err := net.ResolveUDPAddr("udp", s)
	if err != nil {
		fmt.Println("addr:", err)
		os.Exit(1)
	}
	return u
}
