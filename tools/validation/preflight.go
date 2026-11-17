package validation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/goliatone/switchboard-hub/internal/safeio"
)

const maxConfigBytes = 64 * 1024

func readConfig(path string) (Config, error) {
	var config Config
	if !filepath.IsAbs(path) {
		return config, errors.New("validation configuration requires an absolute path")
	}
	f, err := safeio.OpenPrivate(path, false)
	if err != nil {
		return config, errors.New("validation configuration must be an owned 0600 regular file without aliases")
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil || len(raw) > maxConfigBytes {
		return config, errors.New("validation configuration unreadable or exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		return Config{}, errors.New("validation configuration must contain one JSON object with supported settings")
	}
	return config, config.Validate()
}

// checkLocalSetup examines file metadata only. The live entry point separately
// checks credential presence before opening listeners or preparing the run.
func (c Config) checkLocalSetup() error {
	var issues []error
	if !privateDirectory(filepath.Dir(c.RunDir)) {
		issues = append(issues, errors.New("run_dir needs an existing owned 0700 parent directory"))
	}
	if _, err := os.Lstat(c.RunDir); !errors.Is(err, os.ErrNotExist) {
		issues = append(issues, errors.New("run_dir must be a fresh path"))
	}
	coord := c.Values["coordination_dir"]
	if _, err := os.Lstat(coord); !errors.Is(err, os.ErrNotExist) && !privateDirectory(coord) {
		issues = append(issues, errors.New("existing coordination_dir must be an owned 0700 directory"))
	}
	if c.Provider == "tailscale" {
		issues = append(issues, c.checkLocalTailscale())
	}
	return errors.Join(issues...)
}

func (c Config) checkLocalTailscale() error {
	if c.Values["local_api_transport"] == "macos-gui" {
		if runtime.GOOS != "darwin" {
			return errors.New("macos-gui transport requires macOS")
		}
		return nil
	}
	info, err := os.Stat(c.Values["local_api_socket"])
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return errors.New("local_api_socket must point to an existing Tailscale Unix socket")
	}
	return nil
}

func (c Config) checkCredentialPresence() error {
	if c.Provider == "cloudflare" && strings.TrimSpace(os.Getenv(c.CredentialEnv)) == "" {
		return errors.New("the configured Cloudflare credential environment variable is empty or missing")
	}
	return nil
}

func privateDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(stat.Uid) == int64(os.Geteuid())
}
