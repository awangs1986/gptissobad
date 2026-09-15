package gateway_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/awangs1986/gptissobad/internal/gateway"
)

const replyInstruction = "Please reply in Chinese."

type recorded struct {
	mu     sync.Mutex
	bodies [][]byte
	auths  []string
	paths  []string
}

func (r *recorded) add(req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(body))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, append([]byte(nil), body...))
	r.auths = append(r.auths, req.Header.Get("Authorization"))
	r.paths = append(r.paths, req.URL.RequestURI())
}

func (r *recorded) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func (r *recorded) lastBody() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) == 0 {
		return nil
	}
	return r.bodies[len(r.bodies)-1]
}

func (r *recorded) allBodies() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]byte, len(r.bodies))
	copy(out, r.bodies)
	return out
}

type fixture struct {
	gateway    *httptest.Server
	upstream   *recorded
	translator *recorded
	impl       *gateway.Gateway
}

func startFixture(t *testing.T, cfg gateway.Config, up http.HandlerFunc, tr http.HandlerFunc) *fixture {
	t.Helper()
	upRec := &recorded{}
	trRec := &recorded{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upRec.add(r)
		if up != nil {
			up(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"up"}`))
	}))
	t.Cleanup(upstream.Close)
	translator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trRec.add(r)
		if tr != nil {
			tr(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix the login bug."}}]}`))
	}))
	t.Cleanup(translator.Close)
	cfg.Upstream = upstream.URL
	cfg.TranslatorURL = translator.URL + "/v1/chat/completions"
	if cfg.Model == "" {
		cfg.Model = "mimo-v2.5"
	}
	if cfg.TranslateTimeout == 0 {
		cfg.TranslateTimeout = 2 * time.Second
	}
	impl := gateway.New(cfg)
	srv := httptest.NewServer(impl)
	t.Cleanup(srv.Close)
	return &fixture{gateway: srv, upstream: upRec, translator: trRec, impl: impl}
}

func postJSON(t *testing.T, url, auth string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func TestEnglishUserPromptReachesUpstreamUnchanged(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	body := []byte(`{"model":"gpt-5.3-codex","instructions":"You are Codex.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files in src."}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if fx.translator.count() != 0 {
		t.Fatalf("translator called %d times, want 0", fx.translator.count())
	}
	if fx.upstream.count() != 1 {
		t.Fatalf("upstream called %d times, want 1", fx.upstream.count())
	}
	got := fx.upstream.lastBody()
	if !bytes.Contains(got, []byte("List the files in src.")) {
		t.Fatalf("english prompt mangled: %s", got)
	}
	if !bytes.Contains(got, []byte(replyInstruction)) {
		t.Fatalf("Reply Instruction missing on English turn: %s", got)
	}
	m := fx.impl.Metrics()
	if m.TranslationRequests != 0 || m.TotalTokens != 0 || m.TranslatedChars != 0 {
		t.Fatalf("english turn billed: %+v", m)
	}
	if fx.upstream.auths[0] != "Bearer client-key" {
		t.Fatalf("Authorization = %q", fx.upstream.auths[0])
	}
}

func TestEnglishUserPromptWithHanCwdIsNotFailClosed(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Create a file named probe-en.txt with a single line: english passthrough ok.\nWorking directory: C:\\Users\\example-user\\Desktop\\中文项目\\codex-lang"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if fx.translator.count() != 0 {
		t.Fatalf("translator called %d times, want 0", fx.translator.count())
	}
	got := fx.upstream.lastBody()
	if !bytes.Contains(got, []byte("english passthrough ok")) {
		t.Fatalf("english prompt missing: %s", got)
	}
	if !bytes.Contains(got, []byte("中文项目")) {
		t.Fatalf("cwd path missing: %s", got)
	}
}

func TestEnglishUserPromptWithHanCwdXmlIsNotFailClosed(t *testing.T) {
	// Real Codex sends <environment_context><cwd>...</cwd>, where the Han path
	// follows `>` rather than a space. The Nix path hold-out must recognise
	// that prefix, otherwise an English turn in a Han cwd fail-closes.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"pwd\n<environment_context>\n  <cwd>/home/user/测试工作区/codex-lang</cwd>\n</environment_context>"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if fx.translator.count() != 0 {
		t.Fatalf("translator called %d times, want 0", fx.translator.count())
	}
	got := fx.upstream.lastBody()
	if !bytes.Contains(got, []byte("pwd")) {
		t.Fatalf("english prompt missing: %s", got)
	}
	if !bytes.Contains(got, []byte("/home/user/测试工作区/codex-lang")) {
		t.Fatalf("cwd path missing: %s", got)
	}
}

func TestChineseUserPromptKeepsHanCwdPath(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if bytes.Contains(raw, []byte("中文项目")) {
			http.Error(w, "path leaked to translator", http.StatusInternalServerError)
			return
		}
		content := "Fix the login bug."
		if bytes.Contains(raw, []byte("__CLP_0__")) {
			content = "Fix the login bug. Working directory: __CLP_0__"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + content + `"}}]}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug。\nWorking directory: C:\\Users\\example-user\\Desktop\\中文项目\\codex-lang"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	up := string(fx.upstream.lastBody())
	if hasHan(strings.ReplaceAll(up, "中文项目", "")) {
		t.Fatalf("prose Han reached upstream: %s", up)
	}
	if !bytes.Contains(fx.upstream.lastBody(), []byte("中文项目")) {
		t.Fatalf("cwd path missing: %s", up)
	}
	if !bytes.Contains(fx.upstream.lastBody(), []byte("Fix the login bug.")) {
		t.Fatalf("Translated Prompt missing: %s", up)
	}
}

func TestChineseUserPromptIsReplacedBeforeUpstream(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix the login bug."}}]}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug。"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if fx.translator.count() != 1 {
		t.Fatalf("translator called %d times, want 1", fx.translator.count())
	}
	up := string(fx.upstream.lastBody())
	if hasHan(up) {
		t.Fatalf("Han reached upstream: %s", up)
	}
	if !bytes.Contains(fx.upstream.lastBody(), []byte("Fix the login bug.")) {
		t.Fatalf("Translated Prompt missing: %s", up)
	}
	if !bytes.Contains(fx.upstream.lastBody(), []byte(replyInstruction)) {
		t.Fatalf("Reply Instruction missing: %s", up)
	}
	if !json.Valid(fx.upstream.lastBody()) {
		t.Fatalf("upstream body truncated or not JSON: %s", up)
	}
	if int64(len(fx.upstream.lastBody())) <= int64(len(body)) {
		t.Fatalf("rewritten body should be longer than the Chinese original")
	}
}

func TestTranslationTokenMetricsCountsUsage(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix the login bug."}}],"usage":{"prompt_tokens":27,"completion_tokens":84,"total_tokens":111}}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug。"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	m := fx.impl.Metrics()
	if m.TranslationRequests != 1 {
		t.Fatalf("translationRequests = %d, want 1", m.TranslationRequests)
	}
	if m.PromptTokens != 27 || m.CompletionTokens != 84 || m.TotalTokens != 111 {
		t.Fatalf("tokens = %d/%d/%d, want 27/84/111", m.PromptTokens, m.CompletionTokens, m.TotalTokens)
	}
	if m.TranslatedChars == 0 {
		t.Fatalf("translatedChars = 0, want > 0")
	}
	// Same-session replay must not double-count tokens (session hit, no new call).
	sessBody := []byte(`{"model":"gpt-5.3-codex","conversation_id":"tok-metric-1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug。"}]}]}`)
	resp = postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", sessBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sess first status %d", resp.StatusCode)
	}
	mSess := fx.impl.Metrics()
	resp = postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", sessBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay status %d", resp.StatusCode)
	}
	m2 := fx.impl.Metrics()
	if m2.TotalTokens != mSess.TotalTokens {
		t.Fatalf("replay changed totalTokens %d -> %d", mSess.TotalTokens, m2.TotalTokens)
	}
	if m2.PromptTokens != mSess.PromptTokens || m2.CompletionTokens != mSess.CompletionTokens {
		t.Fatalf("replay changed token split")
	}
}

