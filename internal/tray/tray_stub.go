//go:build !linux && !windows

package tray

import "runtime"

// Run has no icon to run outside Linux and Windows. macOS would need the
// Objective-C runtime, which Go can only reach through cgo, and this project
// keeps cgo off so the binaries stay self-contained. Rather than pretend, the
// stub blocks like a working tray does: the watchdog keeps serving the
// Control Page, which is the supported way to watch and control it there.
func Run(onActivate func(), status func() (bool, bool, string), logf func(string)) error {
	if logf != nil {
		logf("tray: no panel icon on " + runtime.GOOS + " yet; use the Control Page")
	}
	select {}
}

func Available() bool { return false }
