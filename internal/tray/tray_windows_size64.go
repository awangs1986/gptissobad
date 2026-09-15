//go:build windows && (amd64 || arm64)

package tray

import "unsafe"

// NOTIFYICONDATAW is 976 bytes on 64-bit Windows; Shell_NotifyIconW validates
// cbSize, so a layout drift must fail the build rather than the tray at
// runtime.
const nidSize64 = unsafe.Sizeof(notifyIconDataW{})

var (
	_ [nidSize64 - 976]byte
	_ [976 - nidSize64]byte
)
