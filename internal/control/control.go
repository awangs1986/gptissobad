package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/awangs1986/gptissobad/internal/fronthost"
	"github.com/awangs1986/gptissobad/internal/gateway"
	"github.com/awangs1986/gptissobad/internal/opencodego"
)

const (
	DefaultUIPort              = "18786"
	DefaultGatewayPort         = "18787"
	DefaultFrontPort           = "18787"
	DefaultInternalGatewayPort = "18788"
	DefaultModel               = "mimo-v2.5"
	// DefaultFallbackModel retries a Han-bearing primary translation once
	// via the Responses dialect. Pre-authorized contingency for recurring
	// answer-instead-of-translate failures; blank it ("-") to disable.
	DefaultFallbackModel = "muse-spark-1.3-contributor"
	DefaultBasePath      = "/v1"
	maxLogLines          = 400
	// frontPortReleaseWait bounds how long we wait for a signalled front to
	// release its port before giving up and reporting the port unusable.
	frontPortReleaseWait = 2 * time.Second
	// maxContextTokens bounds the Control Page's upstream-context field:
	// anything past this is a typo, not a model.
	maxContextTokens = 2000000
)

type Settings struct {
	Enabled       bool   `json:"enabled"`
	UIPort        string `json:"uiPort"`
	GatewayPort   string `json:"gatewayPort"`
	Upstream      string `json:"upstream"`
	Model         string `json:"model"`
	FallbackModel string `json:"fallbackModel"`
	// UpstreamContextTokens is the upstream model's context window in
	// tokens; 0 (default) disables the gateway's coverage check.
	UpstreamContextTokens int    `json:"upstreamContextTokens"`
	FrontEnabled          bool   `json:"frontEnabled"`
	FrontPort             string `json:"frontPort"`
	BasePath              string `json:"basePath"`
	DirectGatewayPort     string `json:"directGatewayPort,omitempty"`
}

type Paths struct {
	Dir        string
	PiAuthFile string
}

func DefaultPaths() Paths {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{Dir: ".codex"}
	}
	return Paths{
		Dir:        filepath.Join(home, ".codex"),
		PiAuthFile: filepath.Join(home, ".pi", "agent", "auth.json"),
	}
}

func (p Paths) settingsFile() string       { return filepath.Join(p.Dir, "codex-lang.json") }
func (p Paths) keyFile() string            { return filepath.Join(p.Dir, "opencode-go.key") }
func (p Paths) frontPIDFile() string       { return fronthost.PIDPath(p.Dir) }
func (p Paths) translateCacheFile() string { return filepath.Join(p.Dir, "translate-cache.json") }

type LogLine struct {
	Seq  int    `json:"seq"`
	Time string `json:"time"`
	Msg  string `json:"msg"`
}

type Runtime struct {
	paths Paths

	mu            sync.Mutex
	settings      Settings
	hasKey        bool
	listener      net.Listener
	server        *http.Server
	gateway       *gateway.Gateway
	frontListener net.Listener
	frontServer   *http.Server

	logMu sync.Mutex
	logs  []LogLine
	seq   int
	wait  []chan struct{}
}

func New(paths Paths) *Runtime {
	r := &Runtime{paths: paths}
	r.settings = Settings{
		UIPort:        DefaultUIPort,
		GatewayPort:   DefaultGatewayPort,
		Model:         DefaultModel,
		FallbackModel: DefaultFallbackModel,
		FrontPort:     DefaultFrontPort,
		BasePath:      DefaultBasePath,
	}
	r.load()
	return r
}

func (r *Runtime) load() {
	raw, err := os.ReadFile(r.paths.settingsFile())
	if err == nil {
		var s Settings
		if json.Unmarshal(raw, &s) == nil {
			if s.UIPort == "" {
				s.UIPort = DefaultUIPort
			}
			if s.GatewayPort == "" {
				s.GatewayPort = DefaultGatewayPort
			}
			if s.Model == "" {
				s.Model = DefaultModel
			}
			if s.FallbackModel == "" {
				s.FallbackModel = DefaultFallbackModel
			}
			if s.FrontPort == "" {
				s.FrontPort = DefaultFrontPort
			}
			if s.BasePath == "" {
				s.BasePath = DefaultBasePath
			}
			r.settings = s
		}
	}
	key, _ := r.readKey()
	r.hasKey = key != ""
}