func TestTranslatorRequestIsDeterministicAndNeverAnswer(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix the login bug."}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug。"}]}]}`)
	if resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if fx.translator.count() != 1 {
		t.Fatalf("translator called %d times, want 1", fx.translator.count())
	}
	var req struct {
		Temperature *float64 `json:"temperature"`
		Messages    []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(fx.translator.lastBody(), &req); err != nil {
		t.Fatal(err)
	}
	if req.Temperature == nil || *req.Temperature != 0 {
		t.Fatalf("temperature = %v, want 0", req.Temperature)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" ||
		!strings.Contains(req.Messages[0].Content, "never an answer") {
		t.Fatalf("system prompt does not forbid answering: %+v", req.Messages)
	}
}

func TestFallbackTranslatorRescuesHanPrimary(t *testing.T) {
	var primaryCalls, fallbackCalls int
	var fallbackModel string
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"中文答复"}}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`))
	}))
	t.Cleanup(primary.Close)
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls++
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &req)
		fallbackModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"Explain this."}]}],"usage":{"input_tokens":5,"output_tokens":7,"total_tokens":12}}`))
	}))
	t.Cleanup(fallback.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"up"}`))
	}))
	t.Cleanup(upstream.Close)
	impl := gateway.New(gateway.Config{
		Upstream:      upstream.URL,
		TranslatorURL: primary.URL,
		APIKey:        "go-key",
		Model:         "mimo-test",
		FallbackModel: "spark-test",
		FallbackURL:   fallback.URL,
	})
	srv := httptest.NewServer(impl)
	t.Cleanup(srv.Close)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"解释一下。"}]}]}`)
	resp := postJSON(t, srv.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s, want rescue", resp.StatusCode, got)
	}
	if primaryCalls != 1 || fallbackCalls != 1 {
		t.Fatalf("primary=%d fallback=%d, want 1/1", primaryCalls, fallbackCalls)
	}
	if fallbackModel != "spark-test" {
		t.Fatalf("fallback model = %q", fallbackModel)
	}
	m := impl.Metrics()
	if m.FallbackRequests != 1 {
		t.Fatalf("fallbackRequests = %d, want 1", m.FallbackRequests)
	}
	if m.PromptTokens != 15 || m.CompletionTokens != 27 || m.TotalTokens != 42 {
		t.Fatalf("tokens = %d/%d/%d, want 15/27/42 (both calls billed)", m.PromptTokens, m.CompletionTokens, m.TotalTokens)
	}
	if m.TranslatedChars != 5 {
		t.Fatalf("translatedChars = %d, want 5", m.TranslatedChars)
	}
}

func TestFallbackRescuesInfraFailure(t *testing.T) {
	// The fallback covers every primary failure mode, not just Han output:
	// a 500ing primary still ends in a forwarded turn.
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(primary.Close)
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"Fix it."}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	t.Cleanup(fallback.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"up"}`))
	}))
	t.Cleanup(upstream.Close)
	impl := gateway.New(gateway.Config{
		Upstream:      upstream.URL,
		TranslatorURL: primary.URL,
		APIKey:        "go-key",
		FallbackModel: "spark-test",
		FallbackURL:   fallback.URL,
	})
	srv := httptest.NewServer(impl)
	t.Cleanup(srv.Close)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修一下。"}]}]}`)
	resp := postJSON(t, srv.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s, want rescue", resp.StatusCode, got)
	}
	if got := impl.Metrics().FallbackRequests; got != 1 {
		t.Fatalf("fallbackRequests = %d, want 1", got)
	}
}

