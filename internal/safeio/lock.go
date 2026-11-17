package safeio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// CanonicalFile resolves parent aliases, but refuses destination symlinks.
// Lock identity is attached to this path, never to a caller-selected state dir.
func CanonicalFile(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := makeDurableDirs(filepath.Dir(absolute)); err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	result := filepath.Join(parent, filepath.Base(absolute))
	info, err := os.Lstat(result)
	if err == nil && (!info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1) {
		return "", errors.New("store path must be a regular file without aliases")
	}
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return result, nil
}

func PrivateDirectory(path string) (string, error) {
	if err := makeDurableDirs(path); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		return "", errors.New("state directory must be a private 0700 directory, not a symlink")
	}
	// Stat_t from os uses syscall.Stat_t; ownership is checked via the opened fd.
	f, err := OpenDirectory(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	fd, err := fileDescriptor(f)
	if err != nil {
		return "", err
	}
	if err := unix.Fstat(fd, &st); err != nil {
		return "", err
	}
	if int64(st.Uid) != int64(os.Geteuid()) {
		return "", errors.New("state directory belongs to another user")
	}
	return filepath.EvalSymlinks(path)
}

// OpenPrivate refuses symlinks, hard links, devices and permissive existing files.
func OpenPrivate(path string, create bool) (*os.File, error) {
	return openPrivate(path, create, false)
}

func openPrivate(path string, create, allowRoot bool) (*os.File, error) {
	flags := unix.O_RDWR | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if create {
		flags |= unix.O_CREAT
	}
	fd, err := unix.Open(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	if fd < 0 {
		return nil, errors.New("invalid file descriptor")
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = f.Close()
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0o777 != 0o600 || st.Nlink != 1 || (int64(st.Uid) != int64(os.Geteuid()) && (!allowRoot || os.Geteuid() != 0)) {
		_ = f.Close()
		return nil, errors.New("expected an owned private regular file without aliases")
	}
	return f, nil
}

// Lock waits cancellably for an advisory cross-process lock. Keep the lock file;
// deleting it would permit simultaneous locks on different inodes.
func Lock(ctx context.Context, path string) (func(), error) {
	return lockFile(ctx, path, false, -1, -1)
}

// LockConfig preserves legacy sudo-managed config ownership. Root may lock the
// operator-owned file; isolated stores continue to require strict current-user
// ownership via Lock. Chown uses the verified open descriptor, never a pathname.
func LockConfig(ctx context.Context, path string, uid, gid int) (func(), error) {
	return lockFile(ctx, path, true, uid, gid)
}

func lockFile(ctx context.Context, path string, allowRoot bool, uid, gid int) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := openPrivate(path, true, allowRoot)
	if err != nil {
		return nil, err
	}
	fd, err := fileDescriptor(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if uid >= 0 && gid >= 0 {
				if err := f.Chown(uid, gid); err != nil {
					_ = unix.Flock(fd, unix.LOCK_UN)
					_ = f.Close()
					return nil, err
				}
			}
			return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = f.Close()
			return nil, fmt.Errorf("lock store: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Persist each new directory entry before using it as a journal/config parent.
func makeDurableDirs(path string) error {
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil {
		if !info.IsDir() {
			return errors.New("state parent is not a directory")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	if err := makeDurableDirs(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	dir, err := OpenDirectory(parent)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

// OpenDirectory returns a directory handle scoped through os.Root for sync and
// ownership checks; no caller-controlled child traversal is performed.
func OpenDirectory(path string) (*os.File, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.Open(".")
}
