package disputes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// JobSendEmail sends an e-mail answer to the provider (and its copy to the
// organization's owner).
const JobSendEmail = "dispute.send_email"

// Refusals of the respond calls (HTTP status is the caller's choice).
var (
	ErrAlreadyAnswered = &ErrInvalid{Code: "dispute.already_submitted", Message: "the dispute was already answered or closed"}
	ErrNoPackage       = &ErrInvalid{Code: "dispute.package_missing", Message: "the evidence package is not built yet"}
	ErrKeyNotWritable  = &ErrInvalid{Code: "dispute.key_not_writable", Message: "the payment key cannot answer disputes; answer in the provider's dashboard with the package"}
	ErrClosedForAnswer = &ErrInvalid{Code: "dispute.closed_for_evidence", Message: "the provider no longer accepts an answer to this dispute"}
	ErrNeedRecipient   = &ErrInvalid{Code: "dispute.recipient_required", Message: "enter the address the answer goes to"}
	ErrTestOutcomeLive = &ErrInvalid{Code: "dispute.test_outcome_live", Message: "a test outcome is only allowed with a test payment key"}
	ErrWrongChannel    = &ErrInvalid{Code: "dispute.wrong_channel", Message: "this action does not apply to this dispute"}
	ErrPackageChanged  = &ErrInvalid{Code: "dispute.package_changed", Message: "the evidence package was rebuilt since you looked at it; open it again before sending"}
	ErrDueInPast       = &ErrInvalid{Code: "dispute.due_in_past", Message: "the deadline is in the past"}
)

// ErrProviderUnavailable: the provider could not be reached and the outcome
// is not known; nothing is recorded as sent.
var ErrProviderUnavailable = errors.New("disputes: the provider did not answer")

// ErrOutcomeUnknown: the answer may have reached the provider and arena could
// not read the dispute back; the card shows the truth once the provider's
// event arrives.
var ErrOutcomeUnknown = errors.New("disputes: the outcome of the answer is not known")

// SubmitInput is the organizer's "Submit".
type SubmitInput struct {
	// TestOutcome ("win" / "lose") makes Stripe's test mode decide the
	// dispute; refused for a live key.
	TestOutcome string
	// Email overrides the letter of an e-mail answer (the organizer may
	// edit it); zero takes the package draft.
	Email *EmailDraft
	// PackageVersion is the version the organizer looked at; 0 sends the
	// latest. A different latest version refuses the send.
	PackageVersion int
}

// Submit sends the answer: through the provider's API for an API channel
// dispute (submit=true, once, irreversible), by e-mail otherwise (a queued
// letter to the provider with the package and a copy to the owner).
func (s *Service) Submit(ctx context.Context, orgID, id uuid.UUID, in SubmitInput, actor Actor) (Dispute, error) {
	d, err := s.Get(ctx, orgID, id)
	if err != nil {
		return Dispute{}, err
	}
	if !d.Open() {
		return Dispute{}, ErrAlreadyAnswered
	}
	pkg, err := s.LatestPackage(ctx, orgID, id)
	if err != nil {
		return Dispute{}, err
	}
	if pkg == nil {
		return Dispute{}, ErrNoPackage
	}
	if in.PackageVersion > 0 && in.PackageVersion != pkg.Version {
		return Dispute{}, ErrPackageChanged
	}
	if d.Channel != ChannelAPI {
		return s.queueEmail(ctx, d, pkg, in.Email, actor)
	}
	resp, err := s.responder(ctx, d)
	if err != nil || resp == nil || d.ProviderDisputeID == nil {
		return Dispute{}, ErrKeyNotWritable
	}
	extra := map[string]string{}
	suffix := ""
	switch in.TestOutcome {
	case "":
	case "win", "lose":
		live, merr := s.configIsLive(ctx, d)
		if merr != nil {
			return Dispute{}, merr
		}
		if live {
			return Dispute{}, ErrTestOutcomeLive
		}
		extra["uncategorized_text"] = map[string]string{"win": "winning_evidence", "lose": "losing_evidence"}[in.TestOutcome]
		suffix = "-" + in.TestOutcome
	default:
		return Dispute{}, &ErrInvalid{Code: "dispute.invalid_test_outcome", Message: "test_outcome must be win or lose"}
	}
	pd, serr := s.saveDraft(ctx, d, pkg, resp, extra, true, suffix)
	if serr != nil {
		switch {
		case errors.Is(serr, payments.ErrDisputeNotWritable):
			_, _ = insertEvent(ctx, s.db, d, "submit_failed", "", "", actor, map[string]any{"code": "key_not_writable"})
			return Dispute{}, ErrKeyNotWritable
		}
		// The outcome is unknown or the provider refused: read the dispute
		// back and let its own state decide. The read gets its own time: the
		// send may have used the whole budget.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.callTimeout)
		fresh, gerr := resp.GetDispute(cctx, *d.ProviderDisputeID)
		cancel()
		if gerr != nil && !errors.Is(serr, payments.ErrDisputeClosedForEvidence) {
			_, _ = insertEvent(ctx, s.db, d, "submit_unknown", "", "", actor, map[string]any{"code": draftErrorCode(serr)})
			s.logger.Warn("disputes: submit outcome unknown", slog.String("dispute_id", d.ID.String()), slog.String("error", serr.Error()))
			return Dispute{}, ErrOutcomeUnknown
		}
		if gerr != nil || fresh.SubmissionCount == 0 {
			_, _ = insertEvent(ctx, s.db, d, "submit_failed", "", "", actor, map[string]any{"code": draftErrorCode(serr)})
			if errors.Is(serr, payments.ErrDisputeClosedForEvidence) {
				return Dispute{}, ErrClosedForAnswer
			}
			s.logger.Warn("disputes: submit failed", slog.String("dispute_id", d.ID.String()), slog.String("error", serr.Error()))
			return Dispute{}, ErrProviderUnavailable
		}
		pd = fresh
	}
	return s.markSubmitted(ctx, d, actor, pd.Status, map[string]any{"version": pkg.Version, "test_outcome": in.TestOutcome})
}

