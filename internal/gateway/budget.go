package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// This file answers two sizing questions the gateway has to guess at, because
// it never sees a tokenizer:
//
//  1. Will this piece still fit in the Translator Backend's window?
//  2. Will the *translated* request still fit in the upstream's window, when
//     the original Chinese did?
//
// Both decisions are made from estimateTokens, which deliberately
// over-estimates: an estimate that is too large costs a little extra
// splitting, one that is too small breaks a turn.

const (
	// English prose runs ~4 characters per token; code, JSON and Chinese
	// mixed with ASCII sit closer to 3. 3 is the safe direction.
	asciiRunesPerToken = 3
	// Han, Kana, Hangul, fullwidth punctuation: roughly 0.5-1 token per
	// rune depending on the tokenizer. Charge one each.
	cjkRunesPerToken = 1
	// Everything else non-ASCII (accented Latin, Cyrillic, emoji): half a
	// token per rune is not a safe assumption, so charge one per two runes.
	otherRunesPerToken = 2

	// translationExpansionPct is how many tokens (as a percentage of the
	// input estimate) a Chinese→English translation is assumed to ADD when
	// the cache does not already know the answer. Modern tokenizers make
	// Chinese cheaper per character than older ones did, so the honest range
	// is "roughly equal" to "+25%"; plan for the expensive side.
	translationExpansionPct = 25
	// upstreamBudgetPct is how much of the configured upstream window the
	// gateway is willing to fill. The rest is headroom for the upstream's
	// own server-side prompt, tool schemas the client adds later, and the
	// error in estimateTokens.
	upstreamBudgetPct = 90
	// maxSplitDepth bounds adaptive splitting when a translator rejects a
	// piece as too large: 1 → 2 halves, 2 → 4, 3 → 8. A backend that
	// rejects even a ~200-rune piece is misconfigured, not oversized, and
	// splitting forever would only turn one 400 into a thousand.
	maxSplitDepth = 3
)

// estimateTokens over-estimates the token count of s. Never zero for
// non-empty input.
func estimateTokens(s string) int {
	return estimateTokensBytes([]byte(s))
}

func estimateTokensBytes(b []byte) int {
	ascii, wide, other := 0, 0, 0
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		i += size
		switch {
		case r < utf8.RuneSelf:
			ascii++
		case isWideRune(r):
			wide++
		default:
			other++
		}
	}
	tokens := tokenCount(ascii, wide, other)
	if tokens == 0 && len(b) > 0 {
		return 1
	}
	return tokens
}

// isWideRune reports whether a rune belongs to a script that costs about a
// token per character.
func isWideRune(r rune) bool {
	if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
		return true
	}
	// CJK punctuation and fullwidth forms.
	return (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF)
}

// runesWithinTokens returns the longest prefix of runes whose token estimate
// stays inside budget, at least one rune (an empty piece would loop forever)
// and at most len(runes). It uses the same accounting as estimateTokens, so a
// piece cut here is inside the budget by the same measure that planned it.
func runesWithinTokens(runes []rune, budget int) int {
	if budget <= 0 {
		return 1
	}
	ascii, wide, other := 0, 0, 0
	for i, r := range runes {
		switch {
		case r < utf8.RuneSelf:
			ascii++
		case isWideRune(r):
			wide++
		default:
			other++
		}
		if tokenCount(ascii, wide, other) > budget {
			if i == 0 {
				return 1
			}
			return i
		}
	}
	return len(runes)
}

// tokenCount is the single accounting rule behind every estimate here.
func tokenCount(ascii, wide, other int) int {
	return wide*cjkRunesPerToken +
		(ascii+asciiRunesPerToken-1)/asciiRunesPerToken +
		(other+otherRunesPerToken-1)/otherRunesPerToken
}

// splitPiece halves one piece so an over-large translator request can be
// retried as two smaller ones. A newline near the middle is preferred (each
// half is then whole lines and reassembly writes the newline back); otherwise
// the cut is at a rune boundary with an empty separator, because the two
// halves are contiguous text and nothing may be invented between them.
// Secret pieces are never split (they are never translated at all).
func splitPiece(p chunkPiece) (chunkPiece, chunkPiece, bool) {
	if p.secret {
		return p, chunkPiece{}, false
	}
	runes := []rune(p.text)
	if len(runes) < 2 {
		return p, chunkPiece{}, false
	}
	mid := len(runes) / 2
	if idx := newlineNear(runes, mid); idx > 0 && idx < len(runes)-1 {
		return chunkPiece{text: string(runes[:idx]), sep: p.sep},
			chunkPiece{text: string(runes[idx+1:]), sep: "\n"}, true
	}
	return chunkPiece{text: string(runes[:mid]), sep: p.sep},
		chunkPiece{text: string(runes[mid:]), sep: ""}, true
}

