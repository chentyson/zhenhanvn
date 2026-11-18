package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"
)

const version = "0.12.4" // + fix download regression: drop sessMap recycle-pin (flows decayed, 60M->15M); bump session activity on stream-open instead

func main() {
	mode := flag.String("mode", "client", "client | server")
	listen := flag.String("listen", "", "listen address; comma separated for multi-port (client: local socks5; server: zhdata public ports)")
	server := flag.String("server", "", "zhdata server host:port, comma separated for multi-port slots (client)")
	uuid := flag.String("uuid", "", "user uuid (client)")
	users := flag.String("users", "", "user key sources: comma separated txt/sqlite paths, e.g. /etc/s-ui/private/s-ui.db,/opt/zhdata/users.txt (server)")
	backend := flag.String("backend", "direct", `backend: "direct" or socks addr e.g. 127.0.0.1:12345 (server)`)
	maxClients := flag.Int("max-clients", 100, "max concurrent sessions per server (sessions = users x client -conns)")
	decoy := flag.String("decoy", "", "optional host:port to splice non-protocol HTTP-looking traffic (server)")
	verbose := flag.Bool("v", false, "verbose logging")
	conns := flag.Int("conns", 1, "parallel tunnel sessions 1-64 (client; independent slots/buckets)")
	spread := flag.Int("spread", 8, "max lanes one bulk upload stream may fan across (client; v0.11 retransmit makes fan-out safe; auto-capped to conns/live-bulk-streams, set 1 to disable)")
	train := flag.Int("train", 0, "seconds between per-session idle training frames; 0 = off (v0.8 experiment: keep ISP shaping buckets warm)")
	migrate := flag.Int("migrate", 1, "stalled/slow-lane whole-backlog migration: 1 = on (v0.11.18 default; confined to upload lanes when split pools active), 0 = off")
	dlconns := flag.Int("dlconns", -1, "split pools: last N sessions are download-only homes, rest upload-only lanes (-1 = auto conns/2 (v0.11.18 default), 0 = shared)")
	slotbase := flag.Int("slotbase", 0, "first slot index for this instance (v0.11.20; run two clients side by side, e.g. -conns 32 -slotbase 32, without takeover conflicts)")
	pprofAddr := flag.String("pprof", "", "enable net/http/pprof (goroutine/heap dumps) on this address, e.g. 127.0.0.1:6060; empty = off")
	showVer := flag.Bool("version", false, "print version")
	flag.Parse()

	if *showVer {
		fmt.Println("zhdata-tunnel", version)
		return
	}
	log.SetFlags(log.LstdFlags)
	if *pprofAddr != "" {
		go func() {
			log.Printf("[pprof] listening on http://%s/debug/pprof/", *pprofAddr)
			if err := http.ListenAndServe(*pprofAddr, nil); err != nil {
				log.Printf("[pprof] server: %v", err)
			}
		}()
	}

	switch *mode {
	case "server":
		if *listen == "" || *users == "" {
			fatal("server mode requires -listen and -users")
		}
		app, err := newServerApp(serverConfig{
			listen:     *listen,
			usersPath:  *users,
			backend:    *backend,
			maxClients: *maxClients,
			decoy:      *decoy,
			verbose:    *verbose,
		})
		if err != nil {
			fatal("server: %v", err)
		}
		waitSignal()
		app.Close()
	case "client":
		if *listen == "" || *server == "" || *uuid == "" {
			fatal("client mode requires -listen, -server and -uuid")
		}
		app, err := newClientApp(clientConfig{
			listen:   *listen,
			server:   *server,
			uuid:     *uuid,
			conns:    *conns,
			spread:   *spread,
			train:    *train,
			migrate:  *migrate,
			dlconns:  *dlconns,
			slotbase: *slotbase,
			verbose:  *verbose,
		})
		if err != nil {
			fatal("client: %v", err)
		}
		log.Printf("[client] socks5 listening on %s -> %s", app.Addr(), *server)
		waitSignal()
		app.Close()
	default:
		fatal("unknown mode %q", *mode)
	}
}

func fatal(format string, v ...any) {
	log.Printf(format, v...)
	os.Exit(1)
}

func waitSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}
