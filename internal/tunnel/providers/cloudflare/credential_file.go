package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/goliatone/switchboard-hub/internal/safeio"
)

// credentialFile is non-secret recovery authority for one private artifact.
// Its caller must durably record it before secret bytes are written. This
// primitive does not establish connector identity or authorize remote cleanup.
type credentialFile struct {
	Path   string
	Device string
	Inode  uint64
}

func createCredentialFile(dir, attempt string, token []byte, record func(credentialFile) error) (credentialFile, error) {
	if attempt == "" || len(token) == 0 || record == nil {
		return credentialFile{}, errors.New("credential attempt, token and durable recorder required")
	}
	canonical, err := safeio.PrivateDirectory(dir)
	if err != nil {
		return credentialFile{}, err
	}
	hash := sha256.Sum256([]byte(attempt))
	path := filepath.Join(canonical, "connector-"+hex.EncodeToString(hash[:])+".token")
	// O_EXCL also refuses an existing symlink; the parent is private and verified.
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return credentialFile{}, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile(filepath.Base(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return credentialFile{}, errors.New("private credential file already exists or is unavailable")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return credentialFile{}, err
	}
	st := info.Sys().(*syscall.Stat_t)
	ref := credentialFile{Path: path, Device: fmt.Sprint(st.Dev), Inode: st.Ino}
	ok := false
	defer func() {
		if !ok {
			_ = ref.remove()
		}
	}()
	// Persist the empty file's directory entry, then its identity, before writing.
	if err = syncCredentialDirectory(canonical); err != nil {
		return ref, err
	}
	if err = record(ref); err != nil {
		return ref, errors.New("credential identity could not be recorded")
	}
	if _, err = f.Write(token); err != nil {
		return ref, errors.New("private credential write failed")
	}
	if err = f.Sync(); err != nil {
		return ref, errors.New("private credential sync failed")
	}
	ok = true
	return ref, nil
}

func (ref credentialFile) verify() error {
	if !filepath.IsAbs(ref.Path) || ref.Inode == 0 {
		return errors.New("credential identity missing")
	}
	if _, err := safeio.PrivateDirectory(filepath.Dir(ref.Path)); err != nil {
		return err
	}
	f, err := safeio.OpenPrivate(ref.Path, false)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	if fmt.Sprint(st.Dev) != ref.Device || st.Ino != ref.Inode {
		return errors.New("credential artifact changed; preserve replacement")
	}
	return nil
}
func (ref credentialFile) remove() error {
	if err := ref.verify(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Remove(ref.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncCredentialDirectory(filepath.Dir(ref.Path))
}
func syncCredentialDirectory(dir string) error {
	f, err := safeio.OpenDirectory(dir)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

// secureConnectorCommand is deliberately separate from legacy processStarter:
// that interface cannot set an environment or supervise credential cleanup.
// The owned lifecycle must supply a supervised lifetime, durable file record,
// and verified exit/recovery before wiring this command into production.
func secureConnectorCommand(ctx context.Context, helper string, ref credentialFile) (*exec.Cmd, error) {
	if !filepath.IsAbs(helper) {
		return nil, errors.New("absolute connector helper required")
	}
	if err := ref.verify(); err != nil {
		return nil, err
	}
	probe := exec.CommandContext(ctx, helper, "tunnel", "run", "--help") // #nosec G204 -- helper is an explicitly selected absolute executable; arguments are fixed.
	probe.Env = connectorEnvironment(filepath.Dir(ref.Path))
	output, err := probe.Output()
	if err != nil || !strings.Contains(string(output), "--token-file") {
		return nil, errors.New("cloudflared token-file support required (2025.4.0+); no argv-token fallback")
	}
	cmd := exec.CommandContext(ctx, helper, "--config", os.DevNull, "--no-autoupdate", "tunnel", "run", "--token-file", ref.Path) // #nosec G204 -- fixed args with a verified private file path, never a token value.
	cmd.Env = connectorEnvironment(filepath.Dir(ref.Path))
	// Output is discarded by default; callers must not surface raw connector logs.
	return cmd, nil
}
func connectorEnvironment(dir string) []string {
	// No inherited API credentials, tokens, proxy overrides, config paths or HOME.
	return []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=" + dir, "TMPDIR=" + dir}
}