func TestQuotaFromEitherSideNamesQuota(t *testing.T) {
	// Primary 500s while the fallback reports quota exhaustion: the
	// actionable signal is quota, not the transport failure.
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(primary.Close)
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Policy §5: quota is named by the body, not by the status alone.
		http.Error(w, "额度已用尽，请充值", http.StatusTooManyRequests)
	}))
	t.Cleanup(fallback.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be reached")
	}))
	t.Cleanup(upstream.Close)
	impl := gateway.New(gateway.Config{
		Upstream:      upstream.URL,
		TranslatorURL: primary.URL,
		APIKey:        "go-key",
		FallbackModel: "spark-test",
		FallbackURL:   fallback.URL,
	})
	srv := httptest.NewServer(impl)
	t.Cleanup(srv.Close)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修一下。"}]}]}`)
	resp := postJSON(t, srv.URL+"/v1/responses", "Bearer client-key", body)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if !bytes.Contains(raw, []byte("额度")) {
		t.Fatalf("body %q must name quota", raw)
	}
}

func TestFallbackHanStillFailsClosed(t *testing.T) {
	han := `{"choices":[{"message":{"content":"中文答复"}}]}`
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(han))
	}))
	t.Cleanup(primary.Close)
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"还是中文"}]}]}`))
	}))
	t.Cleanup(fallback.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be reached")
	}))
	t.Cleanup(upstream.Close)
	impl := gateway.New(gateway.Config{
		Upstream:      upstream.URL,
		TranslatorURL: primary.URL,
		APIKey:        "go-key",
		FallbackModel: "spark-test",
		FallbackURL:   fallback.URL,
	})
	srv := httptest.NewServer(impl)
	t.Cleanup(srv.Close)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"解释一下。"}]}]}`)
	resp := postJSON(t, srv.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
}

func TestTranslatingFlagDuringFlight(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix it."}}]}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修一下。"}]}]}`)
	done := make(chan *http.Response, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, fx.gateway.URL+"/v1/responses", bytes.NewReader(body))
		if err != nil {
			done <- nil
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer client-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- nil
			return
		}
		done <- resp
	}()
	<-started
	deadline := time.Now().Add(5 * time.Second)
	for {
		if fx.impl.Metrics().Translating {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("translating never true mid-flight")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	resp := <-done
	if resp == nil {
		t.Fatal("request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if fx.impl.Metrics().Translating {
		t.Fatal("translating still true after completion")
	}
}

func TestTranslationHanFailureStillCountsTokens(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"\u4e2d\u6587\u7ffb\u8bd1\u5931\u8d25"}}],"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"\u4fee\u590d\u767b\u5f55\u7684 bug\u3002"}]}]}`)
	// The raw JSON uses \u escapes so this source stays ASCII-only; the
	// gateway decodes them to Han before translating, and the stub translator
	// answers with Han, which must fail closed as 403 while still counting
	// the billed tokens.
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != 403 {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	m := fx.impl.Metrics()
	if m.Rejected != 1 {
		t.Fatalf("rejected = %d, want 1", m.Rejected)
	}
	if m.TotalTokens != 12 || m.PromptTokens != 5 || m.CompletionTokens != 7 {
		t.Fatalf("tokens = %d/%d/%d, want 5/7/12", m.PromptTokens, m.CompletionTokens, m.TotalTokens)
	}
	if m.TranslatedChars != 0 {
		t.Fatalf("translatedChars = %d, want 0 for rejected turn", m.TranslatedChars)
	}
}

func TestNonUserHanIsForwardedUntranslated(t *testing.T) {
	// Fail-open policy: instructions, developer messages and tool outputs
	// are never translated and never block. Only user/assistant prose goes
	// through the Translator Backend; everything else rides along as-is.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix the login bug."}}]}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","instructions":"用中文回答。","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"系统提示中文"}]},{"type":"function_call_output","output":"文件内容中文"},{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,xx"},{"type":"input_text","text":"修复登录的 bug。"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s, want forward", resp.StatusCode, raw)
	}
	up := fx.upstream.lastBody()
	for _, want := range []string{"用中文回答。", "系统提示中文", "文件内容中文", "Fix the login bug."} {
		if !bytes.Contains(up, []byte(want)) {
			t.Fatalf("missing %q in %s", want, up)
		}
	}
	if fx.translator.count() != 1 {
		t.Fatalf("translator called %d times, want exactly 1 (user text only)", fx.translator.count())
	}
}

func TestNonUserHanPathIsProxied(t *testing.T) {
	// The only exemption: Han that lives inside a file path. Tool output
	// and instructions carrying a Han working directory must not 403.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	body := []byte(`{"model":"gpt-5.3-codex","instructions":"Work in /home/user/\u6d4b\u8bd5\u5de5\u4f5c\u533a/codex-lang.","input":[{"type":"function_call_output","output":"/home/user/\u6d4b\u8bd5\u5de5\u4f5c\u533a/codex-lang\nok"},{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files."}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s", resp.StatusCode, raw)
	}
	if fx.translator.count() != 0 {
		t.Fatalf("translator called %d times, want 0", fx.translator.count())
	}
	if fx.upstream.count() != 1 {
		t.Fatalf("upstream called %d times, want 1", fx.upstream.count())
	}
}

func TestReplayedHistoryTranslatesOnce(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix the login bug."}}]}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug。"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug。"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if fx.translator.count() != 1 {
		t.Fatalf("translator called %d times, want 1", fx.translator.count())
	}
	up := string(fx.upstream.lastBody())
	if strings.Count(up, "Fix the login bug.") != 2 {
		t.Fatalf("want both items translated the same way: %s", up)
	}
	if strings.Count(up, replyInstruction) != 1 {
		t.Fatalf("Reply Instruction not only on last user item: %s", up)
	}
}

