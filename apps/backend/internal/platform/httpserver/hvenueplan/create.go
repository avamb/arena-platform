package hvenueplan

// create.go — POST /v1/organizations/{org_id}/venues/{venue_id}/plan-intakes.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
)

// CreateBodyLimit is the body cap of the create route: 80 MB of files plus the
// multipart framing. The server lets this route past the global body limit.
const CreateBodyLimit = maxTotalBytes + multipartSlack

// incoming is one checked file, its bytes already in storage.
type incoming struct {
	kind            venueplan.SourceKind
	name            string
	data            []byte
	sha             string
	key             string
	pngKey          string
	pngSize         int64
	pngSHA          string
	pages           *int32
	width, height   *int32
	needsConversion bool
}

// stored tracks what reached storage, to remove it again when the request fails.
type stored struct {
	h    *Handler
	keys []string
}

func (s *stored) put(ctx context.Context, ownerType, contentType string, data []byte) (string, string, int64, error) {
	key, err := mediastore.NewStorageKey(ownerType)
	if err != nil {
		return "", "", 0, err
	}
	sha, size, err := s.h.media.PutAndStream(ctx, key, contentType, bytes.NewReader(data))
	if err != nil {
		return "", "", 0, err
	}
	s.keys = append(s.keys, key)
	return key, sha, size, nil
}

func (s *stored) drop(ctx context.Context) {
	clean := context.WithoutCancel(ctx)
	for _, k := range s.keys {
		_ = s.h.media.Storage().Delete(clean, k)
	}
	s.keys = nil
}

// HandleCreate answers POST /v1/organizations/{org_id}/venues/{venue_id}/plan-intakes.
func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	venueID, ok := httputil.UUIDPathParam(w, r, "venue_id")
	if !ok {
		return
	}
	if !h.venueOpenTo(w, r, venueID, orgID) || refuseAPIKey(w, r) {
		return
	}
	idemKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if utf8.RuneCountInString(idemKey) > 200 {
		writeErrDetails(w, r, http.StatusBadRequest, "venue_plan_intake.invalid_idempotency_key",
			"Idempotency-Key must be at most 200 characters", map[string]any{"field": "Idempotency-Key"})
		return
	}
	if idemKey != "" && h.replay(w, r, orgID, venueID, idemKey) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, CreateBodyLimit)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, r, http.StatusBadRequest, "venue_plan_intake.invalid_multipart",
			"send the files as multipart/form-data in the field files")
		return
	}
	st := &stored{h: h}
	files, channel, ok := h.readParts(w, r, mr, st)
	if !ok {
		st.drop(ctx)
		return
	}
	in, created, err := h.createIntake(ctx, r, orgID, venueID, channel, idemKey, files)
	if err != nil {
		st.drop(ctx)
		h.failed(w, r, "create", err)
		return
	}
	if !created {
		// Another request with the same Idempotency-Key won the race: the
		// same answer as a replay, including the check of the venue.
		st.drop(ctx)
		h.answerExisting(w, r, in, venueID)
		return
	}
	h.writeCard(w, r, http.StatusAccepted, in, false)
}

// venueOpenTo checks that the venue is the organization's own, or a verified
// active venue of another organization (migration 0182); 404 otherwise.
func (h *Handler) venueOpenTo(w http.ResponseWriter, r *http.Request, venueID, orgID uuid.UUID) bool {
	ctx := r.Context()
	notFound := func() bool {
		writeErr(w, r, http.StatusNotFound, "venue.not_found", "venue not found")
		return false
	}
	v, err := h.queries.GetVenueByID(ctx, venueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound()
		}
		h.failed(w, r, "venue lookup", err)
		return false
	}
	if v.OrgID == orgID {
		return true
	}
	sc, err := h.queries.GetVenueSessionContext(ctx, venueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound()
		}
		h.failed(w, r, "venue lookup", err)
		return false
	}
	if !sc.Verified || v.Status != "active" {
		return notFound()
	}
	return true
}

// replay answers a repeated Idempotency-Key with the intake it made.
func (h *Handler) replay(w http.ResponseWriter, r *http.Request, orgID, venueID uuid.UUID, key string) bool {
	in, err := h.queries.GetVenuePlanIntakeByIdempotencyKey(r.Context(), orgID, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		h.failed(w, r, "idempotency lookup", err)
		return true
	}
	h.answerExisting(w, r, in, venueID)
	return true
}

