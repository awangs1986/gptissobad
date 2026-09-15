package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/awangs1986/gptissobad/internal/opencodego"
)

const (
	ReplyInstruction = "Please reply in Chinese."
	// DefaultTranslateTimeout bounds ONE translator request end to end:
	// DNS, dial, TLS, upload, generation and download. The old 15s could
	// not finish a dense Chinese paragraph on a cheap model, which is why
	// long prompts looked like "the translation service is unreachable".
	DefaultTranslateTimeout = 30 * time.Second
	// DefaultMaxChunkRunes caps how much text one translator request
	// carries. Bigger inputs are split at line boundaries (never dropped).
	DefaultMaxChunkRunes = 1500
	sessionTTL           = 30 * time.Minute
	maxSessions          = 1000
	strategyVersion      = "1"
	failClosedMsg        = "翻译失败，这一轮未发送"
	failClosedStatus     = http.StatusForbidden
	quotaMsg             = "翻译额度用尽，这一轮未发送"
	translatorHanMsg     = "Translator Backend 返回的仍是中文，这一轮未发送"
	translatorTruncMsg   = "翻译结果被截断，这一轮未发送"
	// maxOutputTokensCap bounds the max_tokens we ask for: translation
	// output is comparable to input, so the cap scales with the chunk.
	maxOutputTokensCap = 16384
	// Retry pacing: one extra attempt for transient failures only, and a
	// timeout never retries (the same oversized body would just time out
	// again, doubling the wait before the user sees anything).
	retryBackoffNet    = 300 * time.Millisecond
	retryBackoffServer = 500 * time.Millisecond
	retryBackoffRate   = 1500 * time.Millisecond
	// Translation wants determinism, not creativity: temperature 0 plus an
	// explicit never-answer rule. Without it the model often answers
	// Chinese questions in Chinese instead of translating them, and every
	// such answer is billed and then fail-closed, so the user reads it as
	// "Chinese is blocked". Questions must come back as English questions.
	translatorSystem = "Translate the user text into English. Return only the translation, never an answer or explanation. If the input is a question, output only its English translation. Keep code fences, file paths, commands, identifiers, and URLs verbatim."
	userAgent        = "codex-lang/1.0"
)

// errForwardOriginal is not a failure: the turn cannot be translated
// (secret-held or keyless) but policy says forward it exactly as received.
var errForwardOriginal = errors.New("forward original")

type Config struct {
	Upstream      string
	TranslatorURL string
	APIKey        string
	Model         string
	// MimoBaseURL/MimoAPIKey route mimo* models to the Xiaomi direct
	// endpoint. Empty MimoAPIKey falls back to ResolveMimoKey()
	// (~/.codex/mimo.key, $MIMO_API_KEY); without any key the OpenCode
	// chain serves mimo too. An explicit TranslatorURL wins over all.
	MimoBaseURL string
	MimoAPIKey  string
	// FallbackModel retries a Han-bearing primary translation once via the
	// Responses dialect (e.g. muse-spark-1.3-contributor). Empty disables.
	FallbackModel string
	// FallbackURL overrides the Responses endpoint (tests). Empty means
	// the fixed OpenCode Go Responses URL.
	FallbackURL string
	// TranslateTimeout bounds ONE translator request; zero means
	// DefaultTranslateTimeout. Bigger chunks get proportionally more time
	// (timeoutFor), so a dense paragraph is never fail-closed just for
	// being long.
	TranslateTimeout time.Duration
	// MaxChunkRunes caps how much text one translator request carries;
	// zero means DefaultMaxChunkRunes. Oversized text is split at line
	// boundaries and reassembled, never dropped.
	MaxChunkRunes int
	// CacheFile is the on-disk Translate Cache (hash → Translated Prompt).
	// Empty means memory only. The file never stores the User Prompt.
	CacheFile string
	// CacheReadOnly loads the cache file at startup but never writes it.
	// Only the Watchdog gateway may persist; codex-translate runs read-only
	// so two processes never clobber each other's full-map snapshots.
	CacheReadOnly bool
	Log           func(string)
}

type Gateway struct {
	cfg         Config
	upstream    *url.URL
	client      *http.Client
	translate   *http.Client
	sessionID   string
	cacheMu     sync.Mutex
	cache       map[string]string
	cacheDirty  bool
	cacheTimer  *time.Timer
	translating atomic.Int64
	sessionsMu  sync.Mutex
	sessions    map[string]sessionSnapshot
	metricsMu   sync.Mutex
	metrics     Metrics
}

type sessionSnapshot struct {
	items        []string
	translations map[string]string
	generation   uint64
	lastSeen     time.Time
}

// Metrics is deliberately aggregate-only: it never contains prompt text,
// translations, headers, or credentials.
type Metrics struct {
	TranslationRequests uint64 `json:"translationRequests"`
	CacheHits           uint64 `json:"cacheHits"`
	Rejected            uint64 `json:"rejected"`
	IncrementalRequests uint64 `json:"incrementalRequests"`
	FullRebuilds        uint64 `json:"fullRebuilds"`
	ActiveSessions      uint64 `json:"activeSessions"`
	LastLatencyMs       int64  `json:"lastLatencyMs"`
	TranslatedChars     uint64 `json:"translatedChars"`
	PromptTokens        uint64 `json:"promptTokens"`
	CompletionTokens    uint64 `json:"completionTokens"`
	TotalTokens         uint64 `json:"totalTokens"`
	FallbackRequests    uint64 `json:"fallbackRequests"`
	// Translating reports live in-flight translator calls. Best effort:
	// short calls may start and finish between two polls.
	Translating bool `json:"translating"`
}