func TestIncrementalSessionTranslationAndFullRebuild(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	first := []byte(`{"model":"gpt-5.3-codex","conversation_id":"c1","messages":[{"role":"user","content":"修复登录。"}]}`)
	if resp := postJSON(t, fx.gateway.URL+"/v1/chat/completions", "Bearer client-key", first); resp.StatusCode != http.StatusOK {
		t.Fatalf("first status %d", resp.StatusCode)
	}
	second := []byte(`{"model":"gpt-5.3-codex","conversation_id":"c1","messages":[{"role":"user","content":"修复登录。"},{"role":"user","content":"增加测试。"}]}`)
	if resp := postJSON(t, fx.gateway.URL+"/v1/chat/completions", "Bearer client-key", second); resp.StatusCode != http.StatusOK {
		t.Fatalf("second status %d", resp.StatusCode)
	}
	if got := fx.translator.count(); got != 2 {
		t.Fatalf("incremental translator calls = %d, want 2", got)
	}
	// Replacing the first historical item breaks the prefix (full rebuild),
	// but the unchanged later chunk still hits the Translate Cache.
	third := []byte(`{"model":"gpt-5.3-codex","conversation_id":"c1","messages":[{"role":"user","content":"重写登录。"},{"role":"user","content":"增加测试。"}]}`)
	if resp := postJSON(t, fx.gateway.URL+"/v1/chat/completions", "Bearer client-key", third); resp.StatusCode != http.StatusOK {
		t.Fatalf("third status %d", resp.StatusCode)
	}
	if got := fx.translator.count(); got != 3 {
		t.Fatalf("full rebuild translator calls = %d, want 3", got)
	}
	if fx.impl.Metrics().ActiveSessions != 1 {
		t.Fatalf("identified session count = %d, want 1", fx.impl.Metrics().ActiveSessions)
	}
}

func TestUnidentifiedRequestsAreNotPrefixCached(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	first := []byte(`{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"修复登录。"}]}`)
	if resp := postJSON(t, fx.gateway.URL+"/v1/chat/completions", "Bearer client-key", first); resp.StatusCode != http.StatusOK {
		t.Fatalf("first status %d", resp.StatusCode)
	}
	second := []byte(`{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"修复登录。"},{"role":"user","content":"增加测试。"}]}`)
	if resp := postJSON(t, fx.gateway.URL+"/v1/chat/completions", "Bearer client-key", second); resp.StatusCode != http.StatusOK {
		t.Fatalf("second status %d", resp.StatusCode)
	}
	if got := fx.translator.count(); got != 2 {
		t.Fatalf("unidentified translator calls = %d, want 2 (no session, Translate Cache still hits)", got)
	}
	if fx.impl.Metrics().ActiveSessions != 0 {
		t.Fatalf("unidentified requests should not leave a session, got %d", fx.impl.Metrics().ActiveSessions)
	}
}

func TestTranslateCacheReloadsFromDiskWithoutUserPrompt(t *testing.T) {
	cacheFile := filepath.Join(t.TempDir(), "translate-cache.json")
	cfg := gateway.Config{APIKey: "go-key", CacheFile: cacheFile}
	fx1 := startFixture(t, cfg, nil, nil)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录。"}]}]}`)
	if resp := postJSON(t, fx1.gateway.URL+"/v1/responses", "Bearer client-key", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("first status %d", resp.StatusCode)
	}
	if fx1.translator.count() != 1 {
		t.Fatalf("first translator calls = %d, want 1", fx1.translator.count())
	}
	// Disk writes are debounced; flush like Runtime.Stop does on shutdown.
	fx1.impl.FlushCache()
	raw, err := os.ReadFile(cacheFile)
	if err != nil {
		t.Fatal(err)
	}
	if hasHan(string(raw)) {
		t.Fatalf("cache file stored Han (User Prompt leaked): %s", raw)
	}
	if !bytes.Contains(raw, []byte("Fix the login bug.")) {
		t.Fatalf("cache file missing Translated Prompt: %s", raw)
	}

	fx2 := startFixture(t, cfg, nil, nil)
	if resp := postJSON(t, fx2.gateway.URL+"/v1/responses", "Bearer client-key", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("reload status %d", resp.StatusCode)
	}
	if fx2.translator.count() != 0 {
		t.Fatalf("reloaded gateway translator calls = %d, want 0", fx2.translator.count())
	}
	if !bytes.Contains(fx2.upstream.lastBody(), []byte("Fix the login bug.")) {
		t.Fatalf("reloaded turn not translated from cache: %s", fx2.upstream.lastBody())
	}
}

func TestResolveMimoKeyPrefersFileThenEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("MIMO_API_KEY", "")
	if got := gateway.ResolveMimoKey(); got != "" {
		t.Fatalf("empty setup = %q, want empty", got)
	}
	t.Setenv("MIMO_API_KEY", "env-key")
	if got := gateway.ResolveMimoKey(); got != "env-key" {
		t.Fatalf("env = %q", got)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex", "mimo.key"), []byte("file-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := gateway.ResolveMimoKey(); got != "file-key" {
		t.Fatalf("file = %q, want file-key", got)
	}
}

func TestMimoModelRoutesToMimoEndpoint(t *testing.T) {
	var gotAuth, gotModel string
	var xiaomiCalls int
	xiaomi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xiaomiCalls++
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &req)
		gotModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix it."}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(xiaomi.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"up"}`))
	}))
	t.Cleanup(upstream.Close)
	impl := gateway.New(gateway.Config{
		Upstream:    upstream.URL,
		APIKey:      "opencode-key-unused",
		Model:       "mimo-v2.5",
		MimoAPIKey:  "mimo-test-key",
		MimoBaseURL: xiaomi.URL,
	})
	srv := httptest.NewServer(impl)
	t.Cleanup(srv.Close)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复一下。"}]}]}`)
	resp := postJSON(t, srv.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s", resp.StatusCode, got)
	}
	if xiaomiCalls != 1 {
		t.Fatalf("xiaomi calls = %d, want 1", xiaomiCalls)
	}
	if gotAuth != "Bearer mimo-test-key" {
		t.Fatalf("xiaomi auth = %q", gotAuth)
	}
	if gotModel != "mimo-v2.5" {
		t.Fatalf("xiaomi model = %q", gotModel)
	}
}

