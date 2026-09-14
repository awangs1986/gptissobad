package opencodego_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/awangs1986/gptissobad/internal/opencodego"
)

func TestResolveKeyUsesLocalThenPiThenEnvironment(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "opencode-go.key")
	pi := filepath.Join(dir, "auth.json")
	t.Setenv(opencodego.EnvAPIKey, "environment-key")

	if err := os.WriteFile(pi, []byte(`{"opencode-go":{"type":"api_key","key":"pi-key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	key, source := opencodego.ResolveKey(local, pi)
	if key != "pi-key" || source != opencodego.KeySourcePi {
		t.Fatalf("Pi credential = %q/%q", key, source)
	}

	if err := os.WriteFile(local, []byte("local-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, source = opencodego.ResolveKey(local, pi)
	if key != "local-key" || source != opencodego.KeySourceLocal {
		t.Fatalf("local credential = %q/%q", key, source)
	}

	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(pi); err != nil {
		t.Fatal(err)
	}
	key, source = opencodego.ResolveKey(local, pi)
	if key != "environment-key" || source != opencodego.KeySourceEnv {
		t.Fatalf("environment credential = %q/%q", key, source)
	}
}

func TestResolveKeyDoesNotTreatAnotherPiProviderAsOpenCodeGo(t *testing.T) {
	dir := t.TempDir()
	pi := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(pi, []byte(`{"openai":{"type":"api_key","key":"wrong-key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(opencodego.EnvAPIKey, "")

	key, source := opencodego.ResolveKey("", pi)
	if key != "" || source != opencodego.KeySourceNone {
		t.Fatalf("unrelated provider resolved as %q/%q", key, source)
	}
}