// TranslationUsage carries the Translator Backend usage block for one
// successful chunk. It never contains prompt text.
type TranslationUsage struct {
	PromptTokens     uint64
	CompletionTokens uint64
	TotalTokens      uint64
}

func New(cfg Config) *Gateway {
	u, err := url.Parse(strings.TrimSpace(cfg.Upstream))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		u = nil
	}
	timeout := cfg.TranslateTimeout
	if timeout <= 0 {
		timeout = DefaultTranslateTimeout
	}
	if cfg.MaxChunkRunes <= 0 {
		cfg.MaxChunkRunes = DefaultMaxChunkRunes
	}
	if cfg.Model == "" {
		cfg.Model = "mimo-v2.5"
	}
	// TranslatorURL intentionally left empty by default: translatorTarget
	// routes by model (mimo direct vs OpenCode chain) unless overridden.
	cfg.TranslateTimeout = timeout
	// The upstream client must not hang forever: without any timeout a dead
	// upstream parks a goroutine and a connection until the Codex client
	// gives up, instead of failing closed to 403. ResponseHeaderTimeout
	// bounds a silent upstream; established SSE streams are left alone on
	// purpose — an overall deadline would kill legitimate long turns.
	upstreamDialer := &net.Dialer{Timeout: 10 * time.Second}
	g := &Gateway{
		cfg:      cfg,
		upstream: u,
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           upstreamDialer.DialContext,
				ResponseHeaderTimeout: 180 * time.Second,
			},
		},
		translate: &http.Client{
			// Deadlines come from the per-request context in callTranslator,
			// sized to the chunk; a fixed Client.Timeout here would cap the
			// size-aware deadline straight back down. Proxy is explicit
			// rather than inherited from http.DefaultTransport so the
			// translator and the upstream agree on routing.
			Transport: &http.Transport{
				Proxy:           http.ProxyFromEnvironment,
				DialContext:     (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				IdleConnTimeout: 90 * time.Second,
			},
		},
		sessionID: "codex-lang",
		cache:     map[string]string{},
		sessions:  map[string]sessionSnapshot{},
	}
	g.loadCache()
	return g
}

func (g *Gateway) Metrics() Metrics {
	g.metricsMu.Lock()
	defer g.metricsMu.Unlock()
	m := g.metrics
	m.Translating = g.translating.Load() > 0
	return m
}

// CheckTranslator performs one small authenticated request without sending
// anything to the configured Codex upstream.
func (g *Gateway) CheckTranslator(ctx context.Context) error {
	// Probe the route that actually serves the configured model with that
	// route's own key: a mimo-only setup is checked at the Xiaomi endpoint
	// instead of being rejected for a missing OpenCode Go key.
	if _, key := g.translatorTarget(); strings.TrimSpace(key) == "" {
		return errors.New("未找到翻译服务凭据（OpenCode Go 或 MiMo）")
	}
	raw, status, err := g.callTranslator(ctx, "Reply with OK.")
	if err != nil {
		return fmt.Errorf("连接翻译服务失败: %w", err)
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("翻译服务返回 HTTP %d，API key 无效或已过期", status)
	case http.StatusNotFound:
		return errors.New("翻译服务返回 HTTP 404，接口或模型不可用")
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("翻译服务返回 HTTP %d", status)
	}
	if _, _, _, err := parseTranslation(raw); err != nil {
		return errors.New("翻译服务已响应，但返回格式不是 Chat Completions")
	}
	return nil
}

func (g *Gateway) metric(fn func(*Metrics)) {
	g.metricsMu.Lock()
	fn(&g.metrics)
	g.metricsMu.Unlock()
}

func (g *Gateway) logf(format string, args ...any) {
	if g.cfg.Log == nil {
		return
	}
	g.cfg.Log(fmt.Sprintf(format, args...))
}

// translateFailure is the machine-readable cause behind a fail-closed turn.
// It carries class/status/endpoint/byte counts into the log and never the
// prompt text, headers or credentials: the endpoint is stripped to
// scheme+host+path and the wrapped error has its query string cut off.
type translateFailure struct {
	msg      string // exact policy message the Codex client receives
	class    string // timeout | canceled | network | http | too-large | rate | quota | invalid | truncated | han
	status   int
	endpoint string
	bytes    int // response bytes; 0 when the request never got an answer
	err      error
}

func (f *translateFailure) Error() string { return f.msg }

func (f *translateFailure) Unwrap() error { return f.err }

func newFailure(class, msg, endpoint string, status, bytes int, err error) *translateFailure {
	return &translateFailure{
		msg:      msg,
		class:    class,
		status:   status,
		endpoint: redactEndpoint(endpoint),
		bytes:    bytes,
		err:      err,
	}
}

// redactEndpoint drops credentials that may ride in a query string.
func redactEndpoint(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "unknown"
	}
	return u.Scheme + "://" + u.Host + u.Path
}

func failureClass(err error) string {
	var f *translateFailure
	if errors.As(err, &f) {
		return f.class
	}
	return ""
}