func TestExplicitTranslatorURLWinsOverMimoRoute(t *testing.T) {
	var xiaomiCalls, opencodeCalls int
	xiaomi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xiaomiCalls++
	}))
	t.Cleanup(xiaomi.Close)
	opencode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opencodeCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix it."}}]}`))
	}))
	t.Cleanup(opencode.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"up"}`))
	}))
	t.Cleanup(upstream.Close)
	impl := gateway.New(gateway.Config{
		Upstream:      upstream.URL,
		TranslatorURL: opencode.URL,
		APIKey:        "opencode-key",
		Model:         "mimo-v2.5",
		MimoAPIKey:    "mimo-test-key",
		MimoBaseURL:   xiaomi.URL,
	})
	srv := httptest.NewServer(impl)
	t.Cleanup(srv.Close)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复一下。"}]}]}`)
	if resp := postJSON(t, srv.URL+"/v1/responses", "Bearer client-key", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if opencodeCalls != 1 || xiaomiCalls != 0 {
		t.Fatalf("opencode=%d xiaomi=%d, want 1/0", opencodeCalls, xiaomiCalls)
	}
}

func TestTranslatorQuotaNamesQuota(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "额度用尽：本月额度已用完", http.StatusTooManyRequests)
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug。"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if !bytes.Contains(raw, []byte("额度")) {
		t.Fatalf("body %q must name quota", raw)
	}
	if fx.upstream.count() != 0 {
		t.Fatalf("upstream received %d requests, want 0", fx.upstream.count())
	}
}

func TestReplyInstructionIsPrepended(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files."}]}]}`)
	if resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got := fx.upstream.lastBody()
	if !bytes.Contains(got, []byte(`"text":"Please reply in Chinese.\nList the files."`)) {
		t.Fatalf("instruction not at front: %s", got)
	}
}

func TestTranslatorServerErrorFailsClosed(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug。"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if fx.upstream.count() != 0 {
		t.Fatalf("upstream received %d requests, want 0", fx.upstream.count())
	}
	if fx.translator.count() != 2 {
		t.Fatalf("translator called %d times, want 2", fx.translator.count())
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(got, []byte("翻译失败，这一轮未发送")) {
		t.Fatalf("reason = %s", got)
	}
}

func TestTranslatorTimeoutFailsClosed(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key", TranslateTimeout: 80 * time.Millisecond}, nil, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix the login bug."}}]}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug。"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if fx.upstream.count() != 0 {
		t.Fatalf("upstream received %d requests, want 0", fx.upstream.count())
	}
}

func TestEnglishUserPromptWorksWithoutTranslatorKey(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: ""}, nil, nil)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files in src."}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if fx.translator.count() != 0 {
		t.Fatalf("translator called")
	}
	if !bytes.Contains(fx.upstream.lastBody(), []byte(replyInstruction)) {
		t.Fatalf("Reply Instruction missing without translator key: %s", fx.upstream.lastBody())
	}
}

func TestCheckTranslatorSendsOpenCodeGoProbe(t *testing.T) {
	var (
		gotPath    string
		gotAuth    string
		gotAgent   string
		gotSession string
		gotBody    struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
	)
	translator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAgent = r.Header.Get("User-Agent")
		gotSession = r.Header.Get("x-opencode-session")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode probe body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	t.Cleanup(translator.Close)

	g := gateway.New(gateway.Config{
		TranslatorURL:    translator.URL + "/zen/go/v1/chat/completions",
		APIKey:           "go-key",
		Model:            "mimo-v2.5",
		TranslateTimeout: time.Second,
	})
	if err := g.CheckTranslator(context.Background()); err != nil {
		t.Fatalf("CheckTranslator() error = %v", err)
	}
	if gotPath != "/zen/go/v1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer go-key" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotAgent != "codex-lang/1.0" {
		t.Fatalf("User-Agent = %q", gotAgent)
	}
	if gotSession != "codex-lang" {
		t.Fatalf("x-opencode-session = %q", gotSession)
	}
	if gotBody.Model != "mimo-v2.5" {
		t.Fatalf("model = %q", gotBody.Model)
	}
	if len(gotBody.Messages) != 2 ||
		gotBody.Messages[0].Role != "system" ||
		gotBody.Messages[1].Role != "user" ||
		gotBody.Messages[1].Content != "Reply with OK." {
		t.Fatalf("probe messages = %+v", gotBody.Messages)
	}
}

func TestCheckTranslatorReportsInvalidCredential(t *testing.T) {
	translator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid", http.StatusUnauthorized)
	}))
	t.Cleanup(translator.Close)

	g := gateway.New(gateway.Config{
		TranslatorURL:    translator.URL,
		APIKey:           "expired-key",
		TranslateTimeout: time.Second,
	})
	err := g.CheckTranslator(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "无效") {
		t.Fatalf("CheckTranslator() error = %v", err)
	}
}

func TestSecretHanIsForwardedWithoutTranslator(t *testing.T) {
	// Credentials never enter the Translator Backend either way, and
	// policy forwards what cannot be translated: exactly as received.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"sk-中文密钥"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s, want forward", resp.StatusCode, got)
	}
	if fx.translator.count() != 0 {
		t.Fatalf("translator called %d times, want 0 (secrets never sent)", fx.translator.count())
	}
	if !bytes.Contains(fx.upstream.lastBody(), body) {
		t.Fatalf("upstream body rewritten: %s", fx.upstream.lastBody())
	}
}

func TestMissingKeyForwardsUntranslated(t *testing.T) {
	// Without credentials nothing can be translated; policy forwards the
	// turn exactly as received instead of bricking Codex. Control Page key
	// hints still tell the operator to fix the key.
	fx := startFixture(t, gateway.Config{APIKey: ""}, nil, nil)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug。"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s, want forward", resp.StatusCode, got)
	}
	if !bytes.Contains(fx.upstream.lastBody(), body) {
		t.Fatalf("upstream body rewritten: %s", fx.upstream.lastBody())
	}
	if fx.translator.count() != 0 {
		t.Fatalf("translator called %d times, want 0", fx.translator.count())
	}
}

