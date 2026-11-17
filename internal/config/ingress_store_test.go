package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/internal/safeio"
	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

func storeScope(dir string) ingress.MutationScope {
	return ingress.MutationScope{ConfigPath: filepath.Join(dir, "config.yaml"), StateDir: filepath.Join(dir, "state"), Operation: "ensure", Name: "a", Owner: ingress.Owner{System: "test", ScopeID: "a"}}
}
func storeProgress(scope ingress.MutationScope, id string) ingress.Progress {
	return ingress.Progress{Phase: "pending", Resource: ingress.Resource{OperationID: id, Name: scope.Name, Owner: scope.Owner, Generation: "generation"}}
}
func TestIngressStoreLockCancellationAndRelease(t *testing.T) {
	dir := t.TempDir()
	scope := storeScope(dir)
	path, err := safeio.CanonicalFile(scope.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := safeio.Lock(context.Background(), path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	s := &IngressStore{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	calls := 0
	err = s.WithIngressMutation(ctx, scope, func(context.Context, ingress.Recorder) error { calls++; return nil })
	unlock()
	if !errors.Is(err, context.DeadlineExceeded) || calls != 0 {
		t.Fatalf("cancel=%v calls=%d", err, calls)
	}
	if err := s.WithIngressMutation(context.Background(), scope, func(context.Context, ingress.Recorder) error { return nil }); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("callback failure")
	if err := s.WithIngressMutation(context.Background(), scope, func(context.Context, ingress.Recorder) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err := s.WithIngressMutation(context.Background(), scope, func(context.Context, ingress.Recorder) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestIngressStoreDurableProgressAndExternalEdits(t *testing.T) {
	dir := t.TempDir()
	scope := storeScope(dir)
	if err := Save(scope.ConfigPath, Default("test", "127.0.0.1")); err != nil {
		t.Fatal(err)
	}
	s := &IngressStore{}
	var id string
	err := s.WithIngressMutation(context.Background(), scope, func(ctx context.Context, rec ingress.Recorder) error {
		id = rec.OperationID()
		tx := Transaction(ctx)
		cfg, err := tx.Load(scope.ConfigPath)
		if err != nil {
			return err
		}
		if err := rec.Record(ctx, storeProgress(scope, id)); err != nil {
			return err
		}
		// A noncooperating writer is detected and its edit must survive.
		raw, err := os.ReadFile(scope.ConfigPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(scope.ConfigPath, append(raw, []byte("# operator edit\n")...), 0o600); err != nil {
			return err
		}
		return tx.Save(scope.ConfigPath, cfg)
	})
	if !errors.Is(err, ErrStoreChanged) {
		t.Fatal(err)
	}
	data, err := os.ReadFile(scope.ConfigPath)
	if err != nil || !strings.Contains(string(data), "operator edit") {
		t.Fatal("external edit overwritten", err)
	}
	s = &IngressStore{}
	err = s.WithIngressMutation(context.Background(), scope, func(ctx context.Context, rec ingress.Recorder) error {
		p, pending, err := rec.PendingProgress(ctx)
		if err != nil {
			return err
		}
		if !pending || rec.OperationID() != id || p.Resource.OperationID != id {
			t.Fatal("lost retry authority")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(scope.StateDir, "*.json"))
	if err != nil || len(files) != 0 {
		t.Fatal("completed journal retained", files, err)
	}
	for _, name := range []string{scope.ConfigPath, scope.ConfigPath + ".lock", scope.ConfigPath + ".ingress"} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("permissions %s: %v %v", name, info, err)
		}
	}
}

func TestIngressStoreRefusesAliasesAndConflictingState(t *testing.T) {
	for _, kind := range []string{"config-symlink", "config-hardlink", "state-symlink", "state-mode", "lock-symlink", "marker-symlink", "different-state"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			scope := storeScope(dir)
			s := &IngressStore{}
			other := filepath.Join(dir, "other")
			if err := os.WriteFile(other, []byte("do not change"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "config-symlink":
				if err := os.Symlink(other, scope.ConfigPath); err != nil {
					t.Fatal(err)
				}
			case "config-hardlink":
				if err := os.Link(other, scope.ConfigPath); err != nil {
					t.Fatal(err)
				}
			case "state-symlink":
				if err := os.Symlink(t.TempDir(), scope.StateDir); err != nil {
					t.Fatal(err)
				}
			case "state-mode":
				if err := os.Mkdir(scope.StateDir, 0o755); err != nil {
					t.Fatal(err)
				}
			case "lock-symlink":
				if err := os.Symlink(other, scope.ConfigPath+".lock"); err != nil {
					t.Fatal(err)
				}
			case "marker-symlink":
				if err := os.Symlink(other, scope.ConfigPath+".ingress"); err != nil {
					t.Fatal(err)
				}
			case "different-state":
				if err := s.WithIngressMutation(context.Background(), scope, func(context.Context, ingress.Recorder) error { return nil }); err != nil {
					t.Fatal(err)
				}
				scope.StateDir = filepath.Join(dir, "new-state")
			}
			called := false
			err := s.WithIngressMutation(context.Background(), scope, func(context.Context, ingress.Recorder) error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("unsafe path accepted: %v %v", err, called)
			}
			raw, _ := os.ReadFile(other)
			if string(raw) != "do not change" {
				t.Fatal("unrelated file changed")
			}
		})
	}
}

func TestIngressStoreCorruptAndUnownedJournal(t *testing.T) {
	for _, bad := range []string{"{", "{}"} {
		t.Run(bad, func(t *testing.T) {
			scope := storeScope(t.TempDir())
			s := &IngressStore{}
			_ = s.WithIngressMutation(context.Background(), scope, func(ctx context.Context, r ingress.Recorder) error {
				if err := r.Record(ctx, storeProgress(scope, r.OperationID())); err != nil {
					return err
				}
				return errors.New("keep journal")
			})
			paths, _ := filepath.Glob(filepath.Join(scope.StateDir, "*.json"))
			if len(paths) != 1 {
				t.Fatal(paths)
			}
			if err := os.WriteFile(paths[0], []byte(bad), 0o600); err != nil {
				t.Fatal(err)
			}
			called := false
			err := s.WithIngressMutation(context.Background(), scope, func(context.Context, ingress.Recorder) error { called = true; return nil })
			if err == nil || called {
				t.Fatal("invalid journal accepted")
			}
		})
	}
}
