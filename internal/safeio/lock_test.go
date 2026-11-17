package safeio

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigLockOwnershipUsesVerifiedDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.lock")
	unlock, err := LockConfig(context.Background(), path, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	// Normal library calls can reacquire the same private lock after ownership
	// is applied, while symbolic links cannot redirect the chown/open operation.
	unlock, err = Lock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if unlock, err := LockConfig(context.Background(), link, os.Geteuid(), os.Getegid()); err == nil {
		unlock()
		t.Fatal("followed lock symlink")
	}
}
