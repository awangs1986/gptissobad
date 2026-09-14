// Package opencodego contains the fixed OpenCode Go provider contract and
// credential lookup used by both watchdog and the headless gateway.
package opencodego

import (
	"encoding/json"
	"os"
	"strings"
)

const (
	ProviderID         = "opencode-go"
	BaseURL            = "https://opencode.ai/zen/go/v1"
	ChatCompletionsURL = BaseURL + "/chat/completions"
	// ResponsesURL serves the Responses-dialect models (e.g. the
	// muse-spark contributors). Same auth, different body shape.
	ResponsesURL = BaseURL + "/responses"
	EnvAPIKey    = "OPENCODE_API_KEY"
)

type KeySource string

const (
	KeySourceNone  KeySource = ""
	KeySourceLocal KeySource = "codex-file"
	KeySourcePi    KeySource = "pi-login"
	KeySourceEnv   KeySource = "environment"
)

// ResolveKey follows Pi's stored-credential-before-environment rule. The
// codex-lang credential is most specific, followed by Pi's opencode-go login,
// then OPENCODE_API_KEY. It never copies or modifies Pi's credential store.
func ResolveKey(localFile, piAuthFile string) (string, KeySource) {
	if key := readKeyFile(localFile); key != "" {
		return key, KeySourceLocal
	}
	if key := readPiKey(piAuthFile); key != "" {
		return key, KeySourcePi
	}
	if key := strings.TrimSpace(os.Getenv(EnvAPIKey)); key != "" {
		return key, KeySourceEnv
	}
	return "", KeySourceNone
}

func readKeyFile(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func readPiKey(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var auth map[string]struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if json.Unmarshal(raw, &auth) != nil {
		return ""
	}
	credential := auth[ProviderID]
	if credential.Type != "" && credential.Type != "api_key" {
		return ""
	}
	return strings.TrimSpace(credential.Key)
}