// quotaNamed implements translation-policy.md §5: 429/402 only mean quota
// exhaustion when the body names 额度. A bare rate limit is transient and
// must not tell the user the quota is gone.
func quotaNamed(raw []byte) bool {
	return bytes.Contains(raw, []byte("额度"))
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if i := strings.Index(msg, "?"); i >= 0 {
		msg = msg[:i] + "?…"
	}
	return fmt.Sprintf(" err=%q", msg)
}

// logFailClosed names the cause of one fail-closed turn for the operator:
// error class, HTTP status, redacted endpoint, response bytes and the
// transport error. It never logs prompt text or credentials.
func (g *Gateway) logFailClosed(method, path string, err error) {
	var f *translateFailure
	if !errors.As(err, &f) {
		g.logf("fail-closed %s %s", method, path)
		return
	}
	g.logf("fail-closed %s %s class=%s status=%d endpoint=%s bytes=%d%s",
		method, path, f.class, f.status, f.endpoint, f.bytes, sanitizeError(f.err))
}

// timeoutFor scales the base timeout with the chunk size: a max-size chunk
// gets up to 2× the base, so raising MaxChunkRunes cannot reintroduce the
// "long prompt looks unreachable" timeout.
func (g *Gateway) timeoutFor(chunk string) time.Duration {
	base := g.cfg.TranslateTimeout
	if base <= 0 {
		base = DefaultTranslateTimeout
	}
	limit := g.cfg.MaxChunkRunes
	if limit <= 0 {
		limit = DefaultMaxChunkRunes
	}
	runes := len([]rune(chunk))
	if runes <= limit {
		return base
	}
	extra := time.Duration(float64(base) * 0.5 * float64(runes-limit) / float64(limit))
	if extra > base {
		extra = base
	}
	return base + extra
}

// maxOutputTokensFor asks for enough tokens to translate the chunk and no
// more. Without an explicit cap a cut-off answer used to be forwarded as a
// half-translated prompt; now truncation is detected and fails closed.
func maxOutputTokensFor(chunk string) int {
	n := len([]rune(chunk)) * 2
	if n < 1024 {
		n = 1024
	}
	if n > maxOutputTokensCap {
		n = maxOutputTokensCap
	}
	return n
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if isWebSocket(r) {
		g.logf("reject websocket %s %s", r.Method, path)
		http.Error(w, failClosedMsg, failClosedStatus)
		return
	}
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		g.logf("fail-closed %s %s read body", r.Method, path)
		http.Error(w, failClosedMsg, failClosedStatus)
		return
	}
	// Fail-open posture (translation-policy.md): content is never a reason
	// to refuse. Query, headers, body shapes and non-user text all forward
	// as-is; only translator-side failures fail closed. The method plays no
	// role in routing to translation, but only POST turns enter it.
	if len(bytes.TrimSpace(body)) == 0 {
		g.logf("proxy %s %s empty", r.Method, path)
		g.proxy(w, r, bytes.NewReader(body))
		return
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		g.logf("proxy %s %s undecodable", r.Method, path)
		g.proxy(w, r, bytes.NewReader(body))
		return
	}
	if r.Method != http.MethodPost || !needsRewrite(body) {
		g.logf("proxy %s %s", r.Method, path)
		g.proxy(w, r, bytes.NewReader(body))
		return
	}
	rewritten, translated, err := g.rewrite(r.Context(), body, r.Header)
	if err != nil {
		if errors.Is(err, errForwardOriginal) {
			// Untranslatable but forwardable (secret-held or keyless):
			// exactly as received, no instruction attached.
			g.logf("proxy %s %s untranslated", r.Method, path)
			g.proxy(w, r, bytes.NewReader(body))
			return
		}
		g.metric(func(m *Metrics) { m.Rejected++ })
		g.logFailClosed(r.Method, path, err)
		http.Error(w, err.Error(), failClosedStatus)
		return
	}
	// No post-verify: under fail-open, forwarded Han is policy, and the
	// translator's own output was already re-checked per chunk.
	// The log tells whether the Translator Backend ran, not whether the
	// bytes changed: English turns now also carry the Reply Instruction.
	if translated {
		g.logf("translated %s %s", r.Method, path)
	} else {
		g.logf("passthrough %s %s", r.Method, path)
	}
	g.proxy(w, r, bytes.NewReader(rewritten))
}

func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// rewrite returns the body to forward and whether the Translator Backend
// ran. The Reply Instruction rides every turn that has user text —
// translated or not — so answers stay Chinese without spending translation
// tokens on English that needs no translation.
func (g *Gateway) rewrite(ctx context.Context, body []byte, headers http.Header) ([]byte, bool, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false, errors.New(failClosedMsg)
	}
	key := requestSessionKey(payload, headers)
	items := conversationItems(payload)
	working, incremental := g.beginSession(key, items)
	if incremental {
		g.metric(func(m *Metrics) { m.IncrementalRequests++ })
	} else {
		g.metric(func(m *Metrics) { m.FullRebuilds++ })
	}
	changed := false
	err := walkTranslatableTexts(payload, func(text string) (string, error) {
		out, did, err := g.translateTextWithCache(ctx, text, working.translations, true)
		if err != nil {
			return "", err
		}
		if did {
			changed = true
		}
		return out, nil
	})
	if err != nil {
		return nil, false, err
	}
	g.commitSession(key, working)
	prependReplyInstruction(payload)
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, false, errors.New(failClosedMsg)
	}
	return out, changed, nil
}

