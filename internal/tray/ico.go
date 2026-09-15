package tray

import "unicode/utf16"

// This file holds the pure-Go halves of the Windows tray: the .ico encoder and
// the fixed-buffer UTF-16 writer. Both are platform independent on purpose, so
// they can be unit-tested on any host even though only Windows uses them.

// icoForState renders one tray frame as a Windows .ico image.
func icoForState(size int, ok, dim bool) []byte {
	var w, h int32
	var argb []byte
	if dim {
		w, h, argb = IconPixmapDimFor(size, ok)
	} else {
		w, h, argb = IconPixmapFor(size, ok)
	}
	return icoFile(w, h, argb)
}

// icoFile wraps ARGB32 pixels (row-major, top row first) in a single-image ICO
// container: ICONDIR + ICONDIRENTRY + BITMAPINFOHEADER + bottom-up BGRA rows +
// a 1bpp AND mask. The mask is all zero because a 32bpp icon carries its own
// alpha channel.
func icoFile(w, h int32, argb []byte) []byte {
	width, height := int(w), int(h)
	if width <= 0 || height <= 0 || len(argb) < width*height*4 {
		return nil
	}
	xorStride := width * 4
	xor := make([]byte, 0, xorStride*height)
	for y := height - 1; y >= 0; y-- {
		row := argb[y*xorStride : (y+1)*xorStride]
		for x := 0; x < width; x++ {
			px := row[x*4 : x*4+4]
			// ARGB32 (a, r, g, b) -> BGRA
			xor = append(xor, px[3], px[2], px[1], px[0])
		}
	}
	maskStride := ((width + 31) / 32) * 4
	andMask := make([]byte, maskStride*height)

	image := make([]byte, 0, 40+len(xor)+len(andMask))
	image = append(image, le32(40)...)               // biSize
	image = append(image, le32(uint32(width))...)    // biWidth
	image = append(image, le32(uint32(height*2))...) // biHeight: XOR + AND
	image = append(image, 1, 0)                      // biPlanes (WORD)
	image = append(image, 32, 0)                     // biBitCount (WORD)
	image = append(image, le32(0)...)                // biCompression: BI_RGB
	image = append(image, le32(uint32(len(xor)))...) // biSizeImage
	image = append(image, le32(0)...)                // biXPelsPerMeter
	image = append(image, le32(0)...)                // biYPelsPerMeter
	image = append(image, le32(0)...)                // biClrUsed
	image = append(image, le32(0)...)                // biClrImportant
	image = append(image, xor...)
	image = append(image, andMask...)

	out := make([]byte, 0, 22+len(image))
	out = append(out, 0, 0, 1, 0, 1, 0)                                     // reserved, type=1, count=1
	out = append(out, byte(width%256), byte(height%256), 0, 0, 1, 0, 32, 0) // dims, colours, planes, bpp
	out = append(out, le32(uint32(len(image)))...)                          // bytes in resource
	out = append(out, le32(22)...)                                          // image offset
	return append(out, image...)
}

func le32(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}

// copyUTF16 writes s into a fixed-size NUL-terminated UTF-16 buffer,
// truncating between runes (never inside a surrogate pair) and zeroing the
// rest. Used for the Win32 struct string fields, which are plain arrays.
func copyUTF16(dst []uint16, s string) {
	for i := range dst {
		dst[i] = 0
	}
	if len(dst) == 0 {
		return
	}
	units := utf16.Encode([]rune(s))
	if n := len(dst) - 1; len(units) > n {
		units = units[:n]
	}
	copy(dst, units)
}