// answerExisting answers a create whose Idempotency-Key already made an
// intake, both for a replay and for the request that lost the insert race:
// 409 venue_plan_intake.idempotency_key_reused when that intake is for
// another venue, else the intake with 200.
func (h *Handler) answerExisting(w http.ResponseWriter, r *http.Request, in gen.VenuePlanIntakeRow, venueID uuid.UUID) {
	if in.VenueID != venueID {
		writeErr(w, r, http.StatusConflict, "venue_plan_intake.idempotency_key_reused",
			"this Idempotency-Key was already used for another venue, please send a new key")
		return
	}
	h.writeCard(w, r, http.StatusOK, in, false)
}

// readParts reads the multipart body: the files (1..10, 80 MB in total, each
// checked by content and stored at once) and the channel.
func (h *Handler) readParts(w http.ResponseWriter, r *http.Request, mr *multipart.Reader, st *stored) ([]incoming, venueplan.Channel, bool) {
	ctx := r.Context()
	channel := venueplan.ChannelEventCenterWeb
	var files []incoming
	var total int64
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			h.bodyError(w, r, err)
			return nil, "", false
		}
		switch part.FormName() {
		case "channel":
			raw, _ := io.ReadAll(io.LimitReader(part, 64))
			channel = venueplan.Channel(strings.TrimSpace(string(raw)))
			if !channel.Valid() {
				writeErrDetails(w, r, http.StatusBadRequest, "venue_plan_intake.invalid_channel",
					"channel must be telegram_bot or event_center_web", map[string]any{"field": "channel"})
				return nil, "", false
			}
		case "files":
			if len(files) == maxFiles {
				writeErrDetails(w, r, http.StatusBadRequest, "venue_plan_intake.too_many_files",
					"send at most 10 files in one intake", map[string]any{"limit": maxFiles})
				return nil, "", false
			}
			data, err := io.ReadAll(io.LimitReader(part, maxFileBytes+1))
			if err != nil {
				h.bodyError(w, r, err)
				return nil, "", false
			}
			name := cleanFileName(part.FileName())
			total += int64(len(data))
			if total > maxTotalBytes {
				writeErrDetails(w, r, http.StatusRequestEntityTooLarge, "venue_plan_intake.total_too_large",
					"the files of one intake may be at most 80 MB together", map[string]any{"limit_mb": maxTotalBytes / mb})
				return nil, "", false
			}
			f, ok := h.checkFile(w, r, name, data)
			if !ok {
				return nil, "", false
			}
			if !h.storeFile(w, r, st, &f) {
				return nil, "", false
			}
			files = append(files, f)
		default:
			_, _ = io.Copy(io.Discard, io.LimitReader(part, maxFileBytes))
		}
		_ = part.Close()
		if ctx.Err() != nil {
			h.bodyError(w, r, ctx.Err())
			return nil, "", false
		}
	}
	if len(files) == 0 {
		writeErr(w, r, http.StatusBadRequest, "venue_plan_intake.no_files",
			"send at least one file in the multipart field files")
		return nil, "", false
	}
	return files, channel, true
}

func (h *Handler) bodyError(w http.ResponseWriter, r *http.Request, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeErrDetails(w, r, http.StatusRequestEntityTooLarge, "venue_plan_intake.total_too_large",
			"the files of one intake may be at most 80 MB together", map[string]any{"limit_mb": maxTotalBytes / mb})
		return
	}
	writeErr(w, r, http.StatusBadRequest, "venue_plan_intake.invalid_multipart",
		"the multipart body could not be read, please send the files again")
}

func cleanFileName(raw string) string {
	name := strings.TrimSpace(path.Base(strings.ReplaceAll(raw, "\\", "/")))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == "/" {
		name = "file"
	}
	if utf8.RuneCountInString(name) > 255 {
		name = string([]rune(name)[:255])
	}
	return name
}

