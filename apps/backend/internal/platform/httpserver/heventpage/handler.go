// Package heventpage serves POST /v1/organizations/{org_id}/event-page-facts:
// it reads the organizer's own event page and answers what the page says, as
// hints for the event wizards of the Telegram bot and the web event center
// (spec 08_architecture/59). Nothing is created but the page's picture,
// stored as an unattached media object the organizer may choose as the
// poster.
package heventpage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/egress"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventpage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
)

// Windows of the caps (spec 59 §4.4 and §8.8).
const (
	// PageReadsWindow is the window of the page-read cap.
	PageReadsWindow = time.Hour
	// PageReadsPerOrg is how many pages one organization may have read an hour.
	PageReadsPerOrg = 20
	// ModelReadsWindow is the window of the model-read cap.
	ModelReadsWindow = 24 * time.Hour
	// MaxConcurrentReadings bounds the readings one arena-api process runs
	// at once, whatever the organization: a page, its parsed JSON-LD and up
	// to three pictures are held in memory while one runs.
	MaxConcurrentReadings = 4
)

// Time budget of one request: the page, then maybe the model, then the
// picture, on a deadline of their own (the router gives a request 30 s).
const (
	totalBudget   = 60 * time.Second
	writeSlack    = 10 * time.Second
	modelBudget   = 30 * time.Second
	pictureBudget = 15 * time.Second
	maxBodyBytes  = 8 << 10
)

// RowOrgAccess reports whether the caller may act on a row owned by orgID.
// When it refuses it writes the route's own 404 and returns false.
type RowOrgAccess func(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, write bool, notFoundCode, notFoundMessage string) bool

// Quota is a per-organization cap; ratelimit.SlidingWindow implements it.
// Take records a use only while the organization is under its limit.
type Quota interface {
	Take(key string) bool
}

// pictureStore is the part of mediastore.Repo the route writes the picture
// with, narrow so the handler is testable without a database.
type pictureStore interface {
	PutAndStream(ctx context.Context, key, contentType string, body io.Reader) (string, int64, error)
	Insert(ctx context.Context, in mediastore.InsertInput) (mediastore.Object, error)
	FindByChecksum(ctx context.Context, orgID uuid.UUID, ownerType, sha256Hex string) (mediastore.Object, error)
	Backend() string
}

// Handler serves the route.
type Handler struct {
	logger       *slog.Logger
	fetcher      *eventpage.Fetcher
	model        eventpage.TextReader
	pageQuota    Quota
	modelQuota   Quota
	media        pictureStore
	rowOrgAccess RowOrgAccess
	pictureGate  func(r *http.Request) bool
	slots        chan struct{}
	now          func() time.Time
}

// New builds the handler on the production egress policy (egress.Fetch),
// with the page cap on and no model, no picture store and no guard.
func New(logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		logger:    logger,
		fetcher:   eventpage.NewFetcher(egress.Fetch),
		pageQuota: ratelimit.New(ratelimit.Config{MaxAttempts: PageReadsPerOrg, Window: PageReadsWindow}),
		slots:     make(chan struct{}, MaxConcurrentReadings),
		now:       time.Now,
	}
}

// WithPictureGate says whether the caller may store the page's picture
// (media.write in the path organization). Without it the picture is stored
// for any caller who passed the route's gate.
func (h *Handler) WithPictureGate(allowed func(r *http.Request) bool) *Handler {
	h.pictureGate = allowed
	return h
}

// WithPolicy replaces the egress policy (a test pins egress.PrivateDeny).
func (h *Handler) WithPolicy(p egress.Policy) *Handler {
	h.fetcher = eventpage.NewFetcher(p)
	return h
}

// WithModel wires the text model and its per-organization daily cap. A nil
// reader leaves the model off; a nil quota switches the cap off.
func (h *Handler) WithModel(m eventpage.TextReader, quota Quota) *Handler {
	h.model = m
	h.modelQuota = quota
	return h
}

// WithPageQuota replaces the page cap (nil = no cap).
func (h *Handler) WithPageQuota(q Quota) *Handler {
	h.pageQuota = q
	return h
}

// WithMedia wires the picture store; nil leaves pictures out.
func (h *Handler) WithMedia(m *mediastore.Repo) *Handler {
	if m != nil {
		h.media = m
	}
	return h
}

// withPictureStore replaces the picture store (tests).
func (h *Handler) withPictureStore(s pictureStore) *Handler {
	h.media = s
	return h
}

// WithRowOrgAccess wires the organization guard.
func (h *Handler) WithRowOrgAccess(fn RowOrgAccess) *Handler {
	h.rowOrgAccess = fn
	return h
}

// withClock replaces the clock (tests).
func (h *Handler) withClock(now func() time.Time) *Handler {
	h.now = now
	return h
}

type factsRequest struct {
	URL string `json:"url"`
}

type factsResponse struct {
	Facts    eventpage.Facts             `json:"facts"`
	Sources  map[string]eventpage.Source `json:"sources"`
	FinalURL string                      `json:"final_url"`
	Language string                      `json:"language"`
	Warnings []eventpage.Warning         `json:"warnings"`
}