func (g *Gateway) beginSession(key string, items []string) (sessionSnapshot, bool) {
	working := sessionSnapshot{
		items:        append([]string(nil), items...),
		translations: map[string]string{},
		generation:   1,
		lastSeen:     time.Now(),
	}
	if key == "" {
		return working, false
	}
	g.sessionsMu.Lock()
	defer g.sessionsMu.Unlock()
	g.expireSessionsLocked(working.lastSeen)
	previous, exists := g.sessions[key]
	incremental := exists && isPrefix(previous.items, items)
	working.generation = previous.generation + 1
	if incremental {
		for k, v := range previous.translations {
			working.translations[k] = v
		}
	}
	return working, incremental
}

func (g *Gateway) commitSession(key string, working sessionSnapshot) {
	if key == "" {
		return
	}
	g.sessionsMu.Lock()
	defer g.sessionsMu.Unlock()
	if existing, ok := g.sessions[key]; ok && existing.generation >= working.generation {
		g.metric(func(m *Metrics) { m.ActiveSessions = uint64(len(g.sessions)) })
		return
	}
	g.expireSessionsLocked(working.lastSeen)
	if _, ok := g.sessions[key]; !ok {
		g.evictOldestLocked()
	}
	g.sessions[key] = working
	g.metric(func(m *Metrics) { m.ActiveSessions = uint64(len(g.sessions)) })
}

func (g *Gateway) expireSessionsLocked(now time.Time) {
	for id, s := range g.sessions {
		if now.Sub(s.lastSeen) > sessionTTL {
			delete(g.sessions, id)
		}
	}
}

func (g *Gateway) evictOldestLocked() {
	if len(g.sessions) < maxSessions {
		return
	}
	var oldestID string
	var oldest time.Time
	for id, s := range g.sessions {
		if oldestID == "" || s.lastSeen.Before(oldest) {
			oldestID, oldest = id, s.lastSeen
		}
	}
	if oldestID != "" {
		delete(g.sessions, oldestID)
	}
}

func requestSessionKey(payload map[string]any, headers http.Header) string {
	for _, k := range []string{"X-Conversation-ID", "X-Thread-ID", "X-Session-ID", "X-Codex-Conversation-ID"} {
		if v := strings.TrimSpace(headers.Get(k)); v != "" {
			return "header:" + v
		}
	}
	// previous_response_id is deliberately absent: it changes every turn
	// (it names the previous response), so keying on it makes every turn a
	// new session, defeats prefix matching, and piles a record per turn up
	// to maxSessions. Such requests fall back to the request-scoped path.
	for _, k := range []string{"conversation_id", "conversationId", "thread_id", "threadId", "session_id", "sessionId"} {
		if v, ok := payload[k].(string); ok && strings.TrimSpace(v) != "" {
			return "id:" + v
		}
	}
	return ""
}

func conversationItems(payload map[string]any) []string {
	for _, key := range []string{"input", "messages"} {
		switch items := payload[key].(type) {
		case string:
			return []string{items}
		case []any:
			result := make([]string, 0, len(items))
			for _, item := range items {
				raw, err := json.Marshal(item)
				if err == nil {
					result = append(result, string(raw))
				}
			}
			return result
		}
	}
	return nil
}

func isPrefix(old, current []string) bool {
	if len(current) < len(old) {
		return false
	}
	for i := range old {
		if old[i] != current[i] {
			return false
		}
	}
	return true
}

// prependReplyInstruction puts the Reply Instruction at the FRONT of the
// turn (first user text), for both translated and English turns, exactly
// once. Front placement per translation-policy.md §4.
func prependReplyInstruction(payload map[string]any) {
	for _, key := range []string{"input", "messages"} {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		if text, ok := raw.(string); ok {
			if !strings.Contains(text, ReplyInstruction) {
				payload[key] = ReplyInstruction + "\n" + text
			}
			return
		}
		items, ok := raw.([]any)
		if !ok {
			return
		}
		for i := 0; i < len(items); i++ {
			m, ok := items[i].(map[string]any)
			if !ok || !strings.EqualFold(fmt.Sprint(m["role"]), "user") {
				continue
			}
			prependReplyToContent(m, "content")
			return
		}
		return
	}
}

func prependReplyToContent(parent map[string]any, key string) {
	prefix := ReplyInstruction + "\n"
	switch content := parent[key].(type) {
	case string:
		if !strings.Contains(content, ReplyInstruction) {
			parent[key] = prefix + content
		}
	case []any:
		for i := 0; i < len(content); i++ {
			pm, ok := content[i].(map[string]any)
			if !ok {
				continue
			}
			typ, _ := pm["type"].(string)
			if !isTranslatablePart(typ) {
				continue
			}
			text, _ := pm["text"].(string)
			if !strings.Contains(text, ReplyInstruction) {
				pm["text"] = prefix + text
			}
			return
		}
	}
}

func (g *Gateway) translateText(ctx context.Context, text string) (string, bool, error) {
	return g.translateTextWithCache(ctx, text, nil, true)
}