// checkFile decides a file's kind by its content and applies the limits of
// that kind. A JPEG loses its metadata here.
func (h *Handler) checkFile(w http.ResponseWriter, r *http.Request, name string, data []byte) (incoming, bool) {
	f := incoming{name: name, data: data}
	f.kind = sniffKind(data)
	if f.kind == "" {
		writeErrDetails(w, r, http.StatusUnsupportedMediaType, "venue_plan_intake.unsupported_format",
			"we cannot read this file, please send a PDF, an Excel table or a picture of the plan",
			map[string]any{"file_name": name})
		return f, false
	}
	if limit := kindLimit(f.kind); int64(len(data)) > limit {
		writeErrDetails(w, r, http.StatusRequestEntityTooLarge, "venue_plan_intake.file_too_large",
			"the file is larger than its format allows, please send a smaller file",
			map[string]any{"file_name": name, "kind": string(f.kind), "limit_mb": limit / mb})
		return f, false
	}
	if len(data) == 0 {
		writeErrDetails(w, r, http.StatusBadRequest, "venue_plan_intake.empty_file", "the file is empty",
			map[string]any{"file_name": name})
		return f, false
	}
	switch {
	case f.kind == venueplan.SourcePDF:
		if n := pdfPages(data); n > maxPDFPages {
			writeErrDetails(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.too_many_pages",
				"a PDF may have at most 20 pages, please send the pages of the plan only",
				map[string]any{"file_name": name, "limit": maxPDFPages})
			return f, false
		} else if n > 0 {
			p := int32(n) //nolint:gosec // bounded above
			f.pages = &p
		}
	case f.kind == venueplan.SourceXLSX:
		if xlsxUnpackedSize(data) > maxXLSXUnzip {
			writeErrDetails(w, r, http.StatusRequestEntityTooLarge, "venue_plan_intake.unpacked_too_large",
				"the Excel table unpacks to more than 200 MB, please send a smaller table",
				map[string]any{"file_name": name, "limit_mb": maxXLSXUnzip / mb})
			return f, false
		}
	case isPicture(f.kind):
		_, pw, ph, err := mediastore.PictureConfig(data)
		if err != nil {
			writeErrDetails(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.unreadable_picture",
				"the picture cannot be read, please send it again or in another format",
				map[string]any{"file_name": name})
			return f, false
		}
		if pw*ph > maxPixels {
			writeErrDetails(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.too_many_pixels",
				"the picture has more than 40 megapixels, please send a smaller one",
				map[string]any{"file_name": name, "limit": maxPixels})
			return f, false
		}
		ww, hh := int32(pw), int32(ph) //nolint:gosec // pw*ph <= 40M
		f.width, f.height = &ww, &hh
		if f.kind == venueplan.SourceJPEG {
			clean, _, err := stripJPEGMetadata(data)
			if err != nil {
				writeErrDetails(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.unreadable_picture",
					"the picture cannot be read, please send it again or in another format",
					map[string]any{"file_name": name})
				return f, false
			}
			f.data = clean
		}
	}
	f.needsConversion = needsConversion(f.kind)
	return f, true
}

// storeFile puts the file (and its PNG copy) into storage.
func (h *Handler) storeFile(w http.ResponseWriter, r *http.Request, st *stored, f *incoming) bool {
	ctx := r.Context()
	if needsPNGCopy(f.kind) {
		png, _, _, err := h.media.NormalizeToPNG(ctx, f.data, maxPixels)
		if err != nil {
			writeErrDetails(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.unreadable_picture",
				"the picture cannot be read, please send it again or in another format",
				map[string]any{"file_name": f.name})
			return false
		}
		key, sha, size, err := st.put(ctx, mediastore.OwnerVenuePlanSource, "image/png", png)
		if err != nil {
			h.failed(w, r, "storage write", err)
			return false
		}
		f.pngKey, f.pngSHA, f.pngSize = key, sha, size
	}
	key, sha, _, err := st.put(ctx, mediastore.OwnerVenuePlanSource, contentTypeOf(f.kind), f.data)
	if err != nil {
		h.failed(w, r, "storage write", err)
		return false
	}
	f.key, f.sha = key, sha
	return true
}

// sourceSVGDraft finds the first source SVG that imports cleanly: it becomes
// draft 1 (spec 64 wave 1, section 2.1).
func sourceSVGDraft(files []incoming) (int, json.RawMessage) {
	for i, f := range files {
		if f.kind != venueplan.SourceSVG {
			continue
		}
		g, warnings, planType, errs := checkPlanSVG(f.data)
		if len(errs) > 0 {
			continue
		}
		report, err := json.Marshal(summaryOf(g, planType, warnings))
		if err != nil {
			continue
		}
		return i, report
	}
	return -1, nil
}

// checkPlanSVG imports an SVG and validates it for the plan type its geometry
// fits, like the bot's plan upload.
func checkPlanSVG(raw []byte) (seating.Geometry, []seating.ValidationError, string, seating.ValidationErrors) {
	g, warnings, errs := seating.ImportAnySVG(raw)
	planType := seating.PlanTypeOf(g)
	if len(errs) == 0 {
		errs = seating.ValidateForPlanType(g, planType)
	}
	return g, warnings, planType, errs
}