func (r *Runtime) persistLocked() error {
	if err := os.MkdirAll(r.paths.Dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r.settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.paths.settingsFile(), append(raw, '\n'), 0o600)
}

func (r *Runtime) readKey() (string, opencodego.KeySource) {
	return opencodego.ResolveKey(r.paths.keyFile(), r.paths.PiAuthFile)
}

// mimoModel reports whether the configured model is served by the Xiaomi
// direct route rather than the OpenCode Go chain.
func mimoModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "mimo")
}

// readTranslatorKey returns the credential the *configured* route will use,
// plus a label for the page. A mimo model prefers the Xiaomi key (file or
// environment) because that is exactly what gateway.translatorTarget() picks;
// reporting the OpenCode credential alone made a working mimo-only setup look
// keyless in the Control Page and in the watchdog's tray colour.
func (r *Runtime) readTranslatorKey() (string, string) {
	if mimoModel(r.settings.Model) {
		if k := strings.TrimSpace(gateway.ResolveMimoKey()); k != "" {
			return k, "mimo-key"
		}
	}
	key, source := r.readKey()
	return key, string(source)
}

func (r *Runtime) writeKey(key string) error {
	if err := os.MkdirAll(r.paths.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(r.paths.keyFile(), []byte(strings.TrimSpace(key)+"\n"), 0o600)
}

func (r *Runtime) Log(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	r.logMu.Lock()
	r.seq++
	line := LogLine{
		Seq:  r.seq,
		Time: time.Now().Format("15:04:05"),
		Msg:  msg,
	}
	r.logs = append(r.logs, line)
	if len(r.logs) > maxLogLines {
		r.logs = r.logs[len(r.logs)-maxLogLines:]
	}
	waiters := r.wait
	r.wait = nil
	r.logMu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

func (r *Runtime) LogsAfter(seq int) []LogLine {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	for i, line := range r.logs {
		if line.Seq > seq {
			out := make([]LogLine, len(r.logs)-i)
			copy(out, r.logs[i:])
			return out
		}
	}
	return nil
}

func (r *Runtime) waitLogs(seq int) <-chan struct{} {
	ch := make(chan struct{})
	r.logMu.Lock()
	if len(r.logs) > 0 && r.logs[len(r.logs)-1].Seq > seq {
		r.logMu.Unlock()
		close(ch)
		return ch
	}
	r.wait = append(r.wait, ch)
	r.logMu.Unlock()
	return ch
}

type State struct {
	Enabled               bool            `json:"enabled"`
	Running               bool            `json:"running"`
	Listen                string          `json:"listen"`
	FrontListen           string          `json:"frontListen,omitempty"`
	UIListen              string          `json:"uiListen"`
	Upstream              string          `json:"upstream"`
	GatewayPort           string          `json:"gatewayPort"`
	UIPort                string          `json:"uiPort"`
	Model                 string          `json:"model"`
	FallbackModel         string          `json:"fallbackModel"`
	UpstreamContextTokens int             `json:"upstreamContextTokens"`
	FrontEnabled          bool            `json:"frontEnabled"`
	FrontPort             string          `json:"frontPort"`
	BasePath              string          `json:"basePath"`
	HasKey                bool            `json:"hasKey"`
	FrontReachable        bool            `json:"frontReachable"`
	Translating           bool            `json:"translating"`
	TOML                  string          `json:"toml"`
	Error                 string          `json:"error,omitempty"`
	Metrics               gateway.Metrics `json:"metrics"`
	KeySource             string          `json:"credentialSource"`
}

func (r *Runtime) State() State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stateLocked()
}

func (r *Runtime) stateLocked() State {
	var metrics gateway.Metrics
	if r.gateway != nil {
		metrics = r.gateway.Metrics()
	}
	key, keySource := r.readTranslatorKey()
	// Codex always points at the front door when it is enabled; the TOML
	// and the listen line must show the front port, never the internal
	// real-gateway port.
	tomlPort := r.settings.GatewayPort
	listenPort := r.settings.GatewayPort
	frontListen := ""
	if r.settings.FrontEnabled {
		tomlPort = r.settings.FrontPort
		listenPort = r.settings.FrontPort
		frontListen = net.JoinHostPort("127.0.0.1", r.settings.FrontPort)
	}
	return State{
		Enabled:               r.settings.Enabled,
		Running:               r.listener != nil,
		Listen:                net.JoinHostPort("127.0.0.1", listenPort),
		FrontListen:           frontListen,
		UIListen:              net.JoinHostPort("127.0.0.1", r.settings.UIPort),
		Upstream:              r.settings.Upstream,
		GatewayPort:           r.settings.GatewayPort,
		UIPort:                r.settings.UIPort,
		Model:                 r.settings.Model,
		FallbackModel:         r.settings.FallbackModel,
		UpstreamContextTokens: r.settings.UpstreamContextTokens,
		FrontEnabled:          r.settings.FrontEnabled,
		FrontPort:             r.settings.FrontPort,
		BasePath:              r.settings.BasePath,
		HasKey:                key != "",
		FrontReachable:        r.frontReachableLocked(),
		Translating:           metrics.Translating,
		KeySource:             keySource,
		TOML:                  gateway.ConfigTOMLFor(tomlPort, r.settings.BasePath),
		Metrics:               metrics,
	}
}

type ConfigInput struct {
	Upstream      string `json:"upstream"`
	GatewayPort   string `json:"gatewayPort"`
	UIPort        string `json:"uiPort"`
	Model         string `json:"model"`
	FallbackModel string `json:"fallbackModel"`
	APIKey        string `json:"apiKey"`
	// Pointer so 0 (disable) differs from "field not sent".
	UpstreamContextTokens *int   `json:"upstreamContextTokens"`
	FrontPort             string `json:"frontPort"`
	BasePath              string `json:"basePath"`
}

func (r *Runtime) UpdateConfig(in ConfigInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := strings.TrimSpace(in.GatewayPort); p != "" {
		if err := validPort(p); err != nil {
			return err
		}
		r.settings.GatewayPort = p
	}
	if p := strings.TrimSpace(in.UIPort); p != "" {
		if err := validPort(p); err != nil {
			return err
		}
		r.settings.UIPort = p
	}
	if u := strings.TrimSpace(in.Upstream); u != "" {
		if err := validUpstream(u); err != nil {
			return err
		}
		r.settings.Upstream = u
	}
	if m := strings.TrimSpace(in.Model); m != "" {
		r.settings.Model = m
	}
	if f := strings.TrimSpace(in.FallbackModel); f != "" {
		if f == "-" {
			r.settings.FallbackModel = ""
		} else {
			r.settings.FallbackModel = f
		}
	}
	if in.UpstreamContextTokens != nil {
		n := *in.UpstreamContextTokens
		if n != 0 && (n < 1000 || n > maxContextTokens) {
			return fmt.Errorf("上游上下文上限要填 0（关闭）或 1000–%d 之间的整数", maxContextTokens)
		}
		r.settings.UpstreamContextTokens = n
	}
	if p := strings.TrimSpace(in.FrontPort); p != "" {
		if err := validPort(p); err != nil {
			return err
		}
		r.settings.FrontPort = p
	}
	if b := normalizeBasePath(in.BasePath); b != "" {
		r.settings.BasePath = b
	}
	if r.settings.FrontEnabled {
		r.settings.GatewayPort = splitGatewayPort(r.settings.FrontPort, r.settings.GatewayPort)
	}
	if key := strings.TrimSpace(in.APIKey); key != "" {
		if err := r.writeKey(key); err != nil {
			return err
		}
		r.hasKey = true
	} else {
		key, _ := r.readKey()
		r.hasKey = key != ""
	}
	if err := r.persistLocked(); err != nil {
		return err
	}
	if r.settings.Enabled {
		if err := r.restartLocked(); err != nil {
			return err
		}
	}
	if r.settings.FrontEnabled {
		if err := r.startFrontLocked(); err != nil {
			return err
		}
	}
	r.Log("saved config")
	return nil
}

func (r *Runtime) SetEnabled(enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settings.Enabled = enabled
	if err := r.persistLocked(); err != nil {
		return err
	}
	if !enabled {
		r.stopLocked()
		r.Log("Local Gateway stopped")
		return nil
	}
	if r.settings.FrontEnabled {
		r.settings.GatewayPort = splitGatewayPort(r.settings.FrontPort, r.settings.GatewayPort)
	}
	if err := r.startLocked(); err != nil {
		r.settings.Enabled = false
		_ = r.persistLocked()
		return err
	}
	if r.settings.FrontEnabled {
		if err := r.startFrontLocked(); err != nil {
			r.Log("Front door start failed: " + err.Error())
		}
	}
	r.Log("Local Gateway listening on " + net.JoinHostPort("127.0.0.1", r.settings.GatewayPort))
	return nil
}

func (r *Runtime) SetFront(enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if enabled {
		if !r.settings.FrontEnabled {
			r.settings.DirectGatewayPort = r.settings.GatewayPort
			r.settings.GatewayPort = splitGatewayPort(r.settings.FrontPort, r.settings.GatewayPort)
		}
		r.settings.FrontEnabled = true
		if r.settings.Enabled {
			if err := r.restartLocked(); err != nil {
				return err
			}
		}
		if err := r.startFrontLocked(); err != nil {
			return err
		}
		if err := r.persistLocked(); err != nil {
			return err
		}
		r.Log("Front door enabled: Codex faces " + net.JoinHostPort("127.0.0.1", r.settings.FrontPort) + r.settings.BasePath)
		return nil
	}
	r.stopFrontLocked()
	r.settings.FrontEnabled = false
	if r.settings.DirectGatewayPort != "" {
		r.settings.GatewayPort = r.settings.DirectGatewayPort
		r.settings.DirectGatewayPort = ""
	}
	if err := r.persistLocked(); err != nil {
		return err
	}
	if r.settings.Enabled {
		if err := r.restartLocked(); err != nil {
			return err
		}
	}
	r.Log("Front door disabled: Codex faces the real gateway directly")
	return nil
}

func (r *Runtime) CheckTranslator(ctx context.Context) error {
	r.mu.Lock()
	key, _ := r.readKey()
	model := r.settings.Model
	r.mu.Unlock()
	// gateway.CheckTranslator is route-aware: it probes whichever credential
	// the configured model actually needs, so a mimo-only setup gets a real
	// check instead of being refused for a missing OpenCode Go key.
	return gateway.New(gateway.Config{APIKey: key, Model: model}).CheckTranslator(ctx)
}

func (r *Runtime) StartIfEnabled() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settings.FrontEnabled {
		next := splitGatewayPort(r.settings.FrontPort, r.settings.GatewayPort)
		if next != r.settings.GatewayPort {
			r.settings.GatewayPort = next
			_ = r.persistLocked()
		}
	}
	var err error
	if r.settings.Enabled {
		err = r.startLocked()
	}
	if r.settings.FrontEnabled {
		if ferr := r.startFrontLocked(); ferr != nil && err == nil {
			err = ferr
		}
	}
	return err
}

