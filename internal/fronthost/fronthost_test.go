package fronthost_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/awangs1986/gptissobad/internal/fronthost"
)

func startFront(t *testing.T, backend string) *httptest.Server {
	t.Helper()
	front, err := fronthost.New(fronthost.Config{BackendURL: backend})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(front)
	t.Cleanup(srv.Close)
	return srv
}

func TestPostIsProxiedWhenBackendIsUp(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	}))
	t.Cleanup(upstream.Close)
	front := startFront(t, upstream.URL)

	resp, err := http.Post(front.URL+"/backend-api/codex/responses", "application/json", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(raw) != `{"input":"hello"}` {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
}

func TestBackend403IsPreserved(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "backend says no", http.StatusForbidden)
	}))
	t.Cleanup(upstream.Close)
	front := startFront(t, upstream.URL)

	resp, err := http.Post(front.URL+"/v1/responses", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(raw), "backend says no") {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
}

func TestPostIs403WhenBackendIsDown(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // nothing listens here now

	front := startFront(t, deadURL)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/responses"},
		{http.MethodPost, "/backend-api/codex/responses"},
		{http.MethodPost, "/v1/chat/completions"},
	} {
		req, _ := http.NewRequest(tc.method, front.URL+tc.path, strings.NewReader(`{"input":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s status %d, want 403", tc.method, tc.path, resp.StatusCode)
		}
		if !strings.Contains(string(raw), fronthost.FrontNotReadyMsg) {
			t.Fatalf("body %q missing front message", raw)
		}
	}
}

func TestModelsFallbackWhenBackendIsDown(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	front := startFront(t, deadURL)
	for _, path := range []string{"/v1/models", "/backend-api/codex/models"} {
		resp, err := http.Get(front.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status %d", path, resp.StatusCode)
		}
		// Codex requires the `models` field; OpenAI shape needs `data`.
		if !strings.Contains(string(raw), `"models"`) || !strings.Contains(string(raw), `"data"`) {
			t.Fatalf("GET %s body %s missing models/data", path, raw)
		}
	}
}

func TestHealthzAndWebsocket(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	front := startFront(t, deadURL)

	resp, err := http.Get(front.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("healthz %d %s", resp.StatusCode, raw)
	}

	post, err := http.Post(front.URL+"/healthz", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = post.Body.Close()
	if post.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz status %d, want 405", post.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, front.URL+"/v1/responses", nil)
	req.Header.Set("Upgrade", "websocket")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	wsBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("websocket status %d, want 403", resp.StatusCode)
	}
	if !strings.Contains(string(wsBody), fronthost.FrontWSRejectedMsg) {
		t.Fatalf("websocket body %q missing WS message", wsBody)
	}
	if strings.Contains(string(wsBody), fronthost.FrontNotReadyMsg) {
		t.Fatalf("websocket body reused front-not-ready text: %q", wsBody)
	}
}

func TestNonLoopbackBackendIsRejected(t *testing.T) {
	for _, backend := range []string{
		"https://opencode.ai/zen/go/v1",
		"http://203.0.113.10:18788",
		"http://example.com/",
		"not-a-url://",
		"",
	} {
		if _, err := fronthost.New(fronthost.Config{BackendURL: backend}); err == nil {
			t.Fatalf("backend %q accepted, want rejection", backend)
		}
	}
}
