// throughput target: GET -> 50MB body; POST/PUT with Content-Length -> drain
// body then small ok reply (upload target). Usage: fsrv [port]
package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

func main() {
	port := "8000"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}
	ln, err := net.Listen("tcp", "0.0.0.0:"+port)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	body := make([]byte, 50<<20)
	for i := range body {
		body[i] = byte(i)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			continue
		}
		go func(c net.Conn) {
			defer c.Close()
			br := bufio.NewReader(c)
			cl := 0
			for {
				line, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if line == "\r\n" || line == "\n" {
					break
				}
				if i := strings.Index(line, ":"); i >= 0 && strings.EqualFold(line[:i], "Content-Length") {
					cl, _ = strconv.Atoi(strings.TrimSpace(line[i+1:]))
				}
			}
			if cl > 0 {
				n, err := io.CopyN(io.Discard, br, int64(cl))
				fmt.Printf("%s up-done %d\n", time0(), n)
				_ = err
				fmt.Fprintf(c, "HTTP/1.0 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
				return
			}
			fmt.Fprintf(c, "HTTP/1.0 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", len(body))
			c.Write(body)
		}(c)
	}
}

func time0() string { return "-" }
