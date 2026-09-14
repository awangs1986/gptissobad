package gateway

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/awangs1986/gptissobad/internal/opencodego"
)

const (
	// mimoBaseURL is the Xiaomi token-plan endpoint serving mimo models.
	// The key lives outside git: ~/.codex/mimo.key (0600) or $MIMO_API_KEY.
	mimoBaseURL = "https://token-plan-cn.xiaomimimo.com/v1"
	mimoKeyFile = ".codex/mimo.key"
	mimoKeyEnv  = "MIMO_API_KEY"
)

// ResolveMimoKey returns the Xiaomi translator credential without ever
// logging or persisting it: ~/.codex/mimo.key first, then $MIMO_API_KEY.
// Empty means unconfigured (mimo models fall back to the OpenCode route).
func ResolveMimoKey() string {
	if home, err := os.UserHomeDir(); err == nil {
		if raw, err := os.ReadFile(filepath.Join(home, mimoKeyFile)); err == nil {
			if k := strings.TrimSpace(string(raw)); k != "" {
				return k
			}
		}
	}
	return strings.TrimSpace(os.Getenv(mimoKeyEnv))
}

// translatorTarget picks where a primary translation call goes and with
// whose key. An explicit TranslatorURL override (tests, custom setups)
// always wins with cfg.APIKey. Otherwise mimo* models use the Xiaomi
// direct route when its key is configured; everything else — and mimo
// without its key — stays on the OpenCode chain with cfg.APIKey.
func (g *Gateway) translatorTarget() (endpoint, key string) {
	if u := strings.TrimSpace(g.cfg.TranslatorURL); u != "" {
		return u, g.cfg.APIKey
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(g.cfg.Model)), "mimo") {
		base := strings.TrimSpace(g.cfg.MimoBaseURL)
		if base == "" {
			base = mimoBaseURL
		}
		if k := strings.TrimSpace(g.cfg.MimoAPIKey); k != "" {
			return strings.TrimRight(base, "/") + "/chat/completions", k
		}
		if k := ResolveMimoKey(); k != "" {
			return strings.TrimRight(base, "/") + "/chat/completions", k
		}
	}
	return opencodego.ChatCompletionsURL, g.cfg.APIKey
}
