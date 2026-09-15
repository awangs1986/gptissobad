package gateway

import (
	"strings"
	"testing"
)

// han returns n Han runes (each one estimated as one token by design).
func han(n int) string { return strings.Repeat("中", n) }

func TestEstimateTokensOverEstimates(t *testing.T) {
	cases := []struct {
		name string
		in   string
		min  int
	}{
		{"han is a token per rune", han(100), 100},
		{"ascii is never free", strings.Repeat("a", 300), 100},
		{"empty", "", 0},
		{"mixed", "hello " + han(10) + " world", 10},
	}
	for _, tc := range cases {
		if got := estimateTokens(tc.in); got < tc.min {
			t.Fatalf("%s: estimateTokens=%d, want >= %d", tc.name, got, tc.min)
		}
	}
	if estimateTokens("x") == 0 {
		t.Fatal("one rune must not estimate to zero tokens")
	}
	// The point of the estimator is to be an upper bound in the direction
	// that matters: Han must cost at least as much as the ASCII text that
	// carries the same number of runes.
	if estimateTokens(han(30)) <= estimateTokens(strings.Repeat("a", 30)) {
		t.Fatal("Han must estimate heavier than ASCII")
	}
}

func TestRunesWithinTokens(t *testing.T) {
	runes := []rune(han(100))
	if got := runesWithinTokens(runes, 10); got != 10 {
		t.Fatalf("Han budget 10 gave %d runes, want 10", got)
	}
	if got := runesWithinTokens(runes, 0); got != 1 {
		t.Fatalf("zero budget must still make progress, got %d", got)
	}
	if got := runesWithinTokens(runes, 10000); got != len(runes) {
		t.Fatalf("huge budget must take everything, got %d", got)
	}
	ascii := []rune(strings.Repeat("a", 300))
	if got := runesWithinTokens(ascii, 10); got < 25 || got > 31 {
		t.Fatalf("ASCII budget 10 gave %d runes, want ~30", got)
	}
}

func TestSplitPiecePrefersANewline(t *testing.T) {
	left, right, ok := splitPiece(chunkPiece{text: "aaaa\nbbbb", sep: "\n"})
	if !ok {
		t.Fatal("a two-line piece must split")
	}
	if left.text != "aaaa" || right.text != "bbbb" {
		t.Fatalf("split fell on the newline? left=%q right=%q", left.text, right.text)
	}
	if right.sep != "\n" {
		t.Fatalf("the newline must be written back, sep=%q", right.sep)
	}
	if left.sep != "\n" {
		t.Fatalf("the left half keeps the original separator, sep=%q", left.sep)
	}
	if left.text+right.text == left.text+right.text && strings.Contains(left.text, "\n") {
		t.Fatal("the left half still carries the newline")
	}
}

func TestSplitPieceWithoutNewlineInventsNothing(t *testing.T) {
	src := han(40) + "tail"
	left, right, ok := splitPiece(chunkPiece{text: src, sep: "\n"})
	if !ok {
		t.Fatal("a long single line must still split")
	}
	if right.sep != "" {
		t.Fatalf("contiguous halves must not be joined with %q", right.sep)
	}
	if left.text+right.text != src {
		t.Fatalf("halves do not reassemble: %q + %q", left.text, right.text)
	}
}

func TestSplitPieceNeverTouchesSecretsOrSingletons(t *testing.T) {
	secret := chunkPiece{text: strings.Repeat("sk-", 20), secret: true}
	if _, _, ok := splitPiece(secret); ok {
		t.Fatal("a secret line must never be split or sent")
	}
	if _, _, ok := splitPiece(chunkPiece{text: "中"}); ok {
		t.Fatal("a one-rune piece cannot split")
	}
}

func newBudgetGateway(t *testing.T, tokens int) *Gateway {
	t.Helper()
	return New(Config{APIKey: "test-key", Model: "mimo-v2.5", UpstreamContextTokens: tokens})
}

func TestPlanCoverageDisabledByDefault(t *testing.T) {
	g := newBudgetGateway(t, 0)
	texts := []string{han(5000), han(5000)}
	plan := g.planCoverage([]byte("{}"), texts, nil)
	if plan.enabled || plan.skipped != 0 {
		t.Fatalf("no configured window must mean no degradation: %+v", plan)
	}
	if !plan.allowed[0] || !plan.allowed[1] {
		t.Fatal("every text must stay allowed")
	}
}

