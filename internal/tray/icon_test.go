//go:build linux

package tray

import (
	"testing"
	"time"
)

// samplePoint sits inside the accent square but outside both backdrop cuts
// for the 32px render.
func sampleAccent(size int, ok bool) (uint8, uint8, uint8) {
	w, h, pix := IconPixmapFor(size, ok)
	if int(w) != size || int(h) != size {
		panic("bad size")
	}
	x, y := size*11/16, size*10/16
	off := (y*size + x) * 4
	return pix[off+1], pix[off+2], pix[off+3]
}

func TestStatusIconsDiffer(t *testing.T) {
	gr, gg, gb := sampleAccent(32, true)
	rr, rg, rb := sampleAccent(32, false)
	if gr == rr && gg == rg && gb == rb {
		t.Fatal("green and red variants render identically")
	}
	if gg < 120 || gr > 120 {
		t.Fatalf("healthy icon not green enough: %d %d %d", gr, gg, gb)
	}
	if rr < 150 || rb > 150 {
		t.Fatalf("fault icon not red enough: %d %d %d", rr, rg, rb)
	}
}

func TestDimDiffersFromBoth(t *testing.T) {
	dr, dg, db := sampleAccentDim()
	gr, gg, gb := sampleAccent(32, true)
	rr, rg, rb := sampleAccent(32, false)
	if (dr == gr && dg == gg && db == gb) || (dr == rr && dg == rg && db == rb) {
		t.Fatal("dim phase matches a steady state")
	}
	if dg < 20 || dg > 110 || dr > 60 {
		t.Fatalf("dim not dark green: %d %d %d", dr, dg, db)
	}
}

func sampleAccentDim() (uint8, uint8, uint8) {
	w, h, pix := IconPixmapDim(32)
	if int(w) != 32 || int(h) != 32 {
		panic("bad size")
	}
	x, y := 32*11/16, 32*10/16
	off := (y*32 + x) * 4
	return pix[off+1], pix[off+2], pix[off+3]
}

func TestBlinkHoldWindow(t *testing.T) {
	now := time.Now()
	if blinkDim(false, true, now, now) {
		t.Fatal("fault must stay steady red")
	}
	if blinkDim(true, true, time.Time{}, now) {
		t.Fatal("never-active must not blink")
	}
	if !blinkDim(true, true, now, now) {
		t.Fatal("active translating must show dim on phase")
	}
	if blinkDim(true, false, now, now) {
		t.Fatal("off phase must show steady green")
	}
	if !blinkDim(true, true, now.Add(-4*time.Second), now) {
		t.Fatal("hold window must still blink")
	}
	if blinkDim(true, true, now.Add(-6*time.Second), now) {
		t.Fatal("expired hold must stop blinking")
	}
}

func TestLegacyIconUnchanged(t *testing.T) {
	w1, h1, p1 := IconPixmap(32)
	w2, h2, p2 := IconPixmapFor(32, true)
	if w1 != w2 || h1 != h2 || string(p1) != string(p2) {
		t.Fatal("IconPixmap changed meaning")
	}
}
