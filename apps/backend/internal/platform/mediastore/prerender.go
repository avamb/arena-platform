package mediastore

// prerender.go - resized copies rendered right after an upload, by the
// media.render_variants worker job, so the first buyer of a shop page does
// not wait for (or pay the memory of) the render.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// RenderVariantsJobType is the worker job that renders the common widths of
// one stored picture. Payload: {"media_id": "<uuid>"}.
const RenderVariantsJobType = "media.render_variants"

// PrerenderWidths are the widths rendered after an upload: what the shop
// storefront and the event center ask for. Larger widths (and 533) are still
// rendered on demand.
var PrerenderWidths = []int{165, 240, 360, 480, 720}

type renderVariantsPayload struct {
	MediaID string `json:"media_id"`
}

// queuePrerender queues the render job for a freshly inserted picture. Best
// effort: Insert has already committed on its own pool connection, so a failed
// enqueue costs nothing but the head start (the copy is then rendered on the
// first request, as before).
func (r *Repo) queuePrerender(ctx context.Context, obj Object) {
	if !r.prerender || r.pool == nil || variantExt(obj.ContentType) == "" {
		return
	}
	if obj.Width != nil && int(*obj.Width) <= PrerenderWidths[0] {
		return
	}
	if _, err := worker.EnqueuePayload(ctx, r.pool, RenderVariantsJobType,
		renderVariantsPayload{MediaID: obj.ID.String()}, 3); err != nil {
		slog.Warn("mediastore: could not queue variant render",
			"media_id", obj.ID.String(), "error", err.Error())
	}
}

// RenderVariants renders and stores the given widths of obj, skipping copies
// that exist and widths the picture is not wider than. The original is read
// and decoded ONCE for all of them, under one render slot. A width that fails
// is logged and skipped. It returns how many copies it stored.
func (r *Repo) RenderVariants(ctx context.Context, obj Object, widths []int, logger *slog.Logger) int {
	ext := variantExt(obj.ContentType)
	if ext == "" {
		return 0
	}
	if logger == nil {
		logger = slog.Default()
	}
	type todo struct {
		w   int
		key string
	}
	var missing []todo
	for _, w := range widths {
		if obj.Width != nil && int(*obj.Width) <= w {
			continue
		}
		key := VariantKey(obj.ID, w, ext)
		if _, err := r.storage.Stat(ctx, key); err == nil {
			continue
		}
		missing = append(missing, todo{w, key})
	}
	if len(missing) == 0 {
		return 0
	}
	release, err := r.acquireRender(ctx)
	if err != nil {
		logger.Warn("variant prerender: no render slot", "media_id", obj.ID.String(), "error", err.Error())
		return 0
	}
	defer release()
	raw, err := r.readOriginal(ctx, obj)
	if err != nil {
		logger.Warn("variant prerender: cannot read original", "media_id", obj.ID.String(), "error", err.Error())
		return 0
	}
	// Decode for the smallest missing width: every other one is larger, so a
	// picture that can be shrunk to the first is a candidate for all.
	pic, ok := decodeForResize(raw, missing[0].w)
	if !ok {
		return 0
	}
	stored := 0
	for _, m := range missing {
		if pic.width <= m.w {
			continue
		}
		// Another render (an on-demand request) may have stored it meanwhile.
		if _, err := r.storage.Stat(ctx, m.key); err == nil {
			continue
		}
		if r.renderHook != nil {
			r.renderHook(m.key)
		}
		out, ok := scaleAndEncode(pic, ext, m.w)
		if !ok {
			continue
		}
		if err := r.storeVariant(ctx, m.key, ext, out); err != nil {
			logger.Warn("variant prerender: store failed", "media_id", obj.ID.String(), "width", m.w, "error", err.Error())
			continue
		}
		stored++
	}
	return stored
}

// RenderVariantsHandlerOptions bundles the dependencies of the handler.
type RenderVariantsHandlerOptions struct {
	Repo   *Repo
	Logger *slog.Logger
	// Widths overrides PrerenderWidths (tests).
	Widths []int
}

// NewRenderVariantsHandler returns the media.render_variants worker handler.
// A picture that is gone, is not a resizable format or cannot be decoded is
// not an error: there is nothing to retry. A storage hiccup on a single width
// is logged and the job still succeeds (best effort: the width is rendered on
// demand later).
func NewRenderVariantsHandler(opts RenderVariantsHandlerOptions) func(ctx context.Context, payload []byte) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	widths := opts.Widths
	if len(widths) == 0 {
		widths = PrerenderWidths
	}
	return func(ctx context.Context, payload []byte) error {
		if opts.Repo == nil {
			return errors.New(RenderVariantsJobType + ": repo is nil")
		}
		var p renderVariantsPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("%s: bad payload: %w", RenderVariantsJobType, err)
		}
		id, err := uuid.Parse(p.MediaID)
		if err != nil {
			return fmt.Errorf("%s: bad media_id: %w", RenderVariantsJobType, err)
		}
		obj, err := opts.Repo.GetByID(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err // a database error is worth a retry
		}
		n := opts.Repo.RenderVariants(ctx, obj, widths, logger)
		logger.Info("variant prerender done", "media_id", id.String(), "rendered", n)
		return nil
	}
}
