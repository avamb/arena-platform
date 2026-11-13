// Package generate is the arena-worker handler of the job venue_plan.generate
// (spec 08_architecture/64 wave 2, section 4.4): it sends the files of an
// intake to the venue SVG generator and turns the answer into the intake's
// next state.
//
//   - The intake is not received or generating (cancelled meanwhile, already
//     done): nothing happens.
//   - No generator (VENUE_GEN_URL or VENUE_GEN_TOKEN empty) or no media store:
//     received -> operator, reason no_generator.
//   - No file the generator can take: -> operator, unsupported_format.
//   - Else received -> generating in a short transaction, the generator is
//     called OUTSIDE any transaction, and one transaction writes the outcome
//     (Decide) after checking the intake is still generating.
//
// A generator failure that may pass is retried as a NEW worker_jobs row with
// a later scheduled_at (1 and 5 minutes, MaxAttempts in total). Every
// escalation tells the administrators (venueplan.Notifier.IntakeEscalated)
// and leaves an organizer notice of kind escalated; a draft in review leaves
// draft_ready. Audit rows v1.venue_plan_intake.generate_started,
// draft_generated and generate_failed carry a system actor (actor_id NULL).
package generate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/generator"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/intake"
)

// JobType is the job this package handles.
const JobType = intake.GenerateJobType

// Generator is the part of the generator client the handler needs
// (*generator.Client).
type Generator interface {
	Generate(ctx context.Context, files []generator.File) (generator.Result, error)
}

// Options configures NewHandler.
type Options struct {
	Pool *pgxpool.Pool
	// Media reads the sources and stores the draft. nil = no_generator.
	Media *mediastore.Repo
	// Generator is nil when the worker has no generator configured.
	Generator Generator
	// Notifier tells the administrators of an escalation; nil = nobody.
	Notifier venueplan.Notifier
	Audit    audit.Writer
	Logger   *slog.Logger
	Now      func() time.Time
}

type handler struct {
	pool   *pgxpool.Pool
	media  *mediastore.Repo
	gen    Generator
	notify venueplan.Notifier
	audit  audit.Writer
	logger *slog.Logger
	now    func() time.Time
}

// NewHandler is the worker handler of venue_plan.generate.
func NewHandler(opts Options) func(context.Context, []byte) error {
	h := &handler{pool: opts.Pool, media: opts.Media, gen: opts.Generator, notify: opts.Notifier,
		audit: opts.Audit, logger: opts.Logger, now: opts.Now}
	if h.notify == nil {
		h.notify = venueplan.NopNotifier{}
	}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	if h.now == nil {
		h.now = time.Now
	}
	return h.run
}

func (h *handler) run(ctx context.Context, raw []byte) error {
	var p intake.GeneratePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		h.logger.Error("venue plan generate: bad payload, the job is dropped", slog.String("error", err.Error()))
		return nil
	}
	id, err := uuid.Parse(p.IntakeID)
	if err != nil {
		h.logger.Error("venue plan generate: payload without an intake id, the job is dropped")
		return nil
	}
	attempt := p.Attempt
	if attempt < 1 {
		attempt = 1
	}
	in, err := gen.New(h.pool).GetVenuePlanIntakeByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("venue plan generate: read intake: %w", err)
	}
	if !open(in.State) {
		return nil
	}
	if h.gen == nil || h.media == nil {
		return h.finish(ctx, id, attempt, Decision{State: venueplan.StateOperator, Reason: venueplan.EscalationNoGenerator},
			generator.Result{}, nil, nil)
	}
	picked, err := h.candidates(ctx, id)
	if err != nil {
		return err
	}
	if len(picked) == 0 {
		return h.finish(ctx, id, attempt, Decision{State: venueplan.StateOperator, Reason: venueplan.EscalationUnsupportedFormat},
			generator.Result{}, nil, nil)
	}
	started, err := h.start(ctx, id, attempt, picked)
	if err != nil || !started {
		return err
	}

	var res generator.Result
	files, genErr := h.read(ctx, picked)
	if genErr == nil {
		res, genErr = h.gen.Generate(ctx, files)
	}
	var report json.RawMessage
	svgValid := false
	if genErr == nil && res.Result == generator.ResultOK && len(res.SVG) > 0 {
		g, warnings, planType, verrs := intake.CheckPlanSVG(res.SVG)
		if len(verrs) == 0 {
			svgValid = true
			report, err = json.Marshal(draftReport{
				Summary:          intake.SummaryOf(g, planType, warnings),
				ValidationStatus: res.ValidationStatus,
				Files:            res.Files,
			})
			if err != nil {
				return fmt.Errorf("venue plan generate: report: %w", err)
			}
		}
	}
	d := Decide(res, genErr, svgValid, attempt)
	// The outcome is written even when the worker is shutting down.
	octx := context.WithoutCancel(ctx)
	if d.Retry {
		return h.retry(octx, id, attempt, genErr)
	}
	return h.finish(octx, id, attempt, d, res, report, genErr)
}

