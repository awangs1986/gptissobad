package fronthost

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const pidFileName = "fronthost.pid"

// PIDPath is ~/.codex/fronthost.pid (or codexDir/fronthost.pid in tests).
// The standalone binary writes it so Control Page and SessionStart can
// stop a leftover front without killing the watchdog.
func PIDPath(codexDir string) string {
	if strings.TrimSpace(codexDir) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return pidFileName
		}
		codexDir = filepath.Join(home, ".codex")
	}
	return filepath.Join(codexDir, pidFileName)
}

// WritePID records the current process so a later stop can signal it.
func WritePID(codexDir string) error {
	return os.WriteFile(PIDPath(codexDir), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
}

// RemovePID deletes the pid file. Safe if it is already gone.
func RemovePID(codexDir string) {
	_ = os.Remove(PIDPath(codexDir))
}