// newlineNear looks for a line break close to the middle of runes, searching
// outward: a split at a line break keeps both halves grammatical, which is
// worth a few runes of imbalance.
func newlineNear(runes []rune, mid int) int {
	const window = 200
	for d := 0; d < window; d++ {
		if i := mid + d; i < len(runes) && runes[i] == '\n' {
			return i
		}
		if i := mid - d; i > 0 && runes[i] == '\n' {
			return i
		}
	}
	return -1
}

// coveragePlan is how much of a turn may be translated without pushing the
// forwarded request past the configured upstream window.
type coveragePlan struct {
	allowed         []bool
	enabled         bool
	budget          int
	estimated       int
	skipped         int
	overBudgetInput bool
}

func (p coveragePlan) skippedAll() bool {
	if !p.enabled || len(p.allowed) == 0 {
		return false
	}
	for _, ok := range p.allowed {
		if ok {
			return false
		}
	}
	return true
}

// planCoverage decides, before any translator call, how many of texts can be
// translated. The rule is the one the user can predict: the live turn matters
// most, so translation is dropped from the OLDEST text first and the newest
// text is always translated. Text that is dropped rides along in Chinese,
// which translation-policy.md §0/§3 already accepts as better than a hard
// failure.
func (g *Gateway) planCoverage(body []byte, texts []string, sessionCache map[string]string) coveragePlan {
	plan := coveragePlan{allowed: make([]bool, len(texts))}
	for i := range plan.allowed {
		plan.allowed[i] = true
	}
	configured := g.cfg.UpstreamContextTokens
	if configured <= 0 {
		return plan
	}
	plan.enabled = true
	plan.budget = configured * upstreamBudgetPct / 100

	original := estimateTokensBytes(body) + estimateTokens(ReplyInstruction)
	growth := make([]int, len(texts))
	total := original
	for i, text := range texts {
		growth[i] = g.estimatedGrowth(text, sessionCache)
		if growth[i] > 0 {
			total += growth[i]
		}
	}
	plan.estimated = total
	if original > plan.budget {
		// Translation is not what breaks this turn; the request is already
		// over the configured window. Degrading would only hide that, so
		// translate normally and let the log say so.
		plan.overBudgetInput = true
		return plan
	}
	// The newest translatable text is never skipped: translating nothing
	// would send the user's live prompt upstream in Chinese, which is the
	// very failure this gateway exists to prevent. Older history absorbs the
	// whole overrun instead.
	live := -1
	for i, text := range texts {
		if hasHanOutsidePaths(text) {
			live = i
		}
	}
	for i := 0; i < len(texts) && i < live && total > plan.budget; i++ {
		if !plan.allowed[i] || !hasHanOutsidePaths(texts[i]) {
			continue
		}
		plan.allowed[i] = false
		total -= growth[i]
		plan.skipped++
	}
	plan.estimated = total
	return plan
}

// estimatedGrowth estimates how many tokens translation ADDS to one text:
// measured from the cache when the piece is already translated (the common
// case in a long session), the conservative expansion percentage otherwise.
// Negative means the English version is expected to be cheaper in tokens than
// the Chinese one, which older tokenizers really did.
func (g *Gateway) estimatedGrowth(text string, sessionCache map[string]string) int {
	if !hasHanOutsidePaths(text) {
		return 0
	}
	masked, _ := maskHanPaths(text)
	total := 0
	for _, p := range g.buildPieces(masked) {
		if p.secret || !hasHan(p.text) {
			continue
		}
		in := estimateTokens(p.text)
		key := g.cacheKey(p.text)
		out, ok := "", false
		if sessionCache != nil {
			out, ok = sessionCache[key]
		}
		if !ok {
			out, ok = g.cacheGet(key)
		}
		if ok {
			total += estimateTokens(out) - in
			continue
		}
		total += in * translationExpansionPct / 100
	}
	return total
}

// collectTranslatableTexts walks the payload exactly like the translation
// walk does and returns the texts in the same order, without changing
// anything. Two passes over the same walker keep the order guarantee local:
// index i of this slice is the i-th text the rewrite pass will see.
func collectTranslatableTexts(payload map[string]any) []string {
	var texts []string
	_ = walkTranslatableTexts(payload, func(text string) (string, error) {
		texts = append(texts, text)
		return text, nil
	})
	return texts
}

// marshalPayload re-encodes a rewritten payload. HTML escaping is off on
// purpose: json.Marshal turns every <, > and & into six bytes of \u003c,
// which is both a different document than the one the client sent and more
// tokens for the upstream to read. Code-heavy turns are exactly where the
// context window is tightest.
func marshalPayload(payload map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// tooLargeNamed reports whether a translator's error body says the request
// did not fit, which is a size problem the gateway can fix by splitting
// rather than a configuration problem. Bodies are inspected for a few
// markers only; they are never logged or stored.
func tooLargeNamed(raw []byte) bool {
	body := strings.ToLower(string(raw))
	for _, marker := range []string{
		"context_length", "context length", "maximum context", "max context",
		"too large", "too long", "exceeds", "exceeded", "token limit",
		"长度超过", "上下文", "过大", "超长", "上限",
	} {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}