// createIntake writes the intake, its files and (for a clean source SVG) draft
// 1 in one transaction. created=false: the Idempotency-Key was taken by a
// concurrent request; the returned intake is that one.
func (h *Handler) createIntake(ctx context.Context, r *http.Request, orgID, venueID uuid.UUID, channel venueplan.Channel, idemKey string, files []incoming) (gen.VenuePlanIntakeRow, bool, error) {
	var keyPtr *string
	if idemKey != "" {
		keyPtr = &idemKey
	}
	user := actorUserID(ctx)
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return gen.VenuePlanIntakeRow{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.queries.WithTx(tx)
	in, err := q.InsertVenuePlanIntake(ctx, gen.InsertVenuePlanIntakeParams{
		OrgID: orgID, VenueID: venueID, CreatedBy: user, Channel: string(channel),
		State: string(venueplan.StateReceived), IdempotencyKey: keyPtr,
	})
	if errors.Is(err, pgx.ErrNoRows) && keyPtr != nil {
		_ = tx.Rollback(ctx)
		existing, gerr := h.queries.GetVenuePlanIntakeByIdempotencyKey(ctx, orgID, idemKey)
		return existing, false, gerr
	}
	if err != nil {
		return in, false, err
	}
	backend := h.media.Backend()
	var mediaIDs []uuid.UUID
	var fileMeta []map[string]any
	for i, f := range files {
		obj, err := h.media.InsertTx(ctx, tx, mediastore.InsertInput{
			OrgID: &orgID, OwnerType: mediastore.OwnerVenuePlanSource, OwnerID: &in.ID,
			StorageBackend: backend, StorageKey: f.key, ContentType: contentTypeOf(f.kind),
			ByteSize: int64(len(f.data)), ChecksumSHA256: f.sha, Width: f.width, Height: f.height,
		})
		if err != nil {
			return in, false, err
		}
		var pngID *uuid.UUID
		if f.pngKey != "" {
			pobj, err := h.media.InsertTx(ctx, tx, mediastore.InsertInput{
				OrgID: &orgID, OwnerType: mediastore.OwnerVenuePlanSource, OwnerID: &in.ID,
				StorageBackend: backend, StorageKey: f.pngKey, ContentType: "image/png",
				ByteSize: f.pngSize, ChecksumSHA256: f.pngSHA, Width: f.width, Height: f.height,
			})
			if err != nil {
				return in, false, err
			}
			pngID = &pobj.ID
		}
		if _, err := q.InsertVenuePlanIntakeSource(ctx, gen.InsertVenuePlanIntakeSourceParams{
			IntakeID: in.ID, MediaID: obj.ID, NormalizedMediaID: pngID, Sha256: f.sha, Kind: string(f.kind),
			FileName: f.name, ByteSize: int64(len(f.data)), Pages: f.pages, Width: f.width, Height: f.height,
			NeedsConversion: f.needsConversion, Position: int32(i), //nolint:gosec // at most 10 files
		}); err != nil {
			return in, false, err
		}
		mediaIDs = append(mediaIDs, obj.ID)
		fileMeta = append(fileMeta, map[string]any{"kind": string(f.kind), "sha256": f.sha, "byte_size": len(f.data)})
	}

	from := in.State
	var reason venueplan.EscalationReason
	meta := map[string]any{"files": fileMeta, "channel": string(channel)}
	if i, report := sourceSVGDraft(files); i >= 0 {
		d, err := q.InsertVenuePlanDraft(ctx, gen.InsertVenuePlanDraftParams{
			IntakeID: in.ID, Author: string(venueplan.AuthorSourceSVG), AuthorUserID: user,
			Report: report, SvgMediaID: mediaIDs[i],
		})
		if err != nil {
			return in, false, err
		}
		if in, err = q.SetVenuePlanIntakeCurrentDraft(ctx, in.ID, d.N); err != nil {
			return in, false, err
		}
		meta["draft"] = d.N
		var moved bool
		if in, moved, err = moveState(ctx, q, in, venueplan.StateReview, gen.UpdateVenuePlanIntakeStateParams{}); err != nil || !moved {
			return in, false, errors.Join(err, errors.New("received -> review refused"))
		}
	} else {
		reason = venueplan.EscalationNoGenerator
		var moved bool
		if in, moved, err = moveState(ctx, q, in, venueplan.StateOperator, gen.UpdateVenuePlanIntakeStateParams{
			EscalationReason: reasonPtr(reason),
		}); err != nil || !moved {
			return in, false, errors.Join(err, errors.New("received -> operator refused"))
		}
	}
	meta["state_from"], meta["state_to"] = from, in.State
	if err := h.writeAudit(ctx, tx, r, "v1.venue_plan_intake.create", in, meta); err != nil {
		return in, false, err
	}
	n := h.notify()
	if err := n.IntakeCreated(ctx, tx, in.ID); err != nil {
		return in, false, err
	}
	if reason != "" {
		if err := n.IntakeEscalated(ctx, tx, in.ID, reason); err != nil {
			return in, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return in, false, err
	}
	return in, true, nil
}