func (g *Gateway) translateTextWithCache(ctx context.Context, text string, sessionCache map[string]string, allowGlobalCache bool) (string, bool, error) {
	if !hasHanOutsidePaths(text) {
		return text, false, nil
	}
	masked, heldPaths := maskHanPaths(text)
	// The credential test mirrors translatorTarget: whichever route serves
	// this model must have a key. Keyless turns forward as received (policy
	// §5) instead of being fail-closed for a key the user never needed.
	if !g.hasTranslatorKey() {
		return "", false, errForwardOriginal
	}
	pieces := g.buildPieces(masked)
	secretOnly := true
	needTranslate := false
	for _, p := range pieces {
		if p.secret {
			continue
		}
		secretOnly = false
		if hasHan(p.text) {
			needTranslate = true
		}
	}
	if secretOnly {
		return "", false, errForwardOriginal
	}
	if !needTranslate {
		return text, false, nil
	}
	var b strings.Builder
	started := false
	for _, p := range pieces {
		out := p.text
		if !p.secret && hasHan(p.text) {
			key := g.cacheKey(p.text)
			cached, ok := sessionCache[key]
			if !ok {
				var err error
				cached, err = g.translateChunkCached(ctx, p.text, allowGlobalCache)
				if err != nil {
					return "", false, err
				}
				if sessionCache != nil {
					sessionCache[key] = cached
				}
			}
			out = cached
		}
		if started {
			b.WriteString(p.sep)
		}
		b.WriteString(out)
		started = true
	}
	joined := b.String()
	if len(heldPaths) > 0 {
		restored, ok := unmaskHanPaths(joined, heldPaths)
		if !ok {
			return "", false, newFailure("invalid", failClosedMsg, "", 0, 0,
				errors.New("masked path placeholder was lost in translation"))
		}
		joined = restored
	}
	return joined, true, nil
}

// chunkPiece is one translator request's worth of masked text. sep is the
// separator written before this piece when reassembling: "\n" between whole
// lines (the historical behavior) and "" when a single over-long line was
// hard-split, so reassembly never invents a newline.
type chunkPiece struct {
	text   string
	secret bool
	sep    string
}

// buildPieces turns masked text into requests. Two knobs only: secret lines
// ride verbatim as their own piece, and a piece never exceeds MaxChunkRunes.
// A run of lines is packed up to the cap; a single line longer than the cap
// is hard-split at a rune boundary (splitting inside a line is ugly but it
// is exactly what keeps a huge paste from timing out at 15s, and no byte is
// ever dropped).
func (g *Gateway) buildPieces(masked string) []chunkPiece {
	limit := g.cfg.MaxChunkRunes
	if limit <= 0 {
		limit = DefaultMaxChunkRunes
	}
	var pieces []chunkPiece
	var buf []string
	size := 0
	flush := func() {
		if len(buf) == 0 {
			return
		}
		pieces = append(pieces, chunkPiece{text: strings.Join(buf, "\n"), sep: "\n"})
		buf = nil
		size = 0
	}
	for _, line := range strings.Split(masked, "\n") {
		if isSecretLine(line) {
			flush()
			// Secrets never enter the Translator Backend, Han or not; the
			// line rides along verbatim while siblings still translate.
			pieces = append(pieces, chunkPiece{text: line, secret: true, sep: "\n"})
			continue
		}
		runes := []rune(line)
		if len(runes) > limit {
			flush()
			first := true
			for len(runes) > 0 {
				n := limit
				if len(runes) < n {
					n = len(runes)
				}
				sep := ""
				if first {
					sep, first = "\n", false
				}
				pieces = append(pieces, chunkPiece{text: string(runes[:n]), sep: sep})
				runes = runes[n:]
			}
			continue
		}
		if len(buf) > 0 && size+len(runes) > limit {
			flush()
		}
		buf = append(buf, line)
		size += len(runes) + 1
	}
	flush()
	return pieces
}

// hasTranslatorKey reports whether the route that would serve the configured
// model has a credential, using exactly the same resolution as
// translatorTarget (so a mimo-only setup counts as configured).
func (g *Gateway) hasTranslatorKey() bool {
	_, key := g.translatorTarget()
	return strings.TrimSpace(key) != ""
}

// hasFallbackKey reports whether the fallback (always the OpenCode Responses
// dialect, unless a custom FallbackURL is configured) can authenticate. A
// keyless fallback attempt would just burn the chunk's remaining timeout on
// a guaranteed 401.
func (g *Gateway) hasFallbackKey() bool {
	if strings.TrimSpace(g.cfg.FallbackURL) != "" {
		return true
	}
	return strings.TrimSpace(g.cfg.APIKey) != ""
}

func isSecretLine(line string) bool {
	trim := strings.TrimSpace(line)
	if strings.HasPrefix(trim, "sk-") {
		return true
	}
	if strings.Contains(line, "Bearer ") {
		return true
	}
	lower := strings.ToLower(line)
	return strings.Contains(lower, "api_key=")
}

func (g *Gateway) translateChunk(ctx context.Context, chunk string) (string, error) {
	return g.translateChunkCached(ctx, chunk, true)
}

