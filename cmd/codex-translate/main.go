package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/awangs1986/gptissobad/internal/gateway"
	"github.com/awangs1986/gptissobad/internal/opencodego"
)

const defaultPort = "18787"

func main() {
	log.SetFlags(0)
	port := envOr("CODEX_TRANSLATE_PORT", defaultPort)
	if len(os.Args) > 1 && os.Args[1] == "print-config" {
		fmt.Print(gateway.ConfigTOML(port))
		return
	}
	upstream := strings.TrimSpace(os.Getenv("CODEX_TRANSLATE_UPSTREAM"))
	if err := validUpstream(upstream); err != nil {
		log.Fatal(err)
	}
	cfg := gateway.Config{
		Upstream: upstream,
		APIKey:   loadTranslatorKey(),
		Model:    envOr("CODEX_TRANSLATE_MODEL", "mimo-v2.5"),
		// Opt-in only here; the Watchdog enables spark by default.
		FallbackModel: envOr("CODEX_TRANSLATE_FALLBACK_MODEL", ""),
		CacheFile:     translateCachePath(),
		// Read-only: only the Watchdog gateway persists, so two
		// processes never clobber each other's full-map snapshots.
		CacheReadOnly: true,
	}
	addr := net.JoinHostPort("127.0.0.1", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("listening %s upstream=%s model=%s", addr, upstream, cfg.Model)
	srv := &http.Server{Handler: gateway.New(cfg), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil {
		log.Fatal(err)
	}
}

func validUpstream(raw string) error {
	if raw == "" {
		return fmt.Errorf("CODEX_TRANSLATE_UPSTREAM is required (next-hop OpenAI-compatible base URL)")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("CODEX_TRANSLATE_UPSTREAM must be an http(s) URL with a host")
	}
	return nil
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func loadTranslatorKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		key, _ := opencodego.ResolveKey("", "")
		return key
	}
	key, _ := opencodego.ResolveKey(
		filepath.Join(home, ".codex", "opencode-go.key"),
		filepath.Join(home, ".pi", "agent", "auth.json"),
	)
	return key
}

func translateCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "translate-cache.json")
}
