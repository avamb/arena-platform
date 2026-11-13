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
//   - Else received -> generating in a short transaction, and the attempt
//     runs as a chain of short steps, one worker_jobs row each, so no call
//     holds the worker loop longer than one HTTP call:
//   - step submit (no gen_job_id in the payload): POST /generate, then the
//     next row, the step poll, PollDelay later;
//   - step poll: one GET /jobs/{id}. Still running: one more poll row
//     PollDelay later, until Deadline after submitted_at (then the attempt
//     failed with the retryable code "deadline"). A 404 (the generator lost
//     the job) sends the files again once (a submit row with resubmitted); a
//     second 404 is a failed attempt. Done or failed: Decide, and one
//     transaction writes the outcome after checking the intake is still
//     generating.
//
// A generator failure that may pass is retried as a NEW worker_jobs row with
// a later scheduled_at (1 and 5 minutes, MaxAttempts in total). A stopping
// worker (a cancelled context) is not a generator failure: the step returns
// the error and the worker runs the same row again. Every escalation tells
// the administrators (venueplan.Notifier.IntakeEscalated) and leaves an
// organizer notice of kind escalated; a draft in review leaves draft_ready.
// Audit rows v1.venue_plan_intake.generate_started, draft_generated and
// generate_failed carry a system actor (actor_id NULL).
//
// Repeats: every row carries Seq, and every step enqueues its next row with
// Seq+1 under the intake's row lock. A row whose intake already has a
// venue_plan.generate row with a higher Seq is a repeat of a step that was
// done (the worker gave a stale claim back) and does nothing, so a repeat
// never makes a second chain. Every enqueue also moves the intake's
// updated_at, so a living chain never looks stalled.
//
// The sweep (JobTypeSweep, every SweepInterval) sends an intake that stayed
// received or generating for StaleAfter without a step to an administrator
// with generator_failed, the same way as a failed attempt.
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

// JobTypeSweep is the self-scheduling job that sends stalled intakes to an
// administrator.
const JobTypeSweep = "venue_plan.generate_sweep"

// Timing of the steps and of the sweep.
const (
	// PollDelay is the wait before the next step poll.
	PollDelay = 3 * time.Second
	// Deadline is how long the polls of one submit wait for the job.
	Deadline = 4 * time.Minute
	// StaleAfter: an intake received or generating whose updated_at is
	// older is stalled (no step moved it).
	StaleAfter = 30 * time.Minute
	// SweepInterval is the gap between two sweeps.
	SweepInterval = 10 * time.Minute
)

// Error codes the handler gives to a failed attempt itself.
const (
	// CodeDeadline: the job did not finish within Deadline (retryable).
	CodeDeadline = "deadline"
	// CodeStalled: the sweep found the intake without a living step.
	CodeStalled = "stalled"
)

// Generator is the part of the generator client the handler needs
// (*generator.Client): one short HTTP call each.
type Generator interface {
	Submit(ctx context.Context, files []generator.File, opts ...generator.SubmitOption) (string, error)
	Poll(ctx context.Context, jobID string) (generator.Result, bool, error)
}

// Options configures NewHandler and NewSweepHandler.
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
	// PollDelay and Deadline override the defaults (tests).
	PollDelay time.Duration
	Deadline  time.Duration
}

type handler struct {
	pool      *pgxpool.Pool
	media     *mediastore.Repo
	gen       Generator
	notify    venueplan.Notifier
	audit     audit.Writer
	logger    *slog.Logger
	now       func() time.Time
	pollDelay time.Duration
	deadline  time.Duration
}

func newHandler(opts Options) *handler {
	h := &handler{pool: opts.Pool, media: opts.Media, gen: opts.Generator, notify: opts.Notifier,
		audit: opts.Audit, logger: opts.Logger, now: opts.Now, pollDelay: opts.PollDelay, deadline: opts.Deadline}
	if h.notify == nil {
		h.notify = venueplan.NopNotifier{}
	}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	if h.now == nil {
		h.now = time.Now
	}
	if h.pollDelay <= 0 {
		h.pollDelay = PollDelay
	}
	if h.deadline <= 0 {
		h.deadline = Deadline
	}
	return h
}

