package tray

import "testing"

// sampleAccent sits inside the accent square but outside both backdrop cuts
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

func sampleAccentDim(ok bool) (uint8, uint8, uint8) {
	w, h, pix := IconPixmapDimFor(32, ok)
	if int(w) != 32 || int(h) != 32 {
		panic("bad size")
	}
	x, y := 32*11/16, 32*10/16
	off := (y*32 + x) * 4
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
	dr, dg, db := sampleAccentDim(true)
	gr, gg, gb := sampleAccent(32, true)
	rr, rg, rb := sampleAccent(32, false)
	if (dr == gr && dg == gg && db == gb) || (dr == rr && dg == rg && db == rb) {
		t.Fatal("dim phase matches a steady state")
	}
	if dg < 20 || dg > 110 || dr > 60 {
		t.Fatalf("dim not dark green: %d %d %d", dr, dg, db)
	}
}

func TestDimFaultKeepsTheFaultHue(t *testing.T) {
	// A red icon must blink red-dim, not green: the blink is decoupled from
	// health, but the colour still has to tell the truth.
	dr, dg, db := sampleAccentDim(false)
	fr, fg, fb := sampleAccent(32, false)
	if dr > 150 || dg > 80 || db > 80 {
		t.Fatalf("dim fault not dark red: %d %d %d", dr, dg, db)
	}
	if dr == fr && dg == fg && db == fb {
		t.Fatal("dim fault equals the steady fault colour")
	}
	_, gg, _ := sampleAccent(32, true)
	if dg > gg {
		t.Fatalf("dim fault looks greener (%d) than healthy green (%d)", dg, gg)
	}
}

func TestLegacyIconUnchanged(t *testing.T) {
	w1, h1, p1 := IconPixmap(32)
	w2, h2, p2 := IconPixmapFor(32, true)
	if w1 != w2 || h1 != h2 || string(p1) != string(p2) {
		t.Fatal("IconPixmap changed meaning")
	}
	// IconPixmapDim keeps rendering the healthy dim phase.
	w3, h3, p3 := IconPixmapDim(32)
	w4, h4, p4 := IconPixmapDimFor(32, true)
	if w3 != w4 || h3 != h4 || string(p3) != string(p4) {
		t.Fatal("IconPixmapDim changed meaning")
	}
}