func TestPlanCoverageDropsTheOldestFirst(t *testing.T) {
	// A body whose three texts fit before translation and do not fit after:
	// the window is placed exactly between those two estimates, so the only
	// way to fit is to translate less.
	old1, old2, live := han(400), han(400), han(100)
	texts := []string{old1, old2, live}
	body := []byte(`{"input":[{"role":"assistant","content":"` + old1 + `"},` +
		`{"role":"user","content":"` + old2 + `"},` +
		`{"role":"user","content":"` + live + `"}]}`)
	g0 := New(Config{APIKey: "test-key", Model: "mimo-v2.5"})
	original := estimateTokensBytes(body) + estimateTokens(ReplyInstruction)
	growth := 0
	for _, text := range texts {
		growth += g0.estimatedGrowth(text, nil)
	}
	if growth <= 0 {
		t.Fatal("test setup: expected the translation to be estimated as growing")
	}
	target := original + growth/2
	g := newBudgetGateway(t, target*100/upstreamBudgetPct)
	plan := g.planCoverage(body, texts, nil)
	if !plan.enabled {
		t.Fatal("the check must be enabled")
	}
	if plan.allowed[0] || plan.allowed[1] {
		t.Fatal("older texts must be dropped before the live turn")
	}
	if !plan.allowed[2] {
		t.Fatal("the newest text must always be translated")
	}
	if plan.skipped != 2 {
		t.Fatalf("skipped=%d, want 2", plan.skipped)
	}
	if plan.estimated > plan.budget {
		t.Fatalf("plan left the request over budget: %d > %d", plan.estimated, plan.budget)
	}
	if original > plan.budget {
		t.Fatalf("test setup: the original body (%d) should fit the budget (%d)", original, plan.budget)
	}
}

func TestPlanCoverageKeepsEverythingWhenThereIsRoom(t *testing.T) {
	g := newBudgetGateway(t, 100000)
	plan := g.planCoverage([]byte(`{}`), []string{han(100), han(100)}, nil)
	if plan.skipped != 0 || !plan.allowed[0] || !plan.allowed[1] {
		t.Fatalf("a roomy window must not change anything: %+v", plan)
	}
}

func TestPlanCoverageLeavesAnAlreadyOversizedRequestAlone(t *testing.T) {
	// The request is over the window before translation; translation is not
	// the cause, so nothing is skipped and the log says so instead.
	g := newBudgetGateway(t, 1000)
	plan := g.planCoverage([]byte(han(5000)), []string{han(300)}, nil)
	if !plan.overBudgetInput {
		t.Fatalf("expected the over-budget-input path: %+v", plan)
	}
	if plan.skipped != 0 || !plan.allowed[0] {
		t.Fatalf("translation must still happen: %+v", plan)
	}
}

func TestPlanCoverageUsesMeasuredGrowthFromTheCache(t *testing.T) {
	g := newBudgetGateway(t, 4000)
	text := han(1000)
	masked, _ := maskHanPaths(text)
	pieces := g.buildPieces(masked)
	if len(pieces) != 1 {
		t.Fatalf("expected one piece, got %d", len(pieces))
	}
	// A cached translation that is much longer than the input: the planner
	// must plan with the real number, not the 25% guess.
	g.cacheSet(g.cacheKey(pieces[0].text), strings.Repeat("x", 12000))
	measured := g.estimatedGrowth(text, nil)
	guessed := estimateTokens(text) * translationExpansionPct / 100
	if measured <= guessed {
		t.Fatalf("measured growth %d should exceed the %d-token guess", measured, guessed)
	}
}

func TestPlanCoverageNeverDropsTheLiveTurn(t *testing.T) {
	// A window so tight that even the live turn does not fit: the older texts
	// still absorb the whole overrun, because translating nothing would send
	// the user's live prompt upstream in Chinese.
	old, live := han(900), han(500)
	body := []byte(`{"input":[{"role":"user","content":"` + old + `"},` +
		`{"role":"user","content":"` + live + `"}]}`)
	g0 := New(Config{APIKey: "test-key", Model: "mimo-v2.5"})
	original := estimateTokensBytes(body) + estimateTokens(ReplyInstruction)
	// A window the live turn alone would blow: the older text is dropped and
	// the loop must stop there instead of eating the live turn too.
	g := newBudgetGateway(t, (original+g0.estimatedGrowth(live, nil)/2)*100/upstreamBudgetPct)
	texts := []string{old, live}
	plan := g.planCoverage(body, texts, nil)
	if plan.estimated <= plan.budget {
		t.Fatalf("test setup: the live turn alone (%d) should still exceed the budget (%d)", plan.estimated, plan.budget)
	}
	if plan.allowed[0] {
		t.Fatal("the older text must be dropped first")
	}
	if !plan.allowed[1] {
		t.Fatal("the live turn must be translated even when the budget says no")
	}
	if plan.skipped != 1 {
		t.Fatalf("skipped=%d, want 1", plan.skipped)
	}
}

func TestPlanCoverageIgnoresTextsWithoutHan(t *testing.T) {
	english, old, live := "plain english, nothing to translate", han(700), han(500)
	body := []byte(`{"input":[{"role":"user","content":"` + english + `"},` +
		`{"role":"user","content":"` + old + `"},` +
		`{"role":"user","content":"` + live + `"}]}`)
	g0 := New(Config{APIKey: "test-key", Model: "mimo-v2.5"})
	original := estimateTokensBytes(body) + estimateTokens(ReplyInstruction)
	growth := g0.estimatedGrowth(old, nil)
	g := newBudgetGateway(t, (original+growth/2)*100/upstreamBudgetPct)
	texts := []string{english, old, live}
	plan := g.planCoverage(body, texts, nil)
	if !plan.allowed[0] {
		t.Fatal("a text with no Han is never 'skipped': nothing would translate it anyway")
	}
	if plan.allowed[1] || !plan.allowed[2] {
		t.Fatalf("ordering wrong: %v", plan.allowed)
	}
}
