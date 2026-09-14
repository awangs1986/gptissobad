package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func (g *Gateway) loadCache() {
	path := strings.TrimSpace(g.cfg.CacheFile)
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var stored map[string]string
	if json.Unmarshal(raw, &stored) != nil || stored == nil {
		g.logf("translate cache unreadable, starting empty")
		return
	}
	g.cacheMu.Lock()
	defer g.cacheMu.Unlock()
	for k, v := range stored {
		if k == "" || v == "" {
			continue
		}
		g.cache[k] = v
	}
}

func (g *Gateway) persistCache(path string, snapshot map[string]string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		g.logf("translate cache encode failed")
		return
	}
	if err := writeFileAtomic(path, append(raw, '\n')); err != nil {
		g.logf("translate cache write failed")
	}
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "translate-cache-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	_ = tmp.Chmod(0o600)
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	// Rename replaces an existing file on every platform we target, so a
	// failure here means the old cache is still the best copy we have.
	// Keep it and drop the temp rather than clearing the way for a retry.
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
