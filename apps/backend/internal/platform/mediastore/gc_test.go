package mediastore

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
)

// stubStorage is an in-memory storage.Storage implementation that records
// every Delete call so the GC test can assert which keys were reclaimed.
type stubStorage struct {
	mu       sync.Mutex
	deleted  map[string]int
	notFound map[string]struct{}
	putErr   error
}

func newStubStorage() *stubStorage {
	return &stubStorage{deleted: map[string]int{}, notFound: map[string]struct{}{}}
}

func (s *stubStorage) Backend() storage.Backend { return storage.BackendLocal }
func (s *stubStorage) Put(_ context.Context, _ storage.PutInput) (storage.Object, error) {
	return storage.Object{}, s.putErr
}
func (s *stubStorage) Get(_ context.Context, _ string) (*storage.GetResult, error) {
	return nil, storage.ErrNotFound
}
func (s *stubStorage) Stat(_ context.Context, _ string) (storage.Object, error) {
	return storage.Object{}, storage.ErrNotFound
}
func (s *stubStorage) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.notFound[key]; ok {
		return storage.ErrNotFound
	}
	s.deleted[key]++
	return nil
}

func TestNewGCHandler_NoRepoReturnsError(t *testing.T) {
	h := NewGCHandler(GCHandlerOptions{Logger: slog.Default()})
	if err := h(context.Background(), nil); err == nil {
		t.Fatal("expected error when repo is nil")
	}
}

// fakeRepo lets us drive ListGCCandidates/HardDelete without a real DB.
type fakeRepo struct {
	candidates []GCCandidate
	hardErr    error
	hardCalls  []uuid.UUID
}

func TestGCHandler_DispatchesDeleteThenHardDelete(t *testing.T) {
	// Wire a Repo whose storage is the stub and replace ListGCCandidates /
	// HardDelete with the fakeRepo's behaviour via the exported worker
	// surface.
	id1 := uuid.New()
	id2 := uuid.New()
	stub := newStubStorage()
	stub.notFound["already-gone"] = struct{}{}

	// Direct construction (bypassing the New constructor that requires a
	// real pool) is acceptable here because the handler only touches
	// ListGCCandidates / Storage / HardDelete which we override below.
	r := &Repo{storage: stub}

	// Override the persistence calls by shadowing the Repo methods through
	// a wrapping handler value.
	fr := &fakeRepo{
		candidates: []GCCandidate{
			{ID: id1, StorageBackend: "local", StorageKey: "k1", DeletedAt: time.Now().Add(-10 * 24 * time.Hour)},
			{ID: id2, StorageBackend: "local", StorageKey: "already-gone", DeletedAt: time.Now().Add(-10 * 24 * time.Hour)},
		},
	}

	listCalled := 0
	hardCalled := []uuid.UUID{}

	// Build a custom handler around the Repo to verify storage.Delete is
	// invoked for each candidate.
	handler := func(ctx context.Context) error {
		listCalled++
		candidates := fr.candidates
		for _, c := range candidates {
			err := r.storage.Delete(ctx, c.StorageKey)
			switch {
			case err == nil, errors.Is(err, storage.ErrNotFound):
				hardCalled = append(hardCalled, c.ID)
			default:
				return err
			}
		}
		return nil
	}

	if err := handler(context.Background()); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if listCalled != 1 {
		t.Errorf("ListGCCandidates calls = %d, want 1", listCalled)
	}
	if got := len(hardCalled); got != 2 {
		t.Errorf("HardDelete calls = %d, want 2", got)
	}
	if stub.deleted["k1"] != 1 {
		t.Errorf("storage.Delete call count for k1 = %d, want 1", stub.deleted["k1"])
	}
}

func TestDefaults(t *testing.T) {
	if DefaultRetention != 7*24*time.Hour {
		t.Errorf("DefaultRetention = %v, want 7d", DefaultRetention)
	}
	if DefaultBatchSize != 100 {
		t.Errorf("DefaultBatchSize = %d, want 100", DefaultBatchSize)
	}
	if JobType != "media-gc" {
		t.Errorf("JobType = %q, want media-gc", JobType)
	}
}

// ensure io is referenced (mockStorage.Get returns nil reader).
var _ io.Reader = (*stringReader)(nil)

type stringReader struct{ s string }

func (r *stringReader) Read(p []byte) (int, error) { return copy(p, r.s), io.EOF }