func (g *Gateway) translateChunkCached(ctx context.Context, chunk string, allowGlobalCache bool) (string, error) {
	key := g.cacheKey(chunk)
	if allowGlobalCache {
		if hit, ok := g.cacheGet(key); ok {
			g.metric(func(m *Metrics) { m.CacheHits++ })
			return hit, nil
		}
	}
	g.metric(func(m *Metrics) { m.TranslationRequests++ })
	started := time.Now()
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		res := g.translateOnce(ctx, chunk)
		if res.err == nil {
			g.metric(func(m *Metrics) { m.LastLatencyMs = time.Since(started).Milliseconds() })
			g.cacheSet(key, res.out)
			return res.out, nil
		}
		last = res.err
		if !res.retry {
			break
		}
		// One paced retry: resending the same body instantly just burns a
		// second full timeout before the user hears anything.
		if attempt == 0 && !sleepContext(ctx, res.backoff) {
			break
		}
	}
	if last == nil {
		last = newFailure("unknown", failClosedMsg, "", 0, 0, nil)
	}
	// Single fallback site for every primary failure mode (Han output,
	// timeout, 5xx, quota, unreachable): one attempt on the fallback model,
	// then the original error stands. Session entries stay keyed by the
	// primary model; the text is post-verified English either way.
	if strings.TrimSpace(g.cfg.FallbackModel) != "" && g.hasFallbackKey() {
		fb, fusage, ferr := g.translateFallbackOnce(ctx, chunk)
		if ferr == nil {
			g.metric(func(m *Metrics) {
				m.FallbackRequests++
				m.PromptTokens += fusage.PromptTokens
				m.CompletionTokens += fusage.CompletionTokens
				m.TotalTokens += fusage.TotalTokens
				m.TranslatedChars += uint64(len([]rune(chunk)))
			})
			g.logf("fallback translation used")
			if allowGlobalCache {
				g.cacheSet(g.cacheKeyFor(g.cfg.FallbackModel, chunk), fb)
			}
			return fb, nil
		}
		// Either side naming quota is the actionable signal; keep it.
		if failureClass(ferr) == "quota" {
			return "", ferr
		}
		if failureClass(last) == "quota" {
			return "", last
		}
	}
	return "", last
}

// attemptOutcome is one primary translator attempt: its output, the failure
// (if any), whether that failure is worth one retry, and the pacing before
// that retry.
type attemptOutcome struct {
	out     string
	err     error
	retry   bool
	backoff time.Duration
}

func (g *Gateway) translateOnce(ctx context.Context, chunk string) attemptOutcome {
	endpoint, _ := g.translatorTarget()
	raw, status, err := g.callTranslator(ctx, chunk)
	if err != nil {
		class, retry, backoff := "network", true, retryBackoffNet
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			// Same body, same budget: retrying only doubles the wait.
			class, retry, backoff = "timeout", false, 0
		case errors.Is(err, context.Canceled):
			class, retry, backoff = "canceled", false, 0
		}
		return attemptOutcome{
			err:     newFailure(class, failClosedMsg, endpoint, status, len(raw), err),
			retry:   retry,
			backoff: backoff,
		}
	}
	if status >= 500 {
		return attemptOutcome{
			err:     newFailure("http", failClosedMsg, endpoint, status, len(raw), nil),
			retry:   true,
			backoff: retryBackoffServer,
		}
	}
	if status == http.StatusTooManyRequests || status == http.StatusPaymentRequired {
		if quotaNamed(raw) {
			return attemptOutcome{err: newFailure("quota", quotaMsg, endpoint, status, len(raw), nil)}
		}
		// A bare 429 is a busy backend, not an empty account: one paced
		// retry, then fail closed without claiming the quota is gone.
		return attemptOutcome{
			err:     newFailure("rate", failClosedMsg, endpoint, status, len(raw), nil),
			retry:   true,
			backoff: retryBackoffRate,
		}
	}
	if status >= 400 {
		class := "http"
		if status == http.StatusRequestEntityTooLarge || status == http.StatusRequestURITooLong {
			class = "too-large"
		}
		return attemptOutcome{err: newFailure(class, failClosedMsg, endpoint, status, len(raw), nil)}
	}
	out, usage, truncated, err := parseTranslation(raw)
	if err != nil {
		// A 2xx that is not a Chat Completions body: fail closed, but keep
		// the parse error so the log can name the shape.
		return attemptOutcome{err: newFailure("invalid", failClosedMsg, endpoint, status, len(raw), err)}
	}
	// The Translator Backend was called and billed even when its output is
	// unusable, so count tokens before the truncation/Han checks. Chars only
	// count when the translation is actually forwarded.
	g.metric(func(m *Metrics) {
		m.PromptTokens += usage.PromptTokens
		m.CompletionTokens += usage.CompletionTokens
		m.TotalTokens += usage.TotalTokens
	})
	if truncated {
		// Half a translation is worse than none: forwarding a cut-off
		// prompt used to look like success while the model answered
		// something the user never asked.
		return attemptOutcome{err: newFailure("truncated", translatorTruncMsg, endpoint, status, len(raw), nil)}
	}
	if hasHanOutsidePaths(out) {
		return attemptOutcome{err: newFailure("han", translatorHanMsg, endpoint, status, len(raw), nil)}
	}
	g.metric(func(m *Metrics) {
		m.TranslatedChars += uint64(len([]rune(chunk)))
	})
	return attemptOutcome{out: out}
}

