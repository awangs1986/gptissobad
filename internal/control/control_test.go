package control_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/awangs1986/gptissobad/internal/control"
)

func TestControlPageCanSaveConfigAndToggleGateway(t *testing.T) {
	dir := t.TempDir()
	rt := control.New(control.Paths{Dir: dir})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	ui := httptest.NewServer(control.Handler(rt, control.FrontendFS()))
	t.Cleanup(ui.Close)
	t.Cleanup(rt.Stop)

	page, err := http.Get(ui.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	_ = page.Body.Close()
	if page.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("Local Gateway")) {
		t.Fatalf("control page missing: %d %s", page.StatusCode, body)
	}

	cfg, err := json.Marshal(map[string]string{
		"upstream":    upstream.URL,
		"gatewayPort": "0",
		"model":       "mimo-v2.5",
		"apiKey":      "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, ui.URL+"/api/config", bytes.NewReader(cfg))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("save config %d %s", res.StatusCode, saved)
	}
	if bytes.Contains(saved, []byte("test-key")) {
		t.Fatalf("API key leaked in state: %s", saved)
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode-go.key")); err != nil {
		t.Fatalf("key file not written: %v", err)
	}

	enable, _ := json.Marshal(map[string]bool{"enabled": true})
	res, err = http.Post(ui.URL+"/api/enabled", "application/json", bytes.NewReader(enable))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("enable %d %s", res.StatusCode, out)
	}

	var state control.State
	if err := json.Unmarshal(out, &state); err != nil {
		t.Fatal(err)
	}
	if !state.Running || !state.Enabled {
		t.Fatalf("gateway not running: %+v", state)
	}
	if !state.HasKey {
		t.Fatal("hasKey should be true")
	}

	disable, _ := json.Marshal(map[string]bool{"enabled": false})
	res, err = http.Post(ui.URL+"/api/enabled", "application/json", bytes.NewReader(disable))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("disable %d", res.StatusCode)
	}
	if rt.State().Running {
		t.Fatal("gateway still running after disable")
	}
}

func TestUpdateConfigRejectsBadUpstreamAndPort(t *testing.T) {
	dir := t.TempDir()
	rt := control.New(control.Paths{Dir: dir})
	ui := httptest.NewServer(control.Handler(rt, control.FrontendFS()))
	t.Cleanup(ui.Close)

	put := func(body string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, ui.URL+"/api/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		return res.StatusCode
	}

	if got := put(`{"upstream":"not-a-url"}`); got != http.StatusBadRequest {
		t.Fatalf("bad upstream status %d", got)
	}
	if got := put(`{"gatewayPort":"abc"}`); got != http.StatusBadRequest {
		t.Fatalf("bad port status %d", got)
	}
	if rt.State().Upstream != "" {
		t.Fatalf("rejected upstream was stored: %q", rt.State().Upstream)
	}
}

