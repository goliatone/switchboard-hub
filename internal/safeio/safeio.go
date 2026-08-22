package safeio

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ReadFile opens the parent directory as an os.Root so a symlink cannot redirect
// access outside the selected directory.
func ReadFile(path string) ([]byte, error) {
	root, name, err := openParent(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

func Open(path string) (*os.File, error) {
	root, name, err := openParent(path)
	if err != nil {
		return nil, err
	}
	f, openErr := root.Open(name)
	closeErr := root.Close()
	if openErr != nil {
		return nil, openErr
	}
	if closeErr != nil {
		_ = f.Close()
		return nil, closeErr
	}
	return f, nil
}

func OpenFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	root, name, err := openParent(path)
	if err != nil {
		return nil, err
	}
	f, openErr := root.OpenFile(name, flag, perm)
	closeErr := root.Close()
	if openErr != nil {
		return nil, openErr
	}
	if closeErr != nil {
		_ = f.Close()
		return nil, closeErr
	}
	return f, nil
}

func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	return AtomicWriteFileOwned(path, data, perm, -1, -1)
}

// AtomicWriteFileOwned creates and optionally chowns a temporary file through
// an os.Root before renaming it over the destination. This prevents following a
// user-controlled destination symlink while preserving atomic replacement.
func AtomicWriteFileOwned(path string, data []byte, perm os.FileMode, uid, gid int) error {
	root, name, err := openParent(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	tmpName, err := temporaryName(name)
	if err != nil {
		return err
	}
	f, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	cleanup := func() {
		_ = f.Close()
		_ = root.Remove(tmpName)
	}
	if _, err := f.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		cleanup()
		return err
	}
	if uid >= 0 && gid >= 0 {
		if err := f.Chown(uid, gid); err != nil {
			cleanup()
			return err
		}
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmpName)
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		_ = root.Remove(tmpName)
		return err
	}
	dirFD, err := fileDescriptor(dir)
	if err != nil {
		_ = dir.Close()
		_ = root.Remove(tmpName)
		return err
	}
	if err := unix.Renameat(dirFD, tmpName, dirFD, name); err != nil {
		_ = dir.Close()
		_ = root.Remove(tmpName)
		return err
	}
	if err := dir.Close(); err != nil {
		return err
	}
	return nil
}

func fileDescriptor(f *os.File) (int, error) {
	fd := f.Fd()
	maxInt := int(^uint(0) >> 1)
	if fd > uintptr(maxInt) {
		return 0, fmt.Errorf("file descriptor %d exceeds int range", fd)
	}
	return int(fd), nil // #nosec G115 -- the range is checked before converting for renameat.
}

func openParent(path string) (*os.Root, string, error) {
	clean := filepath.Clean(path)
	name := filepath.Base(clean)
	if name == "." || name == string(filepath.Separator) {
		return nil, "", fmt.Errorf("invalid file path %q", path)
	}
	root, err := os.OpenRoot(filepath.Dir(clean))
	if err != nil {
		return nil, "", err
	}
	return root, name, nil
}

func temporaryName(name string) (string, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "." + name + ".switchd-" + hex.EncodeToString(random), nil
}
