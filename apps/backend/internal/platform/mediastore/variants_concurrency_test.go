package mediastore

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/image/draw"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
)

// variantFixture stores a 1000x600 JPEG original and returns the repo, a
// helper to build objects over it and the storage.
func variantFixture(t *testing.T, workers int) (*Repo, storage.Storage, func() Object) {
	t.Helper()
	ctx := context.Background()
	st, err := storage.NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := &Repo{storage: st, renderSem: make(chan struct{}, workers)}
	raw := encodeJPEG(t, 1000, 600)
	newObj := func() Object {
		id := uuid.New()
		key := "shop_product/" + id.String()
		if _, err := st.Put(ctx, storage.PutInput{Key: key, ContentType: "image/jpeg", Body: bytes.NewReader(raw)}); err != nil {
			t.Fatal(err)
		}
		return Object{ID: id, StorageKey: key, ContentType: "image/jpeg", ByteSize: int64(len(raw))}
	}
	return repo, st, newObj
}

func TestOpenVariant_ConcurrentRequestsShareOneRender(t *testing.T) {
	repo, _, newObj := variantFixture(t, 1)
	obj := newObj()
	var renders atomic.Int32
	repo.renderHook = func(string) {
		renders.Add(1)
		time.Sleep(60 * time.Millisecond) // keep the flight open so the others join it
	}
	const n = 12
	var wg sync.WaitGroup
	var okCount atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, ok := repo.OpenVariant(context.Background(), obj, 240)
			if ok {
				okCount.Add(1)
				_ = res.Body.Close()
			}
		}()
	}
	wg.Wait()
	if got := renders.Load(); got != 1 {
		t.Fatalf("rendered %d times, want 1", got)
	}
	if got := okCount.Load(); got != n {
		t.Fatalf("%d of %d requests got the copy", got, n)
	}
}