// draftReport is venue_plan_drafts.report of a generated draft: the summary
// the card shows, and what the generator said.
type draftReport struct {
	intake.Summary
	ValidationStatus string                 `json:"validation_status"`
	Files            []generator.FileReport `json:"generator_files"`
}

func open(state string) bool {
	return state == string(venueplan.StateReceived) || state == string(venueplan.StateGenerating)
}

// candidates lists the sources the generator can take, within its limits.
func (h *handler) candidates(ctx context.Context, id uuid.UUID) ([]Candidate, error) {
	sources, err := gen.New(h.pool).ListVenuePlanIntakeSources(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("venue plan generate: sources: %w", err)
	}
	var all []Candidate
	for _, s := range sources {
		kind, usePNG, ok := SendKind(venueplan.SourceKind(s.Kind), s.NormalizedMediaID != nil)
		if !ok {
			continue
		}
		mediaID, name := s.MediaID, s.FileName
		if usePNG {
			mediaID = *s.NormalizedMediaID
			name = strings.TrimSuffix(name, path.Ext(name)) + ".png"
		}
		obj, err := h.media.GetByID(ctx, mediaID)
		if errors.Is(err, mediastore.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("venue plan generate: source media: %w", err)
		}
		all = append(all, Candidate{Position: int(s.Position), Name: name, Send: kind,
			StorageKey: obj.StorageKey, ContentType: obj.ContentType, Size: obj.ByteSize})
	}
	return Select(all), nil
}

