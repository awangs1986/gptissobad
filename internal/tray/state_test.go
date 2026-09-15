package tray

import (
	"testing"
	"time"
)

func TestBlinkPhaseHoldWindow(t *testing.T) {
	now := time.Now()
	if blinkPhase(true, time.Time{}, now) {
		t.Fatal("never-active must not blink")
	}
	if !blinkPhase(true, now, now) {
		t.Fatal("fresh activity must blink on the dim phase")
	}
	if blinkPhase(false, now, now) {
		t.Fatal("off phase must show the steady colour")
	}
	if !blinkPhase(true, now.Add(-4*time.Second), now) {
		t.Fatal("hold window must still blink")
	}
	if blinkPhase(true, now.Add(-6*time.Second), now) {
		t.Fatal("expired hold must stop blinking")
	}
}

func TestBlinkDoesNotDependOnHealth(t *testing.T) {
	// The bug this pins: the blink used to require ok, so a red icon (missing
	// key, front door down) never blinked even mid-translation.
	now := time.Now()
	for _, ok := range []bool{false, true} {
		st := panelStateFor(ok, blinkPhase(true, now, now), true)
		if !st.dim {
			t.Fatalf("ok=%v: translating must dim", ok)
		}
		if st.status != "NeedsAttention" {
			t.Fatalf("ok=%v: dim phase must ask for attention, got %q", ok, st.status)
		}
		if st.text != StatusText(ok, true) {
			t.Fatalf("ok=%v: tooltip = %q", ok, st.text)
		}
	}
}

func TestPanelStateIdleIsSteady(t *testing.T) {
	st := panelStateFor(true, blinkPhase(false, time.Time{}, time.Time{}), false)
	if st.dim || st.status != "Active" {
		t.Fatalf("idle state = %+v, want steady Active", st)
	}
	if st.text != StatusText(true, false) {
		t.Fatalf("idle tooltip = %q", st.text)
	}
	if got := StatusText(false, false); got == "" || got == StatusText(true, false) {
		t.Fatalf("fault tooltip must differ from the healthy one: %q", got)
	}
}

func TestPaintSigDistinguishesStates(t *testing.T) {
	seen := map[string]bool{}
	for _, sig := range []string{
		paintSig(true, false, "a"),
		paintSig(true, true, "a"),
		paintSig(false, false, "a"),
		paintSig(true, false, "b"),
	} {
		if seen[sig] {
			t.Fatalf("duplicate signature %q hides a repaint", sig)
		}
		seen[sig] = true
	}
}

func TestIconKeyCoversFourFrames(t *testing.T) {
	got := map[string]bool{}
	for _, combo := range [][2]bool{{true, false}, {true, true}, {false, false}, {false, true}} {
		key := iconKey(combo[0], combo[1])
		if got[key] {
			t.Fatalf("iconKey(%v, %v) collides on %q", combo[0], combo[1], key)
		}
		got[key] = true
	}
	if len(got) != 4 {
		t.Fatalf("want four distinct frames, got %v", got)
	}
	if iconKey(false, true) != "fault-dim" || iconKey(true, true) != "ok-dim" {
		t.Fatalf("dim phases must keep their health colour: %v", got)
	}
}