// PageFacts serves POST /v1/organizations/{org_id}/event-page-facts.
//
// Order: the caller and the organization, the body, the address (no
// request is made for a refused one), then the page cap, the fetch, the
// structured reading, the model only when the page left the dates or the
// prices missing (owner decision 03.11), and the picture. The page's text,
// its address and the model's answer are never logged; a log line names
// the site's origin only.
func (h *Handler) PageFacts(w http.ResponseWriter, r *http.Request) {
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	if h.rowOrgAccess == nil {
		writeErr(w, r, http.StatusServiceUnavailable, "dependency.org_access_unavailable", "organization access guard is not configured", nil)
		return
	}
	// A reading may cost a model call and stores a picture: it is a write.
	if !h.rowOrgAccess(w, r, orgID, true, "org.not_found", "organization not found") {
		return
	}
	var req factsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, r, http.StatusBadRequest, "event_page.invalid_request", "the body must be JSON with url", nil)
		return
	}
	ctx := r.Context()
	if _, err := h.fetcher.CheckURL(ctx, req.URL); err != nil {
		h.writeFetchErr(w, r, err)
		return
	}
	// A busy process refuses before the cap is taken: the person tries
	// again in a moment without losing one of the organization's readings.
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		writeErr(w, r, http.StatusServiceUnavailable, "event_page.busy", "many pages are being read right now, try again in a minute", nil)
		return
	}
	if h.pageQuota != nil && !h.pageQuota.Take(orgID.String()) {
		writeErr(w, r, http.StatusTooManyRequests, "event_page.rate_limited", "this organization has read many pages in the last hour, try again later", nil)
		return
	}
	storePicture := h.pictureGate == nil || h.pictureGate(r)

	// The work runs on its own deadline and the write deadline moves past
	// it, as the poster reading does. A caller that hangs up does not stop
	// a reading already started.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(totalBudget + writeSlack))
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), totalBudget)
	defer cancel()

	page, err := h.fetcher.FetchPage(work, req.URL)
	if err != nil {
		h.logger.Info("event page: fetch refused", slog.String("site", egress.Origin(req.URL)), slog.String("reason", fetchReason(err)))
		h.writeFetchErr(w, r, err)
		return
	}
	now := h.now()
	res := eventpage.Extract(page, now)
	if res.NeedsModel() {
		h.readWithModel(work, orgID, page, &res, now)
	}
	if res.Empty() {
		writeErr(w, r, http.StatusUnprocessableEntity, "event_page.nothing_found", "the page says nothing about an event that we could read", nil)
		return
	}
	if storePicture {
		h.attachPicture(work, orgID, &res)
	}

	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusOK, factsResponse{
		Facts:    res.Facts,
		Sources:  res.Sources,
		FinalURL: page.FinalURL.String(),
		Language: res.Language,
		Warnings: res.Warnings,
	})
}

// readWithModel asks the text model what the structured sources could not
// say. Every failure only adds a warning: the page reading stands.
func (h *Handler) readWithModel(ctx context.Context, orgID uuid.UUID, page eventpage.Page, res *eventpage.Result, now time.Time) {
	if h.model == nil {
		res.AddWarning(eventpage.Warning{Code: eventpage.WarnModelUnavailable})
		return
	}
	if h.modelQuota != nil && !h.modelQuota.Take(orgID.String()) {
		res.AddWarning(eventpage.Warning{Code: eventpage.WarnModelQuota})
		return
	}
	mctx, cancel := context.WithTimeout(ctx, modelBudget)
	defer cancel()
	mf, err := h.model.ReadText(mctx, res.Text, now)
	if err != nil {
		if !errors.Is(err, eventpage.ErrModelNotConfigured) {
			// The error never quotes the page or the answer (ModelAPIError).
			h.logger.Warn("event page: model reading failed", slog.String("site", egress.Origin(page.FinalURL.String())), slog.String("error", err.Error()))
		}
		res.AddWarning(eventpage.Warning{Code: eventpage.WarnModelUnavailable})
		return
	}
	res.MergeModel(mf, now)
}

// attachPicture downloads the page's pictures in order and stores the first
// that fits the poster rules; when none fits, the first one that could be
// downloaded is stored with a warning. At most one media object is written.
func (h *Handler) attachPicture(ctx context.Context, orgID uuid.UUID, res *eventpage.Result) {
	if h.media == nil || len(res.ImageURLs) == 0 {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, pictureBudget)
	defer cancel()
	var chosen *eventpage.Picture
	var chosenSource eventpage.Source
	for _, c := range res.ImageURLs {
		pic, err := h.fetcher.FetchImage(pctx, c.URL)
		if err != nil {
			continue
		}
		if chosen == nil || eventpage.FitsPoster(pic.Width, pic.Height) {
			p := pic
			chosen, chosenSource = &p, c.Source
		}
		if eventpage.FitsPoster(pic.Width, pic.Height) {
			break
		}
	}
	if chosen == nil {
		res.AddWarning(eventpage.Warning{Code: eventpage.WarnImageUnavailable})
		return
	}
	id, err := h.storePicture(pctx, orgID, *chosen)
	if err != nil {
		h.logger.Warn("event page: picture not stored", slog.String("error", err.Error()))
		res.AddWarning(eventpage.Warning{Code: eventpage.WarnImageUnavailable})
		return
	}
	fits := eventpage.FitsPoster(chosen.Width, chosen.Height)
	if !fits {
		res.AddWarning(eventpage.Warning{Code: eventpage.WarnImageNotPoster,
			Values: []string{strconv.Itoa(chosen.Width) + "×" + strconv.Itoa(chosen.Height)}})
	}
	res.SetImage(eventpage.Image{MediaID: id.String(), Width: chosen.Width, Height: chosen.Height, FitsPoster: fits}, chosenSource)
}