func (s *Service) markSubmitted(ctx context.Context, d Dispute, actor Actor, providerStatus string, payload map[string]any) (Dispute, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Dispute{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	cur, err := getDispute(ctx, tx, d.OrgID, d.ID, true)
	if err != nil {
		return Dispute{}, err
	}
	state := StateSubmitted
	if cur.State == StateUnderReview || cur.State == StateClosed {
		state = cur.State
	}
	if _, err := tx.Exec(ctx, `UPDATE payment_disputes SET state = $2, submitted_at = COALESCE(submitted_at, now()),
			submitted_by = COALESCE(submitted_by, $3), provider_status = CASE WHEN $4 <> '' THEN $4 ELSE provider_status END,
			updated_at = now() WHERE id = $1`, d.ID, state, actor.UserID, providerStatus); err != nil {
		return Dispute{}, err
	}
	if _, err := insertEvent(ctx, tx, cur, "submitted", "", providerStatus, actor, payload); err != nil {
		return Dispute{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Dispute{}, err
	}
	return s.Get(ctx, d.OrgID, d.ID)
}

// ConfigIsTest reports whether the dispute's payment configuration is a
// test one (the card then offers Stripe's test outcomes).
func (s *Service) ConfigIsTest(ctx context.Context, d Dispute) bool {
	live, err := s.configIsLive(ctx, d)
	return err == nil && !live
}

func (s *Service) configIsLive(ctx context.Context, d Dispute) (bool, error) {
	if d.ConfigID == nil {
		return true, nil
	}
	var mode string
	err := s.db.QueryRow(ctx, `SELECT COALESCE(mode,'live') FROM payment_provider_configs WHERE id = $1 AND org_id = $2`, *d.ConfigID, d.OrgID).Scan(&mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	return mode != "test", nil
}

// emailJob is the payload of dispute.send_email.
type emailJob struct {
	DisputeID string `json:"dispute_id"`
	PackageID string `json:"package_id"`
	To        string `json:"to"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	ActorID   string `json:"actor_id,omitempty"`
}

func validEmail(s string) bool {
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	return err == nil && a.Address == strings.TrimSpace(s) && strings.Contains(s, "@")
}

func (s *Service) queueEmail(ctx context.Context, d Dispute, pkg *Package, override *EmailDraft, actor Actor) (Dispute, error) {
	draft := EmailDraft{}
	if pkg.EmailDraft != nil {
		draft = *pkg.EmailDraft
	}
	// The address the organizer entered after the build (from the
	// provider's letter) wins over the one frozen in the draft.
	if v := strings.TrimSpace(d.RespondTo); v != "" {
		draft.To = v
	}
	bodyEdited := false
	if override != nil {
		if strings.TrimSpace(override.To) != "" {
			draft.To = strings.TrimSpace(override.To)
		}
		if strings.TrimSpace(override.Subject) != "" {
			draft.Subject = strings.TrimSpace(override.Subject)
		}
		if strings.TrimSpace(override.Body) != "" && (pkg.EmailDraft == nil || override.Body != pkg.EmailDraft.Body) {
			draft.Body = override.Body
			bodyEdited = true
		}
	}
	if draft.To == "" {
		draft.To = s.respondTo(d)
	}
	if !validEmail(draft.To) {
		return Dispute{}, ErrNeedRecipient
	}
	if len([]rune(draft.Subject)) > 300 || len([]rune(draft.Body)) > 20000 {
		return Dispute{}, &ErrInvalid{Code: "dispute.email_too_long", Message: "the subject or the text is too long"}
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Dispute{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	cur, err := getDispute(ctx, tx, d.OrgID, d.ID, true)
	if err != nil {
		return Dispute{}, err
	}
	if !cur.Open() {
		return Dispute{}, ErrAlreadyAnswered
	}
	// The letter text stays in the package; the job carries it only when the
	// organizer edited it, so the queue holds no copy of the buyer's data.
	job := emailJob{DisputeID: d.ID.String(), PackageID: pkg.ID.String(), To: draft.To, Subject: draft.Subject}
	if bodyEdited {
		job.Body = draft.Body
	}
	if actor.UserID != nil {
		job.ActorID = actor.UserID.String()
	}
	if _, err := worker.EnqueueInTx(ctx, tx, JobSendEmail, job, 5); err != nil {
		return Dispute{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE payment_disputes SET state = $2, submitted_at = now(), submitted_by = $3,
			respond_to = CASE WHEN respond_to = '' THEN $4 ELSE respond_to END, updated_at = now() WHERE id = $1`,
		d.ID, StateSubmitted, actor.UserID, draft.To); err != nil {
		return Dispute{}, err
	}
	if _, err := insertEvent(ctx, tx, cur, "email_queued", "", "", actor, map[string]any{
		"to": draft.To, "subject": draft.Subject, "version": pkg.Version,
	}); err != nil {
		return Dispute{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Dispute{}, err
	}
	return s.Get(ctx, d.OrgID, d.ID)
}

// Accept gives the dispute up: the money stays with the buyer. Through the
// provider's API for an API channel dispute; recorded only otherwise.
func (s *Service) Accept(ctx context.Context, orgID, id uuid.UUID, actor Actor) (Dispute, error) {
	d, err := s.Get(ctx, orgID, id)
	if err != nil {
		return Dispute{}, err
	}
	if !d.Open() {
		return Dispute{}, ErrAlreadyAnswered
	}
	status := ""
	if d.Channel == ChannelAPI && d.ProviderDisputeID != nil {
		resp, rerr := s.responder(ctx, d)
		if rerr != nil || resp == nil {
			return Dispute{}, ErrKeyNotWritable
		}
		cctx, cancel := context.WithTimeout(ctx, s.callTimeout)
		pd, cerr := resp.CloseDispute(cctx, *d.ProviderDisputeID, fmt.Sprintf("arena-dispute-%s-close", d.ID))
		cancel()
		if cerr != nil {
			_, _ = insertEvent(ctx, s.db, d, "accept_failed", "", "", actor, map[string]any{"code": draftErrorCode(cerr)})
			switch {
			case errors.Is(cerr, payments.ErrDisputeNotWritable):
				return Dispute{}, ErrKeyNotWritable
			case errors.Is(cerr, payments.ErrDisputeClosedForEvidence):
				return Dispute{}, ErrClosedForAnswer
			}
			return Dispute{}, ErrProviderUnavailable
		}
		status = pd.Status
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Dispute{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	cur, err := getDispute(ctx, tx, orgID, id, true)
	if err != nil {
		return Dispute{}, err
	}
	if !cur.Open() {
		// A send or a provider close won the race: never overwrite it.
		return Dispute{}, ErrAlreadyAnswered
	}
	if _, err := tx.Exec(ctx, `UPDATE payment_disputes SET state = 'accepted', outcome = 'accepted',
			closed_at = COALESCE(closed_at, now()), accepted_at = now(), accepted_by = $2,
			provider_status = CASE WHEN $3 <> '' THEN $3 ELSE provider_status END, updated_at = now()
		WHERE id = $1`, id, actor.UserID, status); err != nil {
		return Dispute{}, err
	}
	if _, err := insertEvent(ctx, tx, cur, "accepted", "", status, actor, nil); err != nil {
		return Dispute{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Dispute{}, err
	}
	return s.Get(ctx, orgID, id)
}

// DeadlineInput is the deadline and address from a provider's letter.
type DeadlineInput struct {
	DueBy     *time.Time
	RespondTo *string
}

// SetDeadline records the deadline (and the answer address) the organizer
// read in the provider's letter. An API dispute keeps the provider's own
// deadline.
func (s *Service) SetDeadline(ctx context.Context, orgID, id uuid.UUID, in DeadlineInput, actor Actor) (Dispute, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Dispute{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	d, err := getDispute(ctx, tx, orgID, id, true)
	if err != nil {
		return Dispute{}, err
	}
	if d.Channel == ChannelAPI && in.DueBy != nil {
		return Dispute{}, ErrWrongChannel
	}
	if in.DueBy != nil && !in.DueBy.After(s.now()) {
		return Dispute{}, ErrDueInPast
	}
	// A letter dispute that expired on a deadline the organizer can still
	// correct is reopened by a new, later deadline.
	reopen := d.State == StateExpired && d.Channel != ChannelAPI && d.SubmittedAt == nil && in.DueBy != nil
	if !d.Open() && !reopen {
		return Dispute{}, ErrAlreadyAnswered
	}
	if in.RespondTo != nil {
		v := strings.TrimSpace(*in.RespondTo)
		if v != "" && (!validEmail(v) || len(v) > 320) {
			return Dispute{}, ErrNeedRecipient
		}
		in.RespondTo = &v
	}
	if _, err := tx.Exec(ctx, `UPDATE payment_disputes SET
			due_by = COALESCE($2, due_by),
			due_by_source = CASE WHEN $2::timestamptz IS NOT NULL THEN 'organizer' ELSE due_by_source END,
			respond_to = COALESCE($3, respond_to),
			state = CASE WHEN $4 THEN (CASE WHEN EXISTS (SELECT 1 FROM payment_dispute_packages p WHERE p.dispute_id = $1)
			                                THEN 'draft_ready' ELSE 'needs_response' END) ELSE state END,
			outcome = CASE WHEN $4 THEN NULL ELSE outcome END,
			closed_at = CASE WHEN $4 THEN NULL ELSE closed_at END,
			updated_at = now()
		WHERE id = $1`, id, in.DueBy, in.RespondTo, reopen); err != nil {
		return Dispute{}, err
	}
	if in.DueBy != nil {
		// Reminders follow the new deadline.
		if _, err := tx.Exec(ctx, `DELETE FROM payment_dispute_notices WHERE dispute_id = $1 AND (notice_key LIKE 'due_%' OR notice_key = 'expired')`, id); err != nil {
			return Dispute{}, err
		}
	}
	payload := map[string]any{}
	if in.DueBy != nil {
		payload["due_by"] = in.DueBy.UTC().Format(time.RFC3339)
	}
	if in.RespondTo != nil {
		payload["respond_to_set"] = *in.RespondTo != ""
	}
	if reopen {
		payload["reopened"] = true
	}
	if _, err := insertEvent(ctx, tx, d, "deadline_set", "", "", actor, payload); err != nil {
		return Dispute{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Dispute{}, err
	}
	return s.Get(ctx, orgID, id)
}

// MarkSentByOrganizer records that the organizer sent the package to the
// provider from their own mailbox.
func (s *Service) MarkSentByOrganizer(ctx context.Context, orgID, id uuid.UUID, actor Actor) (Dispute, error) {
	d, err := s.Get(ctx, orgID, id)
	if err != nil {
		return Dispute{}, err
	}
	if d.Channel == ChannelAPI {
		return Dispute{}, ErrWrongChannel
	}
	if !d.Open() {
		return Dispute{}, ErrAlreadyAnswered
	}
	return s.markSubmittedEvent(ctx, d, actor, "sent_by_organizer")
}

func (s *Service) markSubmittedEvent(ctx context.Context, d Dispute, actor Actor, kind string) (Dispute, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Dispute{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	cur, err := getDispute(ctx, tx, d.OrgID, d.ID, true)
	if err != nil {
		return Dispute{}, err
	}
	if !cur.Open() {
		return Dispute{}, ErrAlreadyAnswered
	}
	if _, err := tx.Exec(ctx, `UPDATE payment_disputes SET state = 'submitted', submitted_at = now(), submitted_by = $2,
			updated_at = now() WHERE id = $1`, d.ID, actor.UserID); err != nil {
		return Dispute{}, err
	}
	if _, err := insertEvent(ctx, tx, cur, kind, "", "", actor, nil); err != nil {
		return Dispute{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Dispute{}, err
	}
	return s.Get(ctx, d.OrgID, d.ID)
}