func TestSecretLineIsHeldOutOfTranslatorAndKeptUpstream(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix the login bug."}}]}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录。\nsk-abc123secret\n再试一次。"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, raw := range fx.translator.allBodies() {
		if bytes.Contains(raw, []byte("sk-abc123secret")) {
			t.Fatalf("secret reached translator: %s", raw)
		}
	}
	up := fx.upstream.lastBody()
	if !bytes.Contains(up, []byte("sk-abc123secret")) {
		t.Fatalf("secret missing upstream: %s", up)
	}
	if hasHan(string(up)) {
		t.Fatalf("Han reached upstream: %s", up)
	}
}

func TestWebSocketUpgradeIsRejected(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	req, err := http.NewRequest(http.MethodGet, fx.gateway.URL+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if fx.upstream.count() != 0 {
		t.Fatalf("upstream received %d requests, want 0", fx.upstream.count())
	}
}

func TestModelsIsProxiedUnchanged(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.3-codex"}]}`))
	}, nil)
	resp, err := http.Get(fx.gateway.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(got, []byte("gpt-5.3-codex")) {
		t.Fatalf("body = %s", got)
	}
	if fx.upstream.count() != 1 {
		t.Fatalf("upstream called %d times, want 1", fx.upstream.count())
	}
	if fx.upstream.paths[0] != "/v1/models" {
		t.Fatalf("path = %s", fx.upstream.paths[0])
	}
	if fx.translator.count() != 0 {
		t.Fatalf("translator called")
	}
}

func TestAssistantHistoryIsTranslated(t *testing.T) {
	// The Reply Instruction makes every assistant reply Chinese, and Codex
	// replays it as history on the next turn. That history must enter the
	// same translation path as user text, or every multi-turn session dies
	// at turn two. Cost is bounded: replaying the same history again must
	// not call the Translator Backend a second time.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fixed the login bug."}}]}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"好的，已经修复了登录 bug。"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"再检查一遍。"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	if fx.translator.count() != 2 {
		t.Fatalf("translator called %d times, want 2 (assistant reply + user turn)", fx.translator.count())
	}
	up := fx.upstream.lastBody()
	if bytes.Contains(up, []byte("好的")) || bytes.Contains(up, []byte("再检查")) {
		t.Fatalf("Han reached upstream: %s", up)
	}
	if !bytes.Contains(up, []byte(`"role":"assistant"`)) || !bytes.Contains(up, []byte(`"type":"output_text"`)) {
		t.Fatalf("assistant history item lost its shape: %s", up)
	}
	for _, r := range string(up) {
		if unicode.Is(unicode.Han, r) {
			t.Fatalf("upstream body contains Han: %s", up)
		}
	}

	// Same history replayed: both chunks are cache hits, nothing is billed.
	resp = postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay status %d, want 200", resp.StatusCode)
	}
	if fx.translator.count() != 2 {
		t.Fatalf("replay called translator again: %d total, want 2", fx.translator.count())
	}
}

func TestSystemAndToolHistoryAreForwarded(t *testing.T) {
	// Only user and assistant enter translation. A developer message or a
	// tool result with Han rides along untouched: per translation-policy
	// §1/§3, docs are never translated and content never blocks.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	for _, body := range []string{
		`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"用中文回答。"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
		`{"model":"gpt-5.3-codex","input":[{"type":"function_call_output","call_id":"c1","output":"文件内容"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
	} {
		resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", []byte(body))
		if resp.StatusCode != http.StatusOK {
			got, _ := io.ReadAll(resp.Body)
			t.Fatalf("status %d %s, want forward for %s", resp.StatusCode, got, body)
		}
	}
	if fx.translator.count() != 0 {
		t.Fatalf("translator called %d times, want 0", fx.translator.count())
	}
	if fx.upstream.count() != 2 {
		t.Fatalf("upstream received %d requests, want 2 forwarded", fx.upstream.count())
	}
}

func TestGzipBodyIsForwardedAsIs(t *testing.T) {
	// Fail-open policy: an undecodable body cannot be translated, so it
	// rides through byte-identical without a Translator Backend call.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug。"}]}]}`))
	_ = zw.Close()
	req, err := http.NewRequest(http.MethodPost, fx.gateway.URL+"/v1/responses", bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer client-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want forward", resp.StatusCode)
	}
	if fx.upstream.count() != 1 {
		t.Fatalf("upstream received %d requests, want 1 forwarded", fx.upstream.count())
	}
	if !bytes.Equal(fx.upstream.lastBody(), buf.Bytes()) {
		t.Fatalf("upstream body rewritten")
	}
	if fx.translator.count() != 0 {
		t.Fatalf("translator called %d times, want 0", fx.translator.count())
	}
}

func TestNonPostBodyHanIsForwarded(t *testing.T) {
	// Translation only runs on POST turns; other methods forward as-is.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	req, err := http.NewRequest(http.MethodPut, fx.gateway.URL+"/v1/responses", strings.NewReader(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug。"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer client-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want forward", resp.StatusCode)
	}
	if fx.translator.count() != 0 {
		t.Fatalf("translator called %d times, want 0 (no translation off POST)", fx.translator.count())
	}
	if fx.upstream.count() != 1 {
		t.Fatalf("upstream received %d requests, want 1 forwarded", fx.upstream.count())
	}
}

func TestQueryHanIsForwarded(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	english := `{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files."}]}]}`
	for name, target := range map[string]string{
		"raw":     fx.gateway.URL + "/v1/responses?note=中文",
		"encoded": fx.gateway.URL + "/v1/responses?note=%E4%B8%AD%E6%96%87",
	} {
		req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(english))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer client-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s query: status %d, want forward", name, resp.StatusCode)
		}
	}
	if fx.upstream.count() != 2 {
		t.Fatalf("upstream received %d requests, want 2 forwarded", fx.upstream.count())
	}
	if fx.translator.count() != 0 {
		t.Fatalf("translator called %d times, want 0", fx.translator.count())
	}
}