// storePicture writes the picture as an event_poster media object of the
// organization, attached to nothing. The same picture read again (the page
// read twice) is not stored a second time: the organization's existing
// object with the same bytes is answered.
func (h *Handler) storePicture(ctx context.Context, orgID uuid.UUID, p eventpage.Picture) (uuid.UUID, error) {
	digest := sha256.Sum256(p.Data)
	if obj, err := h.media.FindByChecksum(ctx, orgID, "event_poster", hex.EncodeToString(digest[:])); err == nil {
		return obj.ID, nil
	} else if !errors.Is(err, mediastore.ErrNotFound) {
		return uuid.Nil, err
	}
	key, err := mediastore.NewStorageKey("event_poster")
	if err != nil {
		return uuid.Nil, err
	}
	sum, size, err := h.media.PutAndStream(ctx, key, p.ContentType, bytes.NewReader(p.Data))
	if err != nil {
		return uuid.Nil, err
	}
	if size != int64(len(p.Data)) {
		return uuid.Nil, errors.New("storage wrote a different size than the picture")
	}
	// DecodePicture bounds both sides by MaxImagePixels, far inside int32.
	w, hgt := int32(min(p.Width, eventpage.MaxImagePixels)), int32(min(p.Height, eventpage.MaxImagePixels)) // #nosec G115 -- bounded above
	obj, err := h.media.Insert(ctx, mediastore.InsertInput{
		OrgID:          &orgID,
		OwnerType:      "event_poster",
		StorageBackend: h.media.Backend(),
		StorageKey:     key,
		ContentType:    p.ContentType,
		ByteSize:       size,
		ChecksumSHA256: sum,
		Width:          &w,
		Height:         &hgt,
	})
	if err != nil {
		return uuid.Nil, err
	}
	return obj.ID, nil
}

// writeFetchErr answers a fetch failure with its stable code. Never 502:
// Cloudflare replaces an origin 502 with its own page.
func (h *Handler) writeFetchErr(w http.ResponseWriter, r *http.Request, err error) {
	var up *eventpage.UpstreamError
	switch {
	case errors.Is(err, eventpage.ErrInvalidURL):
		writeErr(w, r, http.StatusBadRequest, "event_page.invalid_url", "the address must be a full http(s) address of a web page", nil)
	case errors.Is(err, eventpage.ErrTicketingPlatform):
		writeErr(w, r, http.StatusUnprocessableEntity, "event_page.ticketing_platform", "this is a page of a ticketing platform; use the event page on your own site", nil)
	case errors.Is(err, eventpage.ErrNotAllowed):
		writeErr(w, r, http.StatusUnprocessableEntity, "event_page.destination_not_allowed", "the address is not on the public internet", nil)
	case errors.Is(err, eventpage.ErrNotHTML):
		writeErr(w, r, http.StatusUnprocessableEntity, "event_page.not_html", "the address is not a web page", nil)
	case errors.Is(err, eventpage.ErrTooLarge):
		writeErr(w, r, http.StatusUnprocessableEntity, "event_page.too_large", "the page is too large to read", nil)
	case errors.As(err, &up):
		writeErr(w, r, http.StatusServiceUnavailable, "event_page.fetch_failed", "the site answered with an error", map[string]any{"status": up.Status})
	default:
		writeErr(w, r, http.StatusServiceUnavailable, "event_page.fetch_failed", "the site did not answer in time", nil)
	}
}

// fetchReason is the log word of a fetch failure.
func fetchReason(err error) string {
	var up *eventpage.UpstreamError
	switch {
	case errors.Is(err, eventpage.ErrTicketingPlatform):
		return "ticketing_platform"
	case errors.Is(err, eventpage.ErrNotAllowed):
		return "not_public"
	case errors.Is(err, eventpage.ErrNotHTML):
		return "not_html"
	case errors.Is(err, eventpage.ErrTooLarge):
		return "too_large"
	case errors.As(err, &up):
		return "http_" + strconv.Itoa(up.Status)
	case errors.Is(err, eventpage.ErrInvalidURL):
		return "invalid_url"
	}
	return "unreachable"
}

func writeErr(w http.ResponseWriter, r *http.Request, status int, code, msg string, details map[string]any) {
	if details == nil {
		httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
		return
	}
	httputil.WriteJSON(w, status, httputil.ErrorEnvelopeWithDetails(code, msg, r, details))
}
