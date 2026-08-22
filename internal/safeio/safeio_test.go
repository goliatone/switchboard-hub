package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFileReplacesSymlinkWithoutFollowingIt(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(target, []byte("inside"), 0o600); err != nil {
		t.Fatalf("AtomicWriteFile returned error: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "inside" {
		t.Fatalf("target = %q, %v", got, err)
	}
	outsideData, err := os.ReadFile(outside)
	if err != nil || string(outsideData) != "outside" {
		t.Fatalf("outside = %q, %v", outsideData, err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("target remained a symlink")
	}
}

func TestReadFileRejectsSymlinkEscapingParent(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(link); err == nil {
		t.Fatal("ReadFile followed a symlink outside its parent")
	}
}

func TestAtomicWriteFileAppliesRequestedPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private")
	if err := AtomicWriteFile(path, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %o, want 600", got)
	}
}
