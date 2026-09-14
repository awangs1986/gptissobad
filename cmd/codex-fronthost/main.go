// Command codex-fronthost is the light Codex-facing front door in front of
// the real Local Gateway.
//
// It binds 127.0.0.1 only, proxies everything to the real gateway on
// loopback, and returns HTTP 403 (never a refused connection) for any
// Codex turn while the real gateway is unreachable. It never touches
// translation, keys, or prompt bodies beyond proxying bytes.
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/awangs1986/gptissobad/internal/fronthost"
)

func main() {
	log.SetFlags(0)
	port := flag.String("port", envOr("CODEX_FRONT_PORT", "18787"), "loopback port Codex points at")
	backend := flag.String("backend", envOr("CODEX_GATEWAY_BACKEND", "http://127.0.0.1:18788"), "real gateway base URL (loopback only)")
	flag.Parse()

	front, err := fronthost.New(fronthost.Config{BackendURL: *backend})
	if err != nil {
		log.Fatal(err)
	}
	addr := net.JoinHostPort("127.0.0.1", strings.TrimSpace(*port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("front port %s unavailable (real gateway or another front already there?): %v", addr, err)
	}
	if err := fronthost.WritePID(""); err != nil {
		log.Printf("pid file: %v", err)
	} else {
		defer fronthost.RemovePID("")
	}
	log.Printf("front %s -> %s", addr, strings.TrimSpace(*backend))
	srv := &http.Server{Handler: front, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	_ = srv.Close()
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
