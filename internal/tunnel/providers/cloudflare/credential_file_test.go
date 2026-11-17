package cloudflare

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func privateToken(t *testing.T, dir, attempt string) credentialFile {
	t.Helper()
	ref, err := createCredentialFile(dir, attempt, []byte("dummy-connector-secret"), func(ref credentialFile) error {
		raw, err := os.ReadFile(ref.Path)
		if err != nil {
			return err
		}
		if len(raw) != 0 {
			t.Fatal("secret written before durable identity")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}
func TestCredentialFilePrivateRecoveryAndReplacement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	ref := privateToken(t, dir, "attempt-a")
	info, err := os.Stat(ref.Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: %v %v", info, err)
	}
	parent, err := os.Stat(dir)
	if err != nil || parent.Mode().Perm() != 0o700 {
		t.Fatal("parent permissions", err)
	}
	other := privateToken(t, dir, "attempt-b")
	if _, err = createCredentialFile(dir, "attempt-a", []byte("another"), func(credentialFile) error { return nil }); err == nil {
		t.Fatal("overwrote pending artifact")
	}
	if err = ref.remove(); err != nil {
		t.Fatal(err)
	}
	if err = ref.remove(); err != nil {
		t.Fatal(err)
	}
	if err = other.verify(); err != nil {
		t.Fatal("other artifact damaged", err)
	}
	// Hold the original inode through rename so replacement cannot reuse it.
	if err = os.Rename(other.Path, other.Path+".original"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(other.Path, []byte("operator replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = other.remove(); err == nil {
		t.Fatal("replacement removed")
	}
	raw, err := os.ReadFile(other.Path)
	if err != nil || string(raw) != "operator replacement" {
		t.Fatal("replacement damaged")
	}
}
func TestCredentialFileRecorderFailureAndUnsafePaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	ref, err := createCredentialFile(dir, "attempt", []byte("secret"), func(credentialFile) error { return errors.New("dummy-secret-in-recorder-error") })
	if err == nil || strings.Contains(err.Error(), "dummy-secret") {
		t.Fatal("unsafe recorder failure", err)
	}
	if _, err = os.Stat(ref.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed record left credential")
	}
	if err = os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err = createCredentialFile(dir, "next", []byte("secret"), func(credentialFile) error { return nil }); err == nil {
		t.Fatal("unsafe directory accepted")
	}
	if err = os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ref = privateToken(t, dir, "link")
	if err = os.Remove(ref.Path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside")
	if err = os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, ref.Path); err != nil {
		t.Fatal(err)
	}
	if err = ref.remove(); err == nil {
		t.Fatal("symlink cleanup accepted")
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "untouched" {
		t.Fatal("outside target changed")
	}
}
func TestSecureConnectorCommandUsesPrivateFileAndExplicitEnvironment(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	ref := privateToken(t, dir, "start")
	helper := filepath.Join(t.TempDir(), "fake-cloudflared")
	// The fake helper implements help and then exits; it never contacts a provider.
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nif [ \"$3\" = \"--help\" ]; then echo --token-file; exit 0; fi\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUNNEL_TOKEN", "dummy-env-token")
	t.Setenv("SWITCHD_CF_API_TOKEN", "dummy-api-key")
	t.Setenv("UNRELATED_SECRET", "dummy-other-secret")
	cmd, err := secureConnectorCommand(context.Background(), helper, ref)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(append(append([]string{}, cmd.Args...), cmd.Env...), " ")
	for _, secret := range []string{"dummy-connector-secret", "dummy-env-token", "dummy-api-key", "dummy-other-secret"} {
		if strings.Contains(joined, secret) {
			t.Fatal("credential in command/environment")
		}
	}
	if !strings.Contains(joined, "--token-file "+ref.Path) {
		t.Fatal("private file not used")
	}
	if err = cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err = ref.remove(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(helper, []byte("#!/bin/sh\necho unsupported\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ref = privateToken(t, dir, "unsupported")
	if _, err = secureConnectorCommand(context.Background(), helper, ref); err == nil {
		t.Fatal("unsupported helper accepted")
	}
}