func (r *Runtime) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopFrontOwnedLocked()
	r.stopLocked()
}

func validPort(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil || n < 0 || n > 65535 {
		return errors.New("端口要是 0-65535 的数字")
	}
	return nil
}

// normalizeBasePath cleans the Codex-facing base path ("/v1" for a
// Cockpit-style upstream, "/backend-api/codex" for direct ChatGPT).
// Empty input means "no change" and stays empty; ConfigTOMLFor falls back
// to DefaultBasePath for empty paths.
func normalizeBasePath(raw string) string {
	b := strings.TrimSpace(raw)
	if b == "" {
		return ""
	}
	if !strings.HasPrefix(b, "/") {
		b = "/" + b
	}
	b = strings.TrimRight(b, "/")
	if b == "" {
		return "/"
	}
	return b
}

func validUpstream(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("需要配置上游 Base URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("上游地址解析不了")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("上游地址要以 http:// 或 https:// 开头")
	}
	if u.Host == "" {
		return errors.New("上游地址缺少主机名")
	}
	return nil
}

func (r *Runtime) startLocked() error {
	if r.listener != nil {
		return nil
	}
	upstream := strings.TrimSpace(r.settings.Upstream)
	if err := validUpstream(upstream); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", r.settings.GatewayPort))
	if err != nil {
		tried := map[string]bool{r.settings.GatewayPort: true}
		for _, next := range gatewayPortFallbacks(r.settings.FrontPort, r.settings.GatewayPort) {
			if next == "" || tried[next] {
				continue
			}
			tried[next] = true
			var ln2 net.Listener
			var err2 error
			if next == "0" {
				ln2, err2 = net.Listen("tcp", "127.0.0.1:0")
			} else {
				ln2, err2 = net.Listen("tcp", net.JoinHostPort("127.0.0.1", next))
			}
			if err2 != nil {
				continue
			}
			actual := next
			if next == "0" {
				actual = strconv.Itoa(ln2.Addr().(*net.TCPAddr).Port)
			}
			r.settings.GatewayPort = actual
			_ = r.persistLocked()
			r.Log("Local Gateway moved to " + net.JoinHostPort("127.0.0.1", actual) + " because the Codex port is already taken")
			ln, err = ln2, nil
			break
		}
	}
	if err != nil {
		return fmt.Errorf("网关端口不可用: %w", err)
	}
	key, _ := r.readKey()
	gw := gateway.New(gateway.Config{
		Upstream:              upstream,
		APIKey:                key,
		Model:                 r.settings.Model,
		FallbackModel:         r.settings.FallbackModel,
		UpstreamContextTokens: r.settings.UpstreamContextTokens,
		CacheFile:             r.paths.translateCacheFile(),
		Log:                   r.Log,
	})
	r.listener = ln
	r.gateway = gw
	// Capture the server in the goroutine: stopLocked() nils r.server, and a
	// fast stop (tests, restart, disable) used to race with this Serve call
	// and panic on the nil pointer.
	srv := &http.Server{Handler: gw, ReadHeaderTimeout: 10 * time.Second}
	r.server = srv
	go func() {
		_ = srv.Serve(ln)
	}()
	return nil
}

