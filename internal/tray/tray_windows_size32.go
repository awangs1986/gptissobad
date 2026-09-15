//go:build windows && 386

package tray

import "unsafe"

// 952 bytes on 32-bit Windows (see tray_windows_size64.go).
const nidSize32 = unsafe.Sizeof(notifyIconDataW{})

var (
	_ [nidSize32 - 952]byte
	_ [952 - nidSize32]byte
)