// read loads the bytes of the picked files. An error is retried like a
// generator failure.
func (h *handler) read(ctx context.Context, picked []Candidate) ([]generator.File, error) {
	out := make([]generator.File, 0, len(picked))
	for _, c := range picked {
		obj, err := h.media.Storage().Get(ctx, c.StorageKey)
		if err != nil {
			return nil, fmt.Errorf("venue plan generate: read source: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(obj.Body, generator.MaxFileBytes+1))
		_ = obj.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("venue plan generate: read source: %w", err)
		}
		out = append(out, generator.File{Name: c.Name, Kind: c.Send, ContentType: c.ContentType, Data: data})
	}
	return out, nil
}

// start moves received -> generating (a retry finds it generating already).
// started=false: the intake is no longer open.
func (h *handler) start(ctx context.Context, id uuid.UUID, attempt int, picked []Candidate) (bool, error) {
	started := false
	err := h.tx(ctx, func(tx pgx.Tx, q *gen.Queries) error {
		in, err := q.LockVenuePlanIntake(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		switch in.State {
		case string(venueplan.StateGenerating):
			started = true
		case string(venueplan.StateReceived):
			out, moved, err := intake.MoveState(ctx, q, in, venueplan.StateGenerating, gen.UpdateVenuePlanIntakeStateParams{})
			if err != nil || !moved {
				return err
			}
			in, started = out, true
		default:
			return nil
		}
		kinds := make([]string, 0, len(picked))
		for _, c := range picked {
			kinds = append(kinds, string(c.Send))
		}
		return h.writeAudit(ctx, tx, "v1.venue_plan_intake.generate_started", in, map[string]any{
			"attempt": attempt, "files": len(picked), "kinds": kinds,
		})
	})
	return started, err
}

// retry queues the next attempt; the intake stays generating.
func (h *handler) retry(ctx context.Context, id uuid.UUID, attempt int, cause error) error {
	return h.tx(ctx, func(tx pgx.Tx, q *gen.Queries) error {
		in, err := q.LockVenuePlanIntake(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if in.State != string(venueplan.StateGenerating) {
			return nil
		}
		delay := RetryDelays[len(RetryDelays)-1]
		if attempt-1 < len(RetryDelays) {
			delay = RetryDelays[attempt-1]
		}
		if err := intake.EnqueueGenerate(ctx, tx, id, attempt+1, delay); err != nil {
			return err
		}
		h.logger.Warn("venue plan generate: attempt failed, retrying",
			slog.String("intake_id", id.String()), slog.Int("attempt", attempt), slog.String("error", errText(cause)))
		return h.writeAudit(ctx, tx, "v1.venue_plan_intake.generate_failed", in, map[string]any{
			"attempt": attempt, "retry": true, "next_attempt": attempt + 1,
			"retry_in_seconds": int(delay.Seconds()), "error_code": errCode(cause),
		})
	})
}

// finish writes the outcome: the draft (when kept), the state, the notice,
// the administrators' notification and the audit row, in one transaction.
func (h *handler) finish(ctx context.Context, id uuid.UUID, attempt int, d Decision, res generator.Result, report json.RawMessage, cause error) error {
	var keys []string
	drop := func() {
		for _, k := range keys {
			_ = h.media.Storage().Delete(context.WithoutCancel(ctx), k)
		}
	}
	type storedObj struct {
		key, sha string
		size     int64
	}
	var svgObj, pngObj *storedObj
	if d.KeepDraft {
		put := func(ct string, data []byte) (*storedObj, error) {
			key, err := mediastore.NewStorageKey(mediastore.OwnerVenuePlanSource)
			if err != nil {
				return nil, err
			}
			sha, size, err := h.media.PutAndStream(ctx, key, ct, bytes.NewReader(data))
			if err != nil {
				return nil, err
			}
			keys = append(keys, key)
			return &storedObj{key: key, sha: sha, size: size}, nil
		}
		var err error
		if svgObj, err = put("image/svg+xml", res.SVG); err != nil {
			drop()
			return fmt.Errorf("venue plan generate: store draft: %w", err)
		}
		if len(res.PreviewPNG) > 0 {
			if pngObj, err = put("image/png", res.PreviewPNG); err != nil {
				drop()
				return fmt.Errorf("venue plan generate: store preview: %w", err)
			}
		}
	}
	written := false
	err := h.tx(ctx, func(tx pgx.Tx, q *gen.Queries) error {
		in, err := q.LockVenuePlanIntake(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !open(in.State) {
			// Cancelled (or handled) meanwhile: the outcome is not written.
			return nil
		}
		from := in.State
		meta := map[string]any{"attempt": attempt, "state_from": from}
		if res.JobID != "" {
			meta["generator_job_id"] = res.JobID
			meta["result"] = res.Result
			meta["validation_status"] = res.ValidationStatus
		}
		if res.GeneratorVersion != "" {
			meta["generator_version"] = json.RawMessage(res.GeneratorVersion)
		}
		if cause != nil {
			meta["error_code"] = errCode(cause)
		}
		if d.KeepDraft {
			svg, err := h.media.InsertTx(ctx, tx, mediastore.InsertInput{
				OrgID: &in.OrgID, OwnerType: mediastore.OwnerVenuePlanSource, OwnerID: &in.ID,
				StorageBackend: h.media.Backend(), StorageKey: svgObj.key, ContentType: "image/svg+xml",
				ByteSize: svgObj.size, ChecksumSHA256: svgObj.sha,
			})
			if err != nil {
				return err
			}
			var previewID *uuid.UUID
			if pngObj != nil {
				p, err := h.media.InsertTx(ctx, tx, mediastore.InsertInput{
					OrgID: &in.OrgID, OwnerType: mediastore.OwnerVenuePlanSource, OwnerID: &in.ID,
					StorageBackend: h.media.Backend(), StorageKey: pngObj.key, ContentType: "image/png",
					ByteSize: pngObj.size, ChecksumSHA256: pngObj.sha,
				})
				if err != nil {
					return err
				}
				previewID = &p.ID
			}
			var version *string
			if res.GeneratorVersion != "" {
				version = &res.GeneratorVersion
			}
			dr, err := q.InsertVenuePlanDraft(ctx, gen.InsertVenuePlanDraftParams{
				IntakeID: in.ID, Author: string(venueplan.AuthorGenerator), GeneratorVersion: version,
				Model: res.Model, Report: report, SvgMediaID: svg.ID, PreviewMediaID: previewID,
			})
			if err != nil {
				return err
			}
			if in, err = q.SetVenuePlanIntakeCurrentDraft(ctx, in.ID, dr.N); err != nil {
				return err
			}
			meta["draft"] = dr.N
			meta["svg_sha256"] = svgObj.sha
		}
		p := gen.UpdateVenuePlanIntakeStateParams{}
		if d.State == venueplan.StateOperator {
			r := string(d.Reason)
			p.EscalationReason = &r
			meta["reason"] = r
		}
		out, moved, err := intake.MoveState(ctx, q, in, d.State, p)
		if err != nil {
			return err
		}
		if !moved {
			return fmt.Errorf("venue plan generate: %s -> %s refused", in.State, d.State)
		}
		meta["state_to"] = out.State
		notice := venueplan.NoticeDraftReady
		if d.State == venueplan.StateOperator {
			notice = venueplan.NoticeEscalated
			if err := h.notify.IntakeEscalated(ctx, tx, out.ID, d.Reason); err != nil {
				return err
			}
		}
		if _, err := q.InsertVenuePlanNotice(ctx, out.ID, string(notice), nil); err != nil {
			return err
		}
		action := "v1.venue_plan_intake.generate_failed"
		if d.KeepDraft {
			action = "v1.venue_plan_intake.draft_generated"
		}
		if err := h.writeAudit(ctx, tx, action, out, meta); err != nil {
			return err
		}
		written = true
		return nil
	})
	if err != nil || !written {
		drop()
	}
	if err == nil && written {
		h.logger.Info("venue plan generate: outcome written", slog.String("intake_id", id.String()),
			slog.Int("attempt", attempt), slog.String("state", string(d.State)), slog.String("reason", string(d.Reason)))
	}
	return err
}

func (h *handler) tx(ctx context.Context, fn func(pgx.Tx, *gen.Queries) error) error {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("venue plan generate: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(tx, gen.New(tx)); err != nil {
		return fmt.Errorf("venue plan generate: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("venue plan generate: commit: %w", err)
	}
	return nil
}

// writeAudit records one step with a system actor: the worker acts for no
// person, so actor_id stays NULL.
func (h *handler) writeAudit(ctx context.Context, tx pgx.Tx, action string, in gen.VenuePlanIntakeRow, meta map[string]any) error {
	if h.audit == nil {
		return nil
	}
	meta["org_id"] = in.OrgID.String()
	meta["venue_id"] = in.VenueID.String()
	return h.audit.WriteTx(ctx, tx, audit.Event{
		OccurredAt:   h.now().UTC(),
		ActorType:    "system",
		Action:       action,
		ResourceType: "venue_plan_intake",
		ResourceID:   in.ID.String(),
		Metadata:     meta,
	})
}

func errCode(err error) string {
	var ge *generator.Error
	if errors.As(err, &ge) {
		return ge.Code
	}
	if err != nil {
		return "internal"
	}
	return ""
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