func (r *Runtime) restartLocked() error {
	r.stopLocked()
	if !r.settings.Enabled {
		return nil
	}
	return r.startLocked()
}

func (r *Runtime) stopLocked() {
	if r.server != nil {
		_ = r.server.Close()
	}
	// Flush pending translations synchronously: covers restart, disable,
	// SIGTERM/SIGINT and logout. Only kill -9 and power loss can drop the
	// last debounce window, which costs a re-translation, not correctness.
	if r.gateway != nil {
		r.gateway.FlushCache()
	}
	r.server = nil
	r.listener = nil
	r.gateway = nil
}

// frontReachableLocked reports whether Codex currently gets an HTTP
// answer from GET /healthz instead of a refused connection. A bare TCP
// accept is not enough: that would light up for any occupant of the port.
func (r *Runtime) frontReachableLocked() bool {
	if !r.settings.FrontEnabled {
		return false
	}
	return r.frontHealthzOKLocked()
}

func (r *Runtime) frontHealthzOKLocked() bool {
	port := strings.TrimSpace(r.settings.FrontPort)
	if port == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort("127.0.0.1", port)+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func splitGatewayPort(frontPort, gatewayPort string) string {
	frontPort = strings.TrimSpace(frontPort)
	gatewayPort = strings.TrimSpace(gatewayPort)
	if gatewayPort == "" {
		gatewayPort = DefaultGatewayPort
	}
	if frontPort == "" {
		frontPort = DefaultFrontPort
	}
	if gatewayPort != frontPort {
		return gatewayPort
	}
	if frontPort == "0" {
		return "0"
	}
	if frontPort == DefaultGatewayPort || frontPort == DefaultFrontPort {
		return DefaultInternalGatewayPort
	}
	n, err := strconv.Atoi(frontPort)
	if err != nil || n < 0 || n >= 65535 {
		return DefaultInternalGatewayPort
	}
	return strconv.Itoa(n + 1)
}

// gatewayPortFallbacks orders escape ports when the configured gateway
// port is taken: the split result, then the conventional internal port,
// then a short scan above the split result, then ":0" which the kernel
// guarantees. The scan exists because a single computed fallback can lose
// a bind race exactly like the original port did (parallel test binaries
// and ephemeral neighbours); without it the suite flakes.
func gatewayPortFallbacks(frontPort, gatewayPort string) []string {
	out := []string{
		splitGatewayPort(frontPort, gatewayPort),
		DefaultInternalGatewayPort,
	}
	if n, err := strconv.Atoi(splitGatewayPort(frontPort, gatewayPort)); err == nil && n > 0 && n < 65535-8 {
		for i := 1; i <= 8; i++ {
			out = append(out, strconv.Itoa(n+i))
		}
	}
	return append(out, "0")
}

func (r *Runtime) startFrontLocked() error {
	r.stopFrontOwnedLocked()
	backend := "http://127.0.0.1:" + r.settings.GatewayPort
	front, err := fronthost.New(fronthost.Config{BackendURL: backend})
	if err != nil {
		return err
	}
	addr := net.JoinHostPort("127.0.0.1", r.settings.FrontPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// A healthy front on the port already serves the 403 contract, so
		// adopt it. Signalling it first would only open a window where
		// nothing answers Codex.
		if r.frontHealthzOKLocked() {
			r.Log("Front door already listening on " + addr)
			return nil
		}
		// Something is squatting the port without serving healthz. Signal
		// it, then wait for the port back: the OS does not release it the
		// moment the process is signalled.
		r.killFrontPIDLocked()
		ln, err = listenWhenFree(addr, frontPortReleaseWait)
		if err != nil {
			return fmt.Errorf("前置端口不可用: %w", err)
		}
	}
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok && r.settings.FrontPort == "0" {
		r.settings.FrontPort = strconv.Itoa(tcp.Port)
	}
	r.frontListener = ln
	frontSrv := &http.Server{Handler: front, ReadHeaderTimeout: 10 * time.Second}
	r.frontServer = frontSrv
	go func() {
		_ = frontSrv.Serve(ln)
	}()
	r.Log("Front door listening on " + ln.Addr().String() + " -> " + backend)
	return nil
}

// listenWhenFree retries the bind until a signalled squatter lets the port go.
func listenWhenFree(addr string, wait time.Duration) (net.Listener, error) {
	deadline := time.Now().Add(wait)
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (r *Runtime) stopFrontOwnedLocked() {
	if r.frontServer != nil {
		_ = r.frontServer.Close()
	}
	r.frontServer = nil
	r.frontListener = nil
}

func (r *Runtime) stopFrontLocked() {
	r.stopFrontOwnedLocked()
	r.killFrontPIDLocked()
}

func (r *Runtime) killFrontPIDLocked() {
	raw, err := os.ReadFile(r.paths.frontPIDFile())
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		_ = os.Remove(r.paths.frontPIDFile())
		return
	}
	proc, err := os.FindProcess(pid)
	if err == nil {
		if runtime.GOOS == "windows" {
			_ = proc.Kill()
		} else {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}
	_ = os.Remove(r.paths.frontPIDFile())
}
