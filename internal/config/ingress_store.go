package config

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/goliatone/switchboard-hub/internal/safeio"
	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

var ErrIsolatedStore = errors.New("store is reserved for coordinated isolated ingress; use its configured isolated client")
var ErrStoreChanged = errors.New("config changed outside the coordinated operation; retained progress requires reconciliation")

// Enrolment is permanent, including after release of the last endpoint. This
// prevents legacy read/modify/provider/save paths from bypassing coordination.
func rejectIsolatedStore(path string) error {
	_, err := os.Lstat(path + ".ingress")
	if err == nil {
		return ErrIsolatedStore
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

type transactionKey struct{}

// IngressStore supplies opt-in coordinated storage. A transaction-specific
// handle travels in context; never store a mutable current transaction on the
// shared client (concurrent callers may use it).
type IngressStore struct{}

type IngressTransaction struct {
	path string
	hash [32]byte
}

func Transaction(ctx context.Context) *IngressTransaction {
	tx, _ := ctx.Value(transactionKey{}).(*IngressTransaction)
	return tx
}

type journal struct {
	Version  int
	Scope    ingress.MutationScope
	ID       string
	Progress *ingress.Progress
}

type diskRecorder struct {
	path string
	data journal
}

func (s *IngressStore) WithIngressMutation(ctx context.Context, scope ingress.MutationScope, fn func(context.Context, ingress.Recorder) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := safeio.CanonicalFile(scope.ConfigPath)
	if err != nil {
		return err
	}
	unlock, err := safeio.Lock(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := safeio.PrivateDirectory(scope.StateDir)
	if err != nil {
		return err
	}
	if err := enrollStore(path, state); err != nil {
		return err
	}
	scope.ConfigPath, scope.StateDir = path, state
	hash, err := fileHash(path)
	if err != nil {
		return err
	}
	tx := &IngressTransaction{path: path, hash: hash}
	key := sha256.Sum256([]byte(path + "\x00" + scope.Name))
	rec, err := openRecorder(filepath.Join(state, hex.EncodeToString(key[:])+".json"), scope)
	if err != nil {
		return err
	}
	if err := fn(context.WithValue(ctx, transactionKey{}, tx), rec); err != nil {
		return err
	}
	// Removal is not atomic with config commit. Recovery recognizes an already
	// persisted operation by its generation/operation identity before any effects.
	if rec.data.Progress == nil {
		return nil
	}
	if err := os.Remove(rec.path); err != nil {
		return err
	}
	return syncDir(state)
}

func enrollStore(path, state string) error {
	marker := path + ".ingress"
	raw, err := readPrivate(marker)
	if err == nil {
		if string(raw) != state {
			return errors.New("isolated store state directory conflicts with its durable enrollment")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return safeio.AtomicWriteFile(marker, []byte(state), 0o600)
}

func openRecorder(path string, scope ingress.MutationScope) (*diskRecorder, error) {
	rec := &diskRecorder{path: path}
	raw, err := readPrivate(path)
	if err == nil {
		if err := json.Unmarshal(raw, &rec.data); err != nil {
			return nil, errors.New("invalid ingress journal; explicit repair required")
		}
		old := rec.data.Scope
		if rec.data.Version != 1 || rec.data.ID == "" || old.ConfigPath != scope.ConfigPath || old.StateDir != scope.StateDir || old.Name != scope.Name || old.Owner != scope.Owner || old.Operation != scope.Operation || !reflect.DeepEqual(old.ExpectedRevision, scope.ExpectedRevision) {
			return nil, errors.New("pending ingress operation conflicts with this request; retry its original owner, operation and revision")
		}
		return rec, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	rec.data = journal{Version: 1, Scope: scope, ID: hex.EncodeToString(id)}
	return rec, nil
}

func (r *diskRecorder) OperationID() string { return r.data.ID }
func (r *diskRecorder) PendingProgress(ctx context.Context) (ingress.Progress, bool, error) {
	if err := ctx.Err(); err != nil {
		return ingress.Progress{}, false, err
	}
	if r.data.Progress == nil {
		return ingress.Progress{}, false, nil
	}
	raw, err := json.Marshal(r.data.Progress)
	if err != nil {
		return ingress.Progress{}, false, err
	}
	var p ingress.Progress
	err = json.Unmarshal(raw, &p)
	return p, true, err
}
func (r *diskRecorder) Record(ctx context.Context, p ingress.Progress) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.Resource.OperationID != r.data.ID || p.Resource.Name != r.data.Scope.Name || p.Resource.Owner != r.data.Scope.Owner {
		return errors.New("journal ownership conflict")
	}
	if p.Phase != "pending" && p.Phase != "confirmed" && p.Phase != "uncertain" && p.Phase != "persisting" {
		return errors.New("invalid journal phase")
	}
	next := r.data
	next.Progress = &p
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return errors.New("ingress journal exceeds size limit")
	}
	if err := safeio.AtomicWriteFile(r.path, raw, 0o600); err != nil {
		return err
	}
	r.data = next
	return nil
}

func readPrivate(path string) ([]byte, error) {
	f, err := safeio.OpenPrivate(path, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err == nil && len(raw) > 1<<20 {
		err = errors.New("private state exceeds size limit")
	}
	return raw, err
}
func fileHash(path string) ([32]byte, error) {
	raw, err := safeio.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return sha256.Sum256([]byte{0}), nil
	}
	return sha256.Sum256(append([]byte{1}, raw...)), err
}
func syncDir(path string) error {
	f, err := safeio.OpenDirectory(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

func (t *IngressTransaction) Load(path string) (*Config, error) {
	canonical, err := safeio.CanonicalFile(path)
	if err != nil {
		return nil, err
	}
	if canonical != t.path {
		return nil, errors.New("transaction path mismatch")
	}
	hash, err := fileHash(t.path)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(hash[:], t.hash[:]) {
		return nil, ErrStoreChanged
	}
	return loadUnchecked(t.path)
}
func (t *IngressTransaction) LoadOrDefault(path string) (*Config, error) {
	cfg, err := t.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default("test", "10.0.0.1"), nil
	}
	return cfg, err
}
func (t *IngressTransaction) LoadOrCreateDefault(path string) (*Config, error) {
	return t.LoadOrDefault(path)
}
func (t *IngressTransaction) Save(path string, cfg *Config) error {
	canonical, err := safeio.CanonicalFile(path)
	if err != nil {
		return err
	}
	if canonical != t.path {
		return errors.New("transaction path mismatch")
	}
	hash, err := fileHash(t.path)
	if err != nil {
		return err
	}
	if hash != t.hash {
		return ErrStoreChanged
	}
	if err := saveUnchecked(t.path, cfg); err != nil {
		return err
	}
	t.hash, err = fileHash(t.path)
	return err
}

// Read-only loads still take the same lock. No legacy config loader may read an
// enrolled store and subsequently perform uncoordinated provider side effects.
func (s *IngressStore) Load(path string) (*Config, error) {
	canonical, err := safeio.CanonicalFile(path)
	if err != nil {
		return nil, err
	}
	unlock, err := safeio.Lock(context.Background(), canonical+".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	return loadUnchecked(canonical)
}
func (s *IngressStore) LoadOrDefault(path string) (*Config, error) {
	cfg, err := s.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default("test", "10.0.0.1"), nil
	}
	return cfg, err
}
func (s *IngressStore) LoadOrCreateDefault(string) (*Config, error) { return nil, ErrIsolatedStore }
func (s *IngressStore) Save(string, *Config) error {
	return fmt.Errorf("%w: no transaction handle", ErrIsolatedStore)
}
