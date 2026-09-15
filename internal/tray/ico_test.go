package tray

import (
	"bytes"
	"testing"
)

func u32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func TestICOFileLayout(t *testing.T) {
	const size = 32
	ico := icoForState(size, true, false)
	if len(ico) == 0 {
		t.Fatal("no icon bytes")
	}
	want := 22 + 40 + size*size*4 + ((size+31)/32*4)*size
	if len(ico) != want {
		t.Fatalf("ico size = %d, want %d", len(ico), want)
	}
	if ico[0] != 0 || ico[1] != 0 || ico[2] != 1 || ico[3] != 0 || ico[4] != 1 || ico[5] != 0 {
		t.Fatalf("bad ICONDIR: % x", ico[:6])
	}
	if ico[6] != size || ico[7] != size {
		t.Fatalf("bad ICONDIRENTRY dimensions: % x", ico[6:8])
	}
	if ico[10] != 1 || ico[11] != 0 || ico[12] != 32 || ico[13] != 0 {
		t.Fatalf("bad ICONDIRENTRY planes/bitcount: % x", ico[10:14])
	}
	if got := u32(ico[14:]); int(got) != len(ico)-22 {
		t.Fatalf("bytesInRes = %d, want %d", got, len(ico)-22)
	}
	if got := u32(ico[18:]); got != 22 {
		t.Fatalf("image offset = %d, want 22", got)
	}
	if got := u32(ico[22:]); got != 40 {
		t.Fatalf("biSize = %d, want 40", got)
	}
	if got := u32(ico[26:]); got != size {
		t.Fatalf("biWidth = %d", got)
	}
	if got := u32(ico[30:]); got != size*2 {
		t.Fatalf("biHeight = %d, want %d (XOR + AND)", got, size*2)
	}
	if ico[22+12] != 1 || ico[22+13] != 0 {
		t.Fatalf("biPlanes = % x", ico[22+12:22+14])
	}
	if ico[22+14] != 32 || ico[22+15] != 0 {
		t.Fatalf("biBitCount = % x", ico[22+14:22+16])
	}
}

func TestICOFileIsBottomUpBGRA(t *testing.T) {
	// 2x2 image: top row red/yellow, bottom row blue/white. Distinguishes the
	// channel order (ARGB -> BGRA) and the vertical flip in one check.
	argb := []byte{
		255, 255, 0, 0, 255, 255, 255, 0, // top: red, yellow
		255, 0, 0, 255, 255, 255, 255, 255, // bottom: blue, white
	}
	ico := icoFile(2, 2, argb)
	const start = 22 + 40
	if got := ico[start : start+4]; !bytes.Equal(got, []byte{255, 0, 0, 255}) {
		t.Fatalf("first pixel = % x, want blue in BGRA (bottom row first)", got)
	}
	if got := ico[start+4 : start+8]; !bytes.Equal(got, []byte{255, 255, 255, 255}) {
		t.Fatalf("second pixel = % x, want white in BGRA", got)
	}
	if got := ico[start+8 : start+12]; !bytes.Equal(got, []byte{0, 0, 255, 255}) {
		t.Fatalf("third pixel = % x, want red in BGRA", got)
	}
}

func TestICOFileRejectsShortInput(t *testing.T) {
	if icoFile(4, 4, make([]byte, 16)) != nil {
		t.Fatal("short pixel buffer must be rejected, not read out of bounds")
	}
	if icoFile(0, 4, make([]byte, 64)) != nil {
		t.Fatal("zero width must be rejected")
	}
}

func TestCopyUTF16TruncatesBetweenRunes(t *testing.T) {
	var full [5]uint16
	copyUTF16(full[:], "ab\U0001F600")
	if full[0] != 'a' || full[1] != 'b' || full[2] != 0xD83D || full[3] != 0xDE00 || full[4] != 0 {
		t.Fatalf("full copy = %#v", full)
	}
	var small [3]uint16
	copyUTF16(small[:], "ab\U0001F600")
	if small != [3]uint16{'a', 'b', 0} {
		t.Fatalf("truncated copy = %#v (must not split a surrogate pair)", small)
	}
	var reuse [4]uint16
	for i := range reuse {
		reuse[i] = 'x'
	}
	copyUTF16(reuse[:], "")
	if reuse != [4]uint16{} {
		t.Fatalf("empty string must zero the field: %#v", reuse)
	}
}
