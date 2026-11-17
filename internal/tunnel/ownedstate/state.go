// Package ownedstate stores provider recovery evidence, never credential values.
package ownedstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/goliatone/switchboard-hub/internal/safeio"
	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

func Path(dir string, r ingress.Resource) string {
	sum := sha256.Sum256([]byte(r.Provider + "\x00" + r.Generation))
	return filepath.Join(dir, "owned-"+hex.EncodeToString(sum[:])+".json")
}
func Load(dir string, r ingress.Resource) (ingress.Resource, bool, error) {
	f, err := safeio.OpenPrivate(Path(dir, r), false)
	if errors.Is(err, os.ErrNotExist) {
		return ingress.Resource{}, false, nil
	}
	if err != nil {
		return ingress.Resource{}, false, err
	}
	defer func() { _ = f.Close() }()
	var saved ingress.Resource
	err = json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&saved)
	if err != nil {
		return saved, false, errors.New("invalid provider recovery evidence")
	}
	if saved.Name != r.Name || saved.Owner != r.Owner || saved.Generation != r.Generation || saved.Provider != r.Provider {
		return saved, false, errors.New("provider ownership conflict")
	}
	return saved, true, nil
}
func Save(dir string, r ingress.Resource) error {
	if r.Generation == "" || r.Owner.System == "" || r.Owner.ScopeID == "" {
		return errors.New("provider ownership required")
	}
	if _, err := safeio.PrivateDirectory(dir); err != nil {
		return err
	}
	if _, _, err := Load(dir, r); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return safeio.AtomicWriteFile(Path(dir, r), raw, 0o600)
}