func TestHeaderHanIsForwarded(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	req, err := http.NewRequest(http.MethodPost, fx.gateway.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files."}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer client-key")
	req.Header.Set("X-Note", "中文标签")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want forward", resp.StatusCode)
	}
	if fx.upstream.count() != 1 {
		t.Fatalf("upstream received %d requests, want 1 forwarded", fx.upstream.count())
	}
	if fx.translator.count() != 0 {
		t.Fatalf("translator called %d times, want 0", fx.translator.count())
	}
}

func TestPreviousResponseIDDoesNotCreateSession(t *testing.T) {
	// previous_response_id names the previous response, so it changes every
	// turn. Keying sessions on it would pile one record per turn; such
	// requests must stay on the request-scoped path instead.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	for _, rid := range []string{"resp-aaa", "resp-bbb"} {
		body := []byte(`{"model":"gpt-5.3-codex","previous_response_id":"` + rid + `","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files."}]}]}`)
		resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
	}
	if got := fx.impl.Metrics().ActiveSessions; got != 0 {
		t.Fatalf("activeSessions = %d, want 0", got)
	}
}

func TestChatCompletionsSystemHanIsForwarded(t *testing.T) {
	// System messages are not translated and do not block: the user text
	// still translates, the system text rides along untouched.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix the login bug."}}]}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","messages":[{"role":"system","content":"用中文。"},{"role":"user","content":"修复登录的 bug。"}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/chat/completions", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s, want forward", resp.StatusCode, raw)
	}
	up := fx.upstream.lastBody()
	if !bytes.Contains(up, []byte("用中文。")) {
		t.Fatalf("system text dropped: %s", up)
	}
	if !bytes.Contains(up, []byte("Fix the login bug.")) {
		t.Fatalf("Translated Prompt missing: %s", up)
	}
}

func TestChatCompletionsUserMessageIsRewritten(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix the login bug."}}]}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","messages":[{"role":"system","content":"Answer briefly."},{"role":"user","content":"修复登录的 bug。"}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/chat/completions", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	up := string(fx.upstream.lastBody())
	if hasHan(up) {
		t.Fatalf("user Han reached upstream: %s", up)
	}
	if !bytes.Contains(fx.upstream.lastBody(), []byte("Fix the login bug.")) {
		t.Fatalf("Translated Prompt missing: %s", up)
	}
}

func TestStreamingResponseFlushesBeforeUpstreamFinishes(t *testing.T) {
	release := make(chan struct{})
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("upstream not flushable")
		}
		_, _ = w.Write([]byte("data: first\n\n"))
		flusher.Flush()
		<-release
		_, _ = w.Write([]byte("data: second\n\n"))
		flusher.Flush()
	}, nil)
	req, err := http.NewRequest(http.MethodPost, fx.gateway.URL+"/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files."}]}]}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if !bytes.Contains(buf[:n], []byte("data: first")) {
		t.Fatalf("first chunk = %q", buf[:n])
	}
	close(release)
	rest, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(append(buf[:n], rest...), []byte("data: second")) {
		t.Fatalf("missing second chunk: %s", rest)
	}
}

func TestStringInputIsTranslatedBeforeUpstream(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	body := []byte(`{"model":"gpt-5.3-codex","input":"修复登录的 bug。"}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	if fx.translator.count() != 1 {
		t.Fatalf("translator called %d times, want 1", fx.translator.count())
	}
	up := string(fx.upstream.lastBody())
	if hasHan(up) {
		t.Fatalf("Han reached upstream: %s", up)
	}
	if !strings.Contains(up, "Fix the login bug.") {
		t.Fatalf("Translated Prompt missing: %s", up)
	}
	if !strings.Contains(up, replyInstruction) {
		t.Fatalf("Reply Instruction missing on string input: %s", up)
	}
}

func TestStringItemsInInputAreTranslated(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	body := []byte(`{"model":"gpt-5.3-codex","input":["你好","List the files."]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	up := string(fx.upstream.lastBody())
	if hasHan(up) {
		t.Fatalf("Han reached upstream: %s", up)
	}
	if !strings.Contains(up, "List the files.") {
		t.Fatalf("English item was rewritten: %s", up)
	}
	if fx.translator.count() != 1 {
		t.Fatalf("translator called %d times, want 1", fx.translator.count())
	}
}

func TestMalformedJSONIsForwarded(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	body := []byte(`{"input": "中文`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s, want forward", resp.StatusCode, got)
	}
	if fx.upstream.count() != 1 {
		t.Fatalf("upstream received %d requests, want 1 forwarded", fx.upstream.count())
	}
	if !bytes.Equal(fx.upstream.lastBody(), body) {
		t.Fatalf("upstream body rewritten: %s", fx.upstream.lastBody())
	}
	if fx.translator.count() != 0 {
		t.Fatalf("translator called %d times, want 0", fx.translator.count())
	}
}

func TestTranslatorReturningHanFailsClosed(t *testing.T) {
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"你好世界"}}]}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug。"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if fx.upstream.count() != 0 {
		t.Fatalf("upstream received %d requests, want 0", fx.upstream.count())
	}
}

func TestSecretLineWithHanIsForwarded(t *testing.T) {
	// The Bearer line never enters the Translator Backend, and policy
	// forwards what cannot be translated: exactly as received.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"什么是 Bearer token？"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s, want forward", resp.StatusCode, got)
	}
	if fx.upstream.count() != 1 {
		t.Fatalf("upstream received %d requests, want 1 forwarded", fx.upstream.count())
	}
	if fx.translator.count() != 0 {
		t.Fatalf("translator called %d times, want 0 (secrets never sent)", fx.translator.count())
	}
}

func TestSecretHanHoldsOutRestTranslates(t *testing.T) {
	// A secret line with Han no longer aborts the turn: it is held out of
	// the Translator Backend verbatim while the surrounding prose still
	// translates, and the Reply Instruction still attaches.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Check again."}}]}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录。\nsk-中文密钥\n再检查。"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s, want forward", resp.StatusCode, got)
	}
	for _, raw := range fx.translator.allBodies() {
		if bytes.Contains(raw, []byte("sk-")) {
			t.Fatalf("secret reached translator: %s", raw)
		}
	}
	up := fx.upstream.lastBody()
	if !bytes.Contains(up, []byte("sk-中文密钥")) {
		t.Fatalf("secret line dropped: %s", up)
	}
	if !bytes.Contains(up, []byte("Check again.")) {
		t.Fatalf("surrounding prose not translated: %s", up)
	}
	if !bytes.Contains(up, []byte(replyInstruction)) {
		t.Fatalf("Reply Instruction missing: %s", up)
	}
}

