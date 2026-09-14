// Package fronthost is the light Codex-facing front door in front of the
// real Local Gateway.
//
// It binds fast, never touches translation, and guarantees fail-closed:
// any Codex turn that cannot reach a healthy real gateway gets HTTP 403,
// never a refused connection and never an untranslated forward.
//
// Split of duties with the real gateway:
//   - front 403 ("网关未就绪") means the real gateway process is down;
//   - real-gateway 403 means the Translator Backend or upstream refused;
//   - either way Codex only ever sees 403, and no Han prose can leak while
//     any hop is unhealthy (the front never dials anyone except the real
//     gateway on loopback, and returns 403 without forwarding when the
//     backend is unreachable).
package fronthost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// FrontNotReadyMsg is returned with HTTP 403 when the real gateway
	// cannot be reached. It is deliberately different from the real
	// gateway messages so logs tell the two layers apart.
	FrontNotReadyMsg = "网关未就绪，这一轮未发送"
	// FrontWSRejectedMsg is returned with HTTP 403 for WebSocket upgrades.
	// It must not reuse FrontNotReadyMsg: the backend may be up.
	FrontWSRejectedMsg = "WebSocket 已关闭，这一轮未发送"
	// FrontNotReadyStatus matches the real gateway fail-closed status.
	FrontNotReadyStatus = http.StatusForbidden
	// minimalModelsID is only used when the backend is down and Codex
	// still asks for the model catalog. Codex falls back to default
	// metadata and keeps working; turns still get 403 until healthy.
	minimalModelsID = "codex"
)

type Config struct {
	// BackendURL is the real gateway base URL, e.g.
	// http://127.0.0.1:18788. Must be loopback http(s); anything else is
	// rejected so the front can never become an open proxy.
	BackendURL string
	// Client overrides the default HTTP client (mainly for tests).
	Client *http.Client
	// Log receives one line per refused/proxied request when set.
	// It never receives bodies, headers, or credentials.
	Log func(string)
}

type Front struct {
	backend *url.URL
	client  *http.Client
	log     func(string)
}

func New(cfg Config) (*Front, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.BackendURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("backend must be an http(s) URL with a host")
	}
	if !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("backend must be loopback, got %q", u.Hostname())
	}
	client := cfg.Client
	if client == nil {
		dialer := &net.Dialer{Timeout: 3 * time.Second}
		client = &http.Client{
			Transport: &http.Transport{
				DialContext:           dialer.DialContext,
				ResponseHeaderTimeout: 0,
			},
		}
	}
	return &Front{backend: u, client: client, log: cfg.Log}, nil
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func (f *Front) logf(format string, args ...any) {
	if f.log == nil {
		return
	}
	f.log(fmt.Sprintf(format, args...))
}

func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func (f *Front) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if isWebSocket(r) {
		f.logf("reject websocket %s %s", r.Method, r.URL.Path)
		http.Error(w, FrontWSRejectedMsg, FrontNotReadyStatus)
		return
	}
	if r.Method == http.MethodGet && isModelsPath(r.URL.Path) {
		f.serveModels(w, r)
		return
	}
	f.serveTurn(w, r)
}

func isModelsPath(path string) bool {
	return path == "/v1/models" || strings.HasSuffix(path, "/models")
}

// serveModels proxies the catalog when the backend is up. When it is down
// it returns a minimal catalog carrying BOTH the Codex `models` field and
// the OpenAI `data` field, so Codex starts with fallback metadata instead
// of failing before the first turn (turns still get 403 until healthy).
func (f *Front) serveModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out, err := http.NewRequestWithContext(ctx, http.MethodGet, f.backend.ResolveReference(&url.URL{Path: r.URL.Path, RawQuery: r.URL.RawQuery}).String(), nil)
	if err == nil {
		copyHeaders(out.Header, r.Header)
		if resp, err := f.client.Do(out); err == nil {
			defer resp.Body.Close()
			copyHeaders(w.Header(), resp.Header)
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
			return
		}
	}
	f.logf("models fallback %s", r.URL.Path)
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"models": []map[string]string{{"id": minimalModelsID}},
		"data":   []map[string]string{{"id": minimalModelsID, "object": "model"}},
	})
}

// serveTurn proxies any turn-like request to the real gateway. Only a
// transport-level failure (backend down) becomes a front 403; any HTTP
// status from the backend — including its own 403s — is preserved.
func (f *Front) serveTurn(w http.ResponseWriter, r *http.Request) {
	target := *r.URL
	dest := f.backend.ResolveReference(&url.URL{Path: target.Path, RawQuery: target.RawQuery})
	out, err := http.NewRequestWithContext(r.Context(), r.Method, dest.String(), r.Body)
	if err != nil {
		f.logf("fail-closed %s %s build", r.Method, r.URL.Path)
		http.Error(w, FrontNotReadyMsg, FrontNotReadyStatus)
		return
	}
	copyHeaders(out.Header, r.Header)
	out.Header.Del("Content-Length")
	if r.ContentLength >= 0 {
		out.ContentLength = r.ContentLength
	}
	out.Host = dest.Host
	resp, err := f.client.Do(out)
	if err != nil {
		f.logf("fail-closed %s %s backend unreachable", r.Method, r.URL.Path)
		http.Error(w, FrontNotReadyMsg, FrontNotReadyStatus)
		return
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if flusher, ok := w.(http.Flusher); ok {
		buf := make([]byte, 32*1024)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				flusher.Flush()
			}
			if readErr != nil {
				break
			}
		}
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopByHop(k) {
			continue
		}
		dst.Del(k)
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func hopByHop(k string) bool {
	switch http.CanonicalHeaderKey(k) {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Te", "Trailers", "Transfer-Encoding", "Upgrade", "Host":
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