// NewHandler is the worker handler of venue_plan.generate.
func NewHandler(opts Options) func(context.Context, []byte) error {
	return newHandler(opts).run
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
	if p.Attempt < 1 {
		p.Attempt = 1
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
	sup, err := superseded(ctx, h.pool, id, p.Seq)
	if err != nil {
		return fmt.Errorf("venue plan generate: %w", err)
	}
	if sup {
		h.logger.Info("venue plan generate: a repeated step is skipped",
			slog.String("intake_id", id.String()), slog.Int("attempt", p.Attempt), slog.Int("seq", p.Seq))
		return nil
	}
	if p.GenJobID != "" {
		return h.pollStep(ctx, id, p)
	}
	return h.submitStep(ctx, id, p)
}

// submitStep picks the files, moves the intake to generating, sends the files
// and queues the first poll.
func (h *handler) submitStep(ctx context.Context, id uuid.UUID, p intake.GeneratePayload) error {
	guard := seqGuard(p.Seq)
	if h.gen == nil || h.media == nil {
		return h.finish(ctx, id, p.Attempt, Decision{State: venueplan.StateOperator, Reason: venueplan.EscalationNoGenerator},
			generator.Result{}, nil, nil, guard)
	}
	picked, err := h.candidates(ctx, id)
	if err != nil {
		return err
	}
	if len(picked) == 0 {
		return h.finish(ctx, id, p.Attempt, Decision{State: venueplan.StateOperator, Reason: venueplan.EscalationUnsupportedFormat},
			generator.Result{}, nil, nil, guard)
	}
	started, err := h.start(ctx, id, p, picked)
	if err != nil || !started {
		return err
	}
	files, genErr := h.read(ctx, picked)
	var jobID string
	if genErr == nil {
		var name string
		if name, genErr = h.categoryName(ctx, id); genErr == nil {
			jobID, genErr = h.gen.Submit(ctx, files, generator.WithCategoryName(name))
		}
	}
	if genErr != nil && ctx.Err() != nil {
		// The worker stops: the same row runs again, the attempt is not spent.
		return fmt.Errorf("venue plan generate: submit interrupted: %w", ctx.Err())
	}
	// What follows is written even when the worker is shutting down.
	octx := context.WithoutCancel(ctx)
	if genErr != nil {
		return h.outcome(octx, id, p, generator.Result{}, genErr)
	}
	at := h.now().UTC()
	next := intake.GeneratePayload{IntakeID: p.IntakeID, Attempt: p.Attempt, Seq: p.Seq + 1,
		GenJobID: jobID, SubmittedAt: &at, Resubmitted: p.Resubmitted}
	return h.enqueueNext(octx, id, p.Seq, next, h.pollDelay)
}

// pollStep asks the generator once about the job.
func (h *handler) pollStep(ctx context.Context, id uuid.UUID, p intake.GeneratePayload) error {
	if h.gen == nil {
		return h.finish(ctx, id, p.Attempt, Decision{State: venueplan.StateOperator, Reason: venueplan.EscalationNoGenerator},
			generator.Result{}, nil, nil, seqGuard(p.Seq))
	}
	res, done, pollErr := h.gen.Poll(ctx, p.GenJobID)
	if !done && ctx.Err() != nil {
		return fmt.Errorf("venue plan generate: poll interrupted: %w", ctx.Err())
	}
	octx := context.WithoutCancel(ctx)
	if !done {
		submitted := h.now()
		if p.SubmittedAt != nil {
			submitted = *p.SubmittedAt
		}
		if h.now().Sub(submitted) >= h.deadline {
			return h.outcome(octx, id, p, generator.Result{}, &generator.Error{Code: CodeDeadline,
				Msg: "the job did not finish before the deadline", Retry: true})
		}
		if pollErr != nil {
			h.logger.Warn("venue plan generate: no answer about the job, asking again",
				slog.String("intake_id", id.String()), slog.Int("attempt", p.Attempt), slog.String("error_code", errCode(pollErr)))
		}
		next := p
		next.Seq = p.Seq + 1
		return h.enqueueNext(octx, id, p.Seq, next, h.pollDelay)
	}
	if generator.IsJobLost(pollErr) && !p.Resubmitted {
		// The generator restarted and lost the job: send the files again, once.
		next := intake.GeneratePayload{IntakeID: p.IntakeID, Attempt: p.Attempt, Seq: p.Seq + 1, Resubmitted: true}
		return h.enqueueNext(octx, id, p.Seq, next, 0)
	}
	return h.outcome(octx, id, p, res, pollErr)
}

// outcome turns one finished attempt into the intake's next state (Decide).
func (h *handler) outcome(ctx context.Context, id uuid.UUID, p intake.GeneratePayload, res generator.Result, genErr error) error {
	var report json.RawMessage
	svgValid := false
	if genErr == nil && (res.SVGTooLarge > 0 || res.PreviewTooLarge > 0) {
		// Sizes only, never the content.
		h.logger.Warn("venue plan generate: the generator's answer is over a limit",
			slog.String("intake_id", id.String()), slog.Int("attempt", p.Attempt),
			slog.Int("svg_bytes", res.SVGTooLarge), slog.Int("svg_limit", generator.MaxSVGBytes),
			slog.Int("preview_bytes", res.PreviewTooLarge), slog.Int("preview_limit", generator.MaxPreviewBytes),
			slog.Bool("draft_dropped", res.SVGTooLarge > 0))
	}
	if genErr == nil && res.Result == generator.ResultOK && len(res.SVG) > 0 {
		g, warnings, planType, verrs := intake.CheckPlanSVG(res.SVG)
		if len(verrs) == 0 {
			svgValid = true
			var err error
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
	d := Decide(res, genErr, svgValid, p.Attempt)
	if d.Retry {
		return h.retry(ctx, id, p, genErr)
	}
	return h.finish(ctx, id, p.Attempt, d, res, report, genErr, seqGuard(p.Seq))
}

// superseded reports whether the intake already has a venue_plan.generate row
// with a Seq above seq: the row of seq was handled and this run is a repeat.
func superseded(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id uuid.UUID, seq int) (bool, error) {
	var found bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM worker_jobs
		WHERE job_type = $1 AND payload->>'intake_id' = $2
		  AND COALESCE((payload->>'seq')::int, 0) > $3)`, JobType, id.String(), seq).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("check repeated step: %w", err)
	}
	return found, nil
}

// guard decides, under the intake's row lock, whether an outcome may still be
// written.
type guard func(ctx context.Context, tx pgx.Tx, in gen.VenuePlanIntakeRow) (bool, error)

// seqGuard: the step of seq was not done already.
func seqGuard(seq int) guard {
	return func(ctx context.Context, tx pgx.Tx, in gen.VenuePlanIntakeRow) (bool, error) {
		sup, err := superseded(ctx, tx, in.ID, seq)
		return !sup, err
	}
}

// staleGuard: no step moved the intake since cutoff.
func staleGuard(cutoff time.Time) guard {
	return func(_ context.Context, _ pgx.Tx, in gen.VenuePlanIntakeRow) (bool, error) {
		return in.UpdatedAt.Before(cutoff), nil
	}
}

// enqueueNext queues the next step of a generating intake, unless the
// intake moved on or the step of seq was done already, and moves the
// intake's updated_at (the sweep then sees a living chain).
func (h *handler) enqueueNext(ctx context.Context, id uuid.UUID, seq int, next intake.GeneratePayload, delay time.Duration) error {
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
		if sup, err := superseded(ctx, tx, id, seq); err != nil || sup {
			return err
		}
		if err := intake.EnqueueGenerateStep(ctx, tx, next, delay); err != nil {
			return err
		}
		return touch(ctx, tx, id)
	})
}

func touch(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE venue_plan_intakes SET updated_at = now() WHERE id = $1`, id); err != nil {
		return fmt.Errorf("touch intake: %w", err)
	}
	return nil
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

// categoryNames is the name of the one price category a generated draft
// carries, by language of the organizer (any other language: en).
var categoryNames = map[string]string{"en": "Standard", "ru": "Стандарт", "es": "Estándar"}

// CategoryName returns the category name for a locale such as "ru" or
// "ru-RU"; an unknown or empty locale gives the English name.
func CategoryName(locale string) string {
	l := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	if n, ok := categoryNames[l]; ok {
		return n
	}
	return categoryNames["en"]
}

// categoryName picks the language of the intake's author: the language of the
// author's live Telegram link for a bot intake, else the author's
// preferred_locale, else the organization's default_locale, else en.
func (h *handler) categoryName(ctx context.Context, id uuid.UUID) (string, error) {
	var bot, user, org *string
	err := h.pool.QueryRow(ctx, `
		SELECT CASE WHEN i.channel = $2 THEN
		         (SELECT NULLIF(btrim(l.locale), '') FROM bot_telegram_links l
		           WHERE l.user_id = i.created_by AND l.revoked_at IS NULL
		           ORDER BY l.updated_at DESC, l.telegram_user_id LIMIT 1) END,
		       NULLIF(btrim(u.preferred_locale), ''),
		       NULLIF(btrim(o.default_locale), '')
		FROM venue_plan_intakes i
		LEFT JOIN users u ON u.id = i.created_by
		LEFT JOIN organizations o ON o.id = i.org_id
		WHERE i.id = $1`, id, string(venueplan.ChannelTelegramBot)).Scan(&bot, &user, &org)
	if errors.Is(err, pgx.ErrNoRows) {
		return CategoryName(""), nil
	}
	if err != nil {
		return "", fmt.Errorf("venue plan generate: author language: %w", err)
	}
	for _, l := range []*string{bot, user, org} {
		if l != nil {
			return CategoryName(*l), nil
		}
	}
	return CategoryName(""), nil
}

// start moves received -> generating (a retry finds it generating already).
// started=false: the intake is no longer open. A resubmit of a lost job
// writes no second generate_started.
func (h *handler) start(ctx context.Context, id uuid.UUID, p intake.GeneratePayload, picked []Candidate) (bool, error) {
	attempt := p.Attempt
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
		if p.Resubmitted {
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
func (h *handler) retry(ctx context.Context, id uuid.UUID, p intake.GeneratePayload, cause error) error {
	attempt := p.Attempt
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
		if sup, err := superseded(ctx, tx, id, p.Seq); err != nil || sup {
			return err
		}
		delay := RetryDelays[len(RetryDelays)-1]
		if attempt-1 < len(RetryDelays) {
			delay = RetryDelays[attempt-1]
		}
		next := intake.GeneratePayload{IntakeID: id.String(), Attempt: attempt + 1, Seq: p.Seq + 1}
		if err := intake.EnqueueGenerateStep(ctx, tx, next, delay); err != nil {
			return err
		}
		if err := touch(ctx, tx, id); err != nil {
			return err
		}
		// Only the code: the generator's message may carry text of others.
		h.logger.Warn("venue plan generate: attempt failed, retrying",
			slog.String("intake_id", id.String()), slog.Int("attempt", attempt), slog.String("error_code", errCode(cause)))
		return h.writeAudit(ctx, tx, "v1.venue_plan_intake.generate_failed", in, map[string]any{
			"attempt": attempt, "retry": true, "next_attempt": attempt + 1,
			"retry_in_seconds": int(delay.Seconds()), "error_code": errCode(cause),
		})
	})
}

// finish writes the outcome: the draft (when kept), the state, the notice,
// the administrators' notification and the audit row, in one transaction.
// ok (under the intake's row lock) may refuse the write.
func (h *handler) finish(ctx context.Context, id uuid.UUID, attempt int, d Decision, res generator.Result, report json.RawMessage, cause error, ok guard) error {
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
		if ok != nil {
			if pass, err := ok(ctx, tx, in); err != nil || !pass {
				return err
			}
		}
		from := in.State
		meta := map[string]any{"state_from": from}
		if attempt > 0 {
			meta["attempt"] = attempt
		}
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

// ─── sweep ──────────────────────────────────────────────────────────────────

// NewSweepHandler is the worker handler of JobTypeSweep: it sends every
// stalled intake to an administrator and queues the next sweep, even when the
// run failed.
func NewSweepHandler(opts Options) func(context.Context, []byte) error {
	h := newHandler(opts)
	return func(ctx context.Context, _ []byte) error {
		n, runErr := h.sweep(ctx)
		if _, err := h.pool.Exec(context.WithoutCancel(ctx), `
			INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
			SELECT $1, '{}', 3, 'pending', now() + make_interval(secs => $2)
			WHERE NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status = 'pending')`,
			JobTypeSweep, SweepInterval.Seconds()); err != nil {
			return fmt.Errorf("venue plan generate sweep: schedule next: %w", err)
		}
		if runErr != nil {
			return runErr
		}
		if n > 0 {
			h.logger.Warn("venue plan generate sweep: stalled intakes sent to an administrator", slog.Int("intakes", n))
		}
		return nil
	}
}

// sweep sends every intake that stayed received or generating for StaleAfter
// to an administrator with generator_failed.
func (h *handler) sweep(ctx context.Context) (int, error) {
	cutoff := h.now().Add(-StaleAfter)
	rows, err := h.pool.Query(ctx, `SELECT id FROM venue_plan_intakes
		WHERE state IN ('received', 'generating') AND updated_at < $1
		ORDER BY updated_at LIMIT 100`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("venue plan generate sweep: list: %w", err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("venue plan generate sweep: scan: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("venue plan generate sweep: list: %w", err)
	}
	n := 0
	for _, id := range ids {
		d := Decision{State: venueplan.StateOperator, Reason: venueplan.EscalationGeneratorFailed}
		cause := &generator.Error{Code: CodeStalled, Msg: "no step moved the intake"}
		if err := h.finish(ctx, id, 0, d, generator.Result{}, nil, cause, staleGuard(cutoff)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ScheduleInitialSweep queues the first sweep when none is pending or claimed.
func ScheduleInitialSweep(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', now()
		WHERE NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed'))`,
		JobTypeSweep); err != nil {
		return fmt.Errorf("venue plan generate sweep: enqueue initial: %w", err)
	}
	return nil
}