// translateFallbackOnce retries one Han-bearing primary translation on the
// fallback model via the Responses dialect. Single attempt, no recursion:
// if this also returns Han, the turn fails closed as before.
func (g *Gateway) translateFallbackOnce(ctx context.Context, chunk string) (string, TranslationUsage, error) {
	model := strings.TrimSpace(g.cfg.FallbackModel)
	if model == "" {
		return "", TranslationUsage{}, errors.New("no fallback model")
	}
	endpoint := strings.TrimSpace(g.cfg.FallbackURL)
	if endpoint == "" {
		endpoint = opencodego.ResponsesURL
	}
	ctx, cancel := context.WithTimeout(ctx, g.timeoutFor(chunk))
	defer cancel()
	reqBody, err := json.Marshal(map[string]any{
		"model":             model,
		"max_output_tokens": maxOutputTokensFor(chunk),
		"input": []map[string]any{
			{"type": "message", "role": "system", "content": []map[string]string{
				{"type": "input_text", "text": translatorSystem},
			}},
			{"type": "message", "role": "user", "content": []map[string]string{
				{"type": "input_text", "text": chunk},
			}},
		},
	})
	if err != nil {
		return "", TranslationUsage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", TranslationUsage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+g.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("x-opencode-session", g.sessionID)
	resp, err := g.doTranslate(req)
	if err != nil {
		class := "network"
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			class = "timeout"
		case errors.Is(err, context.Canceled):
			class = "canceled"
		}
		return "", TranslationUsage{}, newFailure(class, failClosedMsg, endpoint, 0, 0, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", TranslationUsage{}, newFailure("network", failClosedMsg, endpoint, resp.StatusCode, 0, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusPaymentRequired {
		if quotaNamed(raw) {
			return "", TranslationUsage{}, newFailure("quota", quotaMsg, endpoint, resp.StatusCode, len(raw), nil)
		}
		return "", TranslationUsage{}, newFailure("rate", failClosedMsg, endpoint, resp.StatusCode, len(raw), nil)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", TranslationUsage{}, newFailure("http", failClosedMsg, endpoint, resp.StatusCode, len(raw), nil)
	}
	out, usage, truncated, err := parseResponsesTranslation(raw)
	if err != nil {
		return "", TranslationUsage{}, newFailure("invalid", failClosedMsg, endpoint, resp.StatusCode, len(raw), err)
	}
	if truncated {
		return "", TranslationUsage{}, newFailure("truncated", translatorTruncMsg, endpoint, resp.StatusCode, len(raw), nil)
	}
	if hasHanOutsidePaths(out) {
		return "", TranslationUsage{}, newFailure("han", translatorHanMsg, endpoint, resp.StatusCode, len(raw), nil)
	}
	return out, usage, nil
}

func parseResponsesTranslation(raw []byte) (string, TranslationUsage, bool, error) {
	var parsed struct {
		Status            string `json:"status"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  uint64 `json:"input_tokens"`
			OutputTokens uint64 `json:"output_tokens"`
			TotalTokens  uint64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return "", TranslationUsage{}, false, errors.New("invalid Responses response")
	}
	truncated := parsed.Status == "incomplete" &&
		(parsed.IncompleteDetails.Reason == "max_output_tokens" || parsed.IncompleteDetails.Reason == "max_tokens")
	var parts []string
	for _, item := range parsed.Output {
		if item.Type != "message" {
			continue
		}
		for _, c := range item.Content {
			if c.Type != "output_text" {
				continue
			}
			if strings.TrimSpace(c.Text) != "" {
				parts = append(parts, strings.TrimSpace(c.Text))
			}
		}
	}
	usage := TranslationUsage{
		PromptTokens:     parsed.Usage.InputTokens,
		CompletionTokens: parsed.Usage.OutputTokens,
		TotalTokens:      parsed.Usage.TotalTokens,
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if truncated {
		// Partial text is not a translation; the caller fails closed.
		return strings.Join(parts, "\n"), usage, true, nil
	}
	if len(parts) == 0 {
		return "", TranslationUsage{}, false, errors.New("empty translation")
	}
	return strings.Join(parts, "\n"), usage, false, nil
}

// doTranslate marks translator calls in flight so the panel icon can
// blink while translating. Both primary and fallback go through here.
func (g *Gateway) doTranslate(req *http.Request) (*http.Response, error) {
	g.translating.Add(1)
	defer g.translating.Add(-1)
	return g.translate.Do(req)
}

func (g *Gateway) callTranslator(ctx context.Context, chunk string) ([]byte, int, error) {
	endpoint, key := g.translatorTarget()
	ctx, cancel := context.WithTimeout(ctx, g.timeoutFor(chunk))
	defer cancel()
	reqBody, err := json.Marshal(map[string]any{
		"model":       g.cfg.Model,
		"temperature": 0,
		// Without a cap the backend's default output limit silently cut long
		// translations in half; with it, parseTranslation sees the truncation
		// and the turn fails closed instead of forwarding half a sentence.
		"max_tokens": maxOutputTokensFor(chunk),
		"messages": []map[string]string{
			{"role": "system", "content": translatorSystem},
			{"role": "user", "content": chunk},
		},
	})
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("x-opencode-session", g.sessionID)
	resp, err := g.doTranslate(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

func parseTranslation(raw []byte) (string, TranslationUsage, bool, error) {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     uint64 `json:"prompt_tokens"`
			CompletionTokens uint64 `json:"completion_tokens"`
			TotalTokens      uint64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &parsed) != nil || len(parsed.Choices) == 0 {
		return "", TranslationUsage{}, false, errors.New("invalid Chat Completions response")
	}
	choice := parsed.Choices[0]
	usage := TranslationUsage{
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		TotalTokens:      parsed.Usage.TotalTokens,
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	out := strings.TrimSpace(choice.Message.Content)
	truncated := choice.FinishReason == "length" || choice.FinishReason == "max_tokens"
	if truncated {
		// Whatever arrived is a fragment; the caller turns this into
		// translatorTruncMsg rather than forwarding half a prompt.
		return out, usage, true, nil
	}
	if out == "" {
		return "", TranslationUsage{}, false, errors.New("empty translation")
	}
	return out, usage, false, nil
}

func (g *Gateway) cacheGet(key string) (string, bool) {
	g.cacheMu.Lock()
	defer g.cacheMu.Unlock()
	v, ok := g.cache[key]
	return v, ok
}

// cachePersistDebounce batches disk writes: translating a long history
// would otherwise rewrite the whole file per chunk (O(n²)). A dirty flag
// plus this timer keeps at most one write per quiet window; Runtime.Stop
// flushes synchronously. Only money is at stake on crash, never
// correctness: Han-free and fail-closed never depend on this cache.
const cachePersistDebounce = 2 * time.Second

func (g *Gateway) cacheSet(key, value string) {
	g.cacheMu.Lock()
	g.cache[key] = value
	if g.cfg.CacheFile == "" || g.cfg.CacheReadOnly {
		g.cacheMu.Unlock()
		return
	}
	g.cacheDirty = true
	if g.cacheTimer != nil {
		g.cacheTimer.Stop()
	}
	g.cacheTimer = time.AfterFunc(cachePersistDebounce, g.flushCache)
	g.cacheMu.Unlock()
}

// FlushCache writes a pending snapshot now. Safe to call anytime; it is a
// no-op when clean, unconfigured, or read-only.
func (g *Gateway) FlushCache() {
	g.flushCache()
}

func (g *Gateway) flushCache() {
	g.cacheMu.Lock()
	if !g.cacheDirty || g.cfg.CacheFile == "" || g.cfg.CacheReadOnly {
		g.cacheMu.Unlock()
		return
	}
	g.cacheDirty = false
	snapshot := maps.Clone(g.cache)
	path := g.cfg.CacheFile
	g.cacheMu.Unlock()
	g.persistCache(path, snapshot)
}

func (g *Gateway) cacheKey(chunk string) string {
	return g.cacheKeyFor(g.cfg.Model, chunk)
}

func (g *Gateway) cacheKeyFor(model, chunk string) string {
	// translatorSystem is part of the key on purpose: retuning the prompt
	// must invalidate old entries, or the improvement silently never hits.
	// The model is in the key so primary and fallback entries never collide.
	sum := sha256.Sum256([]byte(strategyVersion + "\x00" + model + "\x00" + translatorSystem + "\x00" + chunk))
	return hex.EncodeToString(sum[:])
}

func (g *Gateway) proxy(w http.ResponseWriter, r *http.Request, body io.Reader) {
	if g.upstream == nil || g.upstream.Host == "" {
		http.Error(w, failClosedMsg, failClosedStatus)
		return
	}
	target := *r.URL
	dest := g.upstream.ResolveReference(&url.URL{Path: target.Path, RawQuery: target.RawQuery})
	out, err := http.NewRequestWithContext(r.Context(), r.Method, dest.String(), body)
	if err != nil {
		http.Error(w, failClosedMsg, failClosedStatus)
		return
	}
	copyHeaders(out.Header, r.Header)
	// After a rewrite the inbound Content-Length is wrong. bytes.Reader
	// already set out.ContentLength; drop the copied header so Transport
	// sends the size of the body we actually forward.
	out.Header.Del("Content-Length")
	if br, ok := body.(*bytes.Reader); ok {
		out.ContentLength = int64(br.Len())
	}
	out.Host = dest.Host
	resp, err := g.client.Do(out)
	if err != nil {
		g.logf("upstream error %s %s", r.Method, r.URL.Path)
		http.Error(w, failClosedMsg, failClosedStatus)
		return
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			break
		}
	}
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

func ConfigTOML(port string) string {
	return ConfigTOMLFor(port, "/v1")
}

// ConfigTOMLFor renders the Codex-facing TOML for any base path: "/v1"
// for a Cockpit-style upstream, "/backend-api/codex" for direct ChatGPT.
func ConfigTOMLFor(port, basePath string) string {
	if port == "" {
		port = "18787"
	}
	basePath = strings.TrimSpace(basePath)
	if basePath == "" {
		basePath = "/v1"
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	basePath = strings.TrimRight(basePath, "/")
	if basePath == "" {
		basePath = "/"
	}
	return fmt.Sprintf(`# Paste into the user-level ~/.codex/config.toml only.
# Project-local .codex/config.toml ignores these keys.
# Do not reuse the reserved ids openai, ollama, or lmstudio.
# wire_api must be responses. env_key is the next hop's key; the Local Gateway forwards it.
# supports_websockets = false keeps Codex off the transport this rewrite cannot see.
# For direct-ChatGPT upstreams use base path /backend-api/codex with
# requires_openai_auth = true instead of the env_key line.

model_provider = "codex_translate"

[model_providers.codex_translate]
name = "Codex translate"
base_url = "http://127.0.0.1:%s%s"
wire_api = "responses"
supports_websockets = false
env_key = "CODEX_TRANSLATE_KEY"
`, port, basePath)
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