func TestRuntimeUsesPiOpenCodeGoLoginWithoutImportingOrdinaryUpstreamConfig(t *testing.T) {
	dir := t.TempDir()
	codexDir := filepath.Join(dir, "codex")
	piAuth := filepath.Join(dir, "pi", "agent", "auth.json")
	if err := os.MkdirAll(filepath.Dir(piAuth), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(piAuth, []byte(`{"opencode-go":{"type":"api_key","key":"pi-key"},"openai":{"type":"api_key","key":"wrong-key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pi", "agent", "models.json"), []byte(`{"providers":{"openai":{"baseUrl":"https://wrong.example/v1","apiKey":"wrong-key"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_API_KEY", "")

	rt := control.New(control.Paths{Dir: codexDir, PiAuthFile: piAuth})
	state := rt.State()
	if !state.HasKey || state.KeySource != "pi-login" {
		t.Fatalf("state = %+v", state)
	}
	if _, err := os.Stat(filepath.Join(codexDir, "opencode-go.key")); !os.IsNotExist(err) {
		t.Fatalf("Pi login was copied into codex key file: %v", err)
	}
}

func TestSetFrontSplitsPortStartsHealthzAndRestores(t *testing.T) {
	dir := t.TempDir()
	rt := control.New(control.Paths{Dir: dir})
	t.Cleanup(rt.Stop)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()

	if err := rt.UpdateConfig(control.ConfigInput{
		Upstream:    "http://127.0.0.1:9",
		GatewayPort: port,
		FrontPort:   port,
	}); err != nil {
		t.Fatal(err)
	}
	if err := rt.SetFront(true); err != nil {
		t.Fatal(err)
	}
	state := rt.State()
	if !state.FrontEnabled || !state.FrontReachable {
		t.Fatalf("front not up: %+v", state)
	}
	if state.GatewayPort == port {
		t.Fatalf("real gateway stayed on Codex port %s", port)
	}
	if !strings.Contains(state.Listen, state.FrontPort) || !strings.Contains(state.TOML, state.FrontPort) {
		t.Fatalf("Codex-facing listen/TOML still internal: listen=%s toml=%s", state.Listen, state.TOML)
	}
	resp, err := http.Get("http://" + state.FrontListen + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"ok":true`)) {
		t.Fatalf("healthz %d %s", resp.StatusCode, body)
	}

	if err := rt.SetFront(false); err != nil {
		t.Fatal(err)
	}
	state = rt.State()
	if state.FrontEnabled || state.FrontReachable {
		t.Fatalf("front still on: %+v", state)
	}
	if state.GatewayPort != port {
		t.Fatalf("direct gateway port %s, want restored %s", state.GatewayPort, port)
	}
	if _, err := http.Get("http://127.0.0.1:" + port + "/healthz"); err == nil {
		t.Fatal("front still answering after disable")
	}
}

// A front that already answers healthz honours the 403 contract, so enabling
// the front must adopt it. Signalling it first left Codex with nothing
// listening while the port drained.
func TestSetFrontAdoptsHealthyFrontInsteadOfSignallingIt(t *testing.T) {
	dir := t.TempDir()
	healthLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	health := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})}
	go func() { _ = health.Serve(healthLn) }()
	t.Cleanup(func() { _ = health.Close() })
	port := strconv.Itoa(healthLn.Addr().(*net.TCPAddr).Port)

	// Our own pid is never signalled, so the file surviving is the signal
	// that the adopt branch ran rather than the kill branch.
	pidFile := filepath.Join(dir, "fronthost.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rt := control.New(control.Paths{Dir: dir})
	t.Cleanup(rt.Stop)
	if err := rt.UpdateConfig(control.ConfigInput{
		Upstream:    "http://127.0.0.1:9",
		GatewayPort: port,
		FrontPort:   port,
	}); err != nil {
		t.Fatal(err)
	}
	if err := rt.SetFront(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatalf("pid file gone: the healthy front was signalled instead of adopted")
	}
	resp, err := http.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		t.Fatalf("adopted front stopped serving: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("adopted front healthz %d", resp.StatusCode)
	}
}

func TestFrontReachableRequiresHealthzNotBareTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	dir := t.TempDir()
	raw := []byte(`{"frontEnabled":true,"frontPort":"` + port + `","gatewayPort":"18788"}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "codex-lang.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rt := control.New(control.Paths{Dir: dir})
	if rt.State().FrontReachable {
		t.Fatal("bare TCP listener must not count as reachable")
	}

	healthLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	health := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})}
	go func() { _ = health.Serve(healthLn) }()
	t.Cleanup(func() { _ = health.Close() })
	healthPort := strconv.Itoa(healthLn.Addr().(*net.TCPAddr).Port)
	raw = []byte(`{"frontEnabled":true,"frontPort":"` + healthPort + `","gatewayPort":"18788"}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "codex-lang.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rt = control.New(control.Paths{Dir: dir})
	if !rt.State().FrontReachable {
		t.Fatal("GET /healthz 200 should count as reachable")
	}
}

func TestEnableGatewayMovesOffOccupiedCodexPort(t *testing.T) {
	dir := t.TempDir()
	rt := control.New(control.Paths{Dir: dir})
	t.Cleanup(rt.Stop)

	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	port := strconv.Itoa(held.Addr().(*net.TCPAddr).Port)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	if err := rt.UpdateConfig(control.ConfigInput{
		Upstream:    upstream.URL,
		GatewayPort: port,
		FrontPort:   port,
	}); err != nil {
		t.Fatal(err)
	}
	if err := rt.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	state := rt.State()
	if !state.Running {
		t.Fatal("gateway not running")
	}
	if state.GatewayPort == port {
		t.Fatalf("gateway stayed on occupied Codex port %s", port)
	}
}