func TestOpenVariant_RenderSlotsLimitConcurrency(t *testing.T) {
	for _, workers := range []int{1, 2} {
		repo, _, newObj := variantFixture(t, workers)
		var running, peak atomic.Int32
		repo.renderHook = func(string) {
			cur := running.Add(1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			running.Add(-1)
		}
		objs := []Object{newObj(), newObj(), newObj(), newObj()}
		var wg sync.WaitGroup
		for _, o := range objs {
			wg.Add(1)
			go func(o Object) {
				defer wg.Done()
				if res, ok := repo.OpenVariant(context.Background(), o, 240); ok {
					_ = res.Body.Close()
				} else {
					t.Error("expected a variant")
				}
			}(o)
		}
		wg.Wait()
		if got := int(peak.Load()); got != workers {
			t.Fatalf("workers=%d: peak concurrent renders = %d", workers, got)
		}
	}
}

func TestOpenVariant_CancelledWhileWaitingServesTheOriginal(t *testing.T) {
	repo, _, newObj := variantFixture(t, 1)
	obj := newObj()
	repo.renderSem <- struct{}{} // every slot taken by someone else

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if res, ok := repo.OpenVariant(ctx, obj, 240); ok {
		_ = res.Body.Close()
		t.Fatal("a request that gave up must not get a variant")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("waited %v after the context ended", d)
	}
	<-repo.renderSem // free the slot: the detached render finishes
	// A later request gets the copy (the abandoned render still stored it).
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, ok := repo.OpenVariant(context.Background(), obj, 240)
		if ok {
			_ = res.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the variant never appeared")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestOpenVariant_SecondLookAfterTakingTheSlot(t *testing.T) {
	repo, st, newObj := variantFixture(t, 1)
	obj := newObj()
	key := VariantKey(obj.ID, 240, "jpg")
	// Another process stored the copy while this render queued.
	if _, err := st.Put(context.Background(), storage.PutInput{Key: key, ContentType: "image/jpeg", Body: bytes.NewReader(encodeJPEG(t, 240, 144))}); err != nil {
		t.Fatal(err)
	}
	var renders atomic.Int32
	repo.renderHook = func(string) { renders.Add(1) }
	if err := repo.renderVariant(context.Background(), obj, 240, "jpg", key); err != nil {
		t.Fatal(err)
	}
	if renders.Load() != 0 {
		t.Fatal("rendered although the copy was already stored")
	}
}

func TestRenderVariants_DecodesOnceAndSkipsWhatExists(t *testing.T) {
	repo, st, newObj := variantFixture(t, 1)
	ctx := context.Background()
	obj := newObj()
	// 240 already exists; 1200 is wider than the picture.
	existing := VariantKey(obj.ID, 240, "jpg")
	if _, err := st.Put(ctx, storage.PutInput{Key: existing, ContentType: "image/jpeg", Body: bytes.NewReader([]byte("x"))}); err != nil {
		t.Fatal(err)
	}
	var renders atomic.Int32
	repo.renderHook = func(string) { renders.Add(1) }
	n := repo.RenderVariants(ctx, obj, []int{165, 240, 360, 1200}, nil)
	if n != 2 || renders.Load() != 2 {
		t.Fatalf("stored %d, rendered %d, want 2 and 2 (165 and 360)", n, renders.Load())
	}
	for _, w := range []int{165, 360} {
		if _, err := st.Stat(ctx, VariantKey(obj.ID, w, "jpg")); err != nil {
			t.Errorf("width %d not stored: %v", w, err)
		}
	}
	if _, err := st.Stat(ctx, VariantKey(obj.ID, 1200, "jpg")); err == nil {
		t.Error("a picture is never upscaled")
	}
	// Second pass: nothing left to do.
	if n := repo.RenderVariants(ctx, obj, []int{165, 240, 360}, nil); n != 0 {
		t.Fatalf("second pass stored %d", n)
	}
	// A GIF is not resized at all.
	gif := obj
	gif.ContentType = "image/gif"
	if n := repo.RenderVariants(ctx, gif, PrerenderWidths, nil); n != 0 {
		t.Fatalf("a GIF stored %d copies", n)
	}
}

func TestRenderVariantsHandler_RejectsBadPayloadAndNilRepo(t *testing.T) {
	h := NewRenderVariantsHandler(RenderVariantsHandlerOptions{Repo: &Repo{}})
	if err := h(context.Background(), []byte(`{`)); err == nil {
		t.Error("bad JSON must fail")
	}
	if err := h(context.Background(), []byte(`{"media_id":"nope"}`)); err == nil {
		t.Error("a bad id must fail")
	}
	if err := NewRenderVariantsHandler(RenderVariantsHandlerOptions{})(context.Background(), []byte(`{}`)); err == nil {
		t.Error("a nil repo must fail")
	}
}

// A picture big enough to be scaled in several bands must come out the same as
// one single Catmull-Rom pass (to within rounding of the band edges).
func TestScaleBanded_MatchesASinglePass(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	src := image.NewRGBA(image.Rect(0, 0, 1600, 2400))
	for y := 0; y < 2400; y++ {
		for x := 0; x < 1600; x++ {
			base := uint8((x/40 + y/60) * 9)
			src.SetRGBA(x, y, color.RGBA{R: base + uint8(rng.Intn(24)), G: uint8(y * 255 / 2400), B: uint8(x * 255 / 1600), A: 255})
		}
	}
	for _, dw := range []int{240, 533, 720} {
		dh := (2400*dw + 800) / 1600
		if dw*2400 <= scaleTmpEntries {
			t.Fatalf("dw=%d would not be banded", dw)
		}
		want := image.NewRGBA(image.Rect(0, 0, dw, dh))
		draw.CatmullRom.Scale(want, want.Bounds(), src, src.Bounds(), draw.Src, nil)
		got := image.NewRGBA(image.Rect(0, 0, dw, dh))
		scaleBanded(got, src)
		var maxDiff, sum int
		for i := range want.Pix {
			d := int(want.Pix[i]) - int(got.Pix[i])
			if d < 0 {
				d = -d
			}
			sum += d
			if d > maxDiff {
				maxDiff = d
			}
		}
		mean := float64(sum) / float64(len(want.Pix))
		if mean > 0.3 || maxDiff > 40 {
			t.Errorf("dw=%d: mean diff %.3f, max %d", dw, mean, maxDiff)
		}
	}
}