func TestUnknownShapePartialTranslatesRest(t *testing.T) {
	// An unknown part shape is held out in place while known siblings
	// still translate, instead of cancelling the whole turn.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix the login bug."}}]}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录。"},{"type":"custom","text":"备注中文"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s, want forward", resp.StatusCode, got)
	}
	up := fx.upstream.lastBody()
	if !bytes.Contains(up, []byte("Fix the login bug.")) {
		t.Fatalf("known part not translated: %s", up)
	}
	if !bytes.Contains(up, []byte("备注中文")) {
		t.Fatalf("unknown part dropped: %s", up)
	}
}

func TestUnknownUserPartWithHanIsForwarded(t *testing.T) {
	// Shapes the translator cannot address ride along untouched instead of
	// failing: an untranslatable turn is still a forwardable turn.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, nil)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"custom","text":"中文"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s, want forward", resp.StatusCode, got)
	}
	if fx.upstream.count() != 1 {
		t.Fatalf("upstream received %d requests, want 1 forwarded", fx.upstream.count())
	}
}

func TestBareRateLimitRetriesThenFailsClosed(t *testing.T) {
	// translation-policy.md §5: quota means 429/402 whose body names 额度.
	// A bare 429 is a busy backend: retry once, paced, then fail closed with
	// the generic message instead of telling the user the quota is gone.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug。"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if bytes.Contains(raw, []byte("额度")) {
		t.Fatalf("body %q must not claim quota for a bare 429", raw)
	}
	if fx.translator.count() != 2 {
		t.Fatalf("translator called %d times, want 2 (one paced retry)", fx.translator.count())
	}
	if fx.upstream.count() != 0 {
		t.Fatalf("upstream received %d requests, want 0", fx.upstream.count())
	}
}

func TestTruncatedTranslationFailsClosed(t *testing.T) {
	// finish_reason=length means the translator ran out of output budget.
	// The fragment used to pass the Han check, get cached and be forwarded
	// as the user's prompt; now it fails closed and says so.
	fx := startFixture(t, gateway.Config{APIKey: "go-key"}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix the login"},"finish_reason":"length"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`))
	})
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复登录的 bug：登录一直失败，需要看日志。修复登录的 bug：登录一直失败，需要看日志。"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if !bytes.Contains(raw, []byte("截断")) {
		t.Fatalf("body %q must name truncation", raw)
	}
	if fx.upstream.count() != 0 {
		t.Fatalf("upstream received %d requests, want 0", fx.upstream.count())
	}
}

func TestOversizedLineIsSplitWithoutInventingNewlines(t *testing.T) {
	// The chunk cap is the fix for "a long paste looks like the translator
	// is unreachable": one over-long line becomes several bounded requests,
	// and reassembly must not insert newlines that were never in the input.
	var mu sync.Mutex
	var sizes []int
	fx := startFixture(t, gateway.Config{APIKey: "go-key", MaxChunkRunes: 10}, nil, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Messages) != 2 {
			t.Errorf("messages = %+v", req.Messages)
		}
		piece := req.Messages[1].Content
		mu.Lock()
		sizes = append(sizes, len([]rune(piece)))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + strings.Repeat("b", len([]rune(piece))) + `"},"finish_reason":"stop"}]}`))
	})
	// Distinct content per piece: identical pieces would be served from
	// the hash cache and never reach the translator.
	line := "第一段修复登录。第二段修复支付。第三段修复搜索。第四段修复上传。"
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"` + line + `"}]}]}`)
	resp := postJSON(t, fx.gateway.URL+"/v1/responses", "Bearer client-key", body)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d %s", resp.StatusCode, raw)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sizes) != 4 {
		t.Fatalf("translator calls = %v, want 4 pieces of <=10 runes", sizes)
	}
	if sizes[3] != 2 {
		t.Fatalf("last piece = %d runes, want the 2-rune remainder", sizes[3])
	}
	for _, n := range sizes {
		if n > 10 {
			t.Fatalf("piece size = %d, want <= 10", n)
		}
	}
	joined := strings.Repeat("b", 32)
	if !bytes.Contains(fx.upstream.lastBody(), []byte("Please reply in Chinese.\\n"+joined)) {
		t.Fatalf("reassembled text missing or newline-split: %s", fx.upstream.lastBody())
	}
}

func TestMimoKeyAloneIsEnoughToTranslate(t *testing.T) {
	// The Xiaomi route carries its own credential: an empty OpenCode Go key
	// must not silently forward Chinese untranslated (that bug also killed
	// the panel blink, because no translator call ever started).
	var xiaomiCalls int
	xiaomi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xiaomiCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Fix it."},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(xiaomi.Close)
	upstream := &recorded{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.add(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"up"}`))
	}))
	t.Cleanup(up.Close)
	impl := gateway.New(gateway.Config{
		Upstream:    up.URL,
		APIKey:      "", // no OpenCode Go credential at all
		Model:       "mimo-v2.5",
		MimoAPIKey:  "mimo-test-key",
		MimoBaseURL: xiaomi.URL,
	})
	srv := httptest.NewServer(impl)
	t.Cleanup(srv.Close)
	body := []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修复一下。"}]}]}`)
	resp := postJSON(t, srv.URL+"/v1/responses", "Bearer client-key", body)
	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s", resp.StatusCode, got)
	}
	if xiaomiCalls != 1 {
		t.Fatalf("mimo calls = %d, want 1", xiaomiCalls)
	}
	if !bytes.Contains(upstream.lastBody(), []byte("Fix it.")) {
		t.Fatalf("turn forwarded untranslated: %s", upstream.lastBody())
	}
}
