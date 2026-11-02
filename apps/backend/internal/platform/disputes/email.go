package disputes

// email.go — the e-mail answer (channel email / manual): arena-worker sends
// the organizer's letter with the package to the provider, Reply-To the
// owner, and a copy to the owner (owner decision 2026-11-02). Each send is
// written to the dispute's journal with what left (recipient, subject,
// attachment hashes) — this is not a buyer letter, so email_letters is not
// used.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// OwnerEmails returns the account e-mails of the organization's owners
// (the contact form's owner rule: active org_admin, no platform role).
func OwnerEmails(ctx context.Context, q querier, orgID uuid.UUID, withManagers bool) ([]string, error) {
	roles := []string{"org_admin"}
	if withManagers {
		roles = append(roles, "organizer")
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT u.email FROM memberships m JOIN users u ON u.id = m.user_id
		JOIN organizations o ON o.id = m.org_id
		WHERE o.id = $1 AND m.role = ANY($2) AND m.status = 'active'
		  AND u.deactivated_at IS NULL AND u.anonymized_at IS NULL AND btrim(u.email) <> ''
		  AND NOT EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
		                  WHERE ur.user_id = u.id AND `+gen.PlatformRoleRowSQL+`)
		ORDER BY u.email`, orgID, roles)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EmailJobHandler is the worker handler of dispute.send_email.
func (s *Service) EmailJobHandler(sender email.Sender) func(ctx context.Context, payload []byte) error {
	return func(ctx context.Context, payload []byte) error {
		if sender == nil {
			return errors.New("disputes: no e-mail sender configured")
		}
		var j emailJob
		if err := json.Unmarshal(payload, &j); err != nil {
			return fmt.Errorf("disputes: email payload: %w", err)
		}
		disputeID, err := uuid.Parse(j.DisputeID)
		if err != nil {
			return fmt.Errorf("disputes: email payload: %w", err)
		}
		pkgID, err := uuid.Parse(j.PackageID)
		if err != nil {
			return fmt.Errorf("disputes: email payload: %w", err)
		}
		d, err := getDisputeAnyOrg(ctx, s.db, disputeID, false)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		// One letter per queued send: a retry after a sent letter stops here.
		var already bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM payment_dispute_events
			WHERE dispute_id = $1 AND kind = 'email_sent' AND payload->>'package_id' = $2)`, d.ID, pkgID.String()).Scan(&already); err != nil {
			return err
		}
		if already {
			return nil
		}
		files, err := packageFiles(ctx, s.db, pkgID)
		if err != nil {
			return err
		}
		var atts []email.Attachment
		var digests []map[string]any
		for _, kind := range []string{FileReceipt, FileService, FileCommunication, FilePolicy} {
			f, ok := files[kind]
			if !ok {
				continue
			}
			atts = append(atts, email.Attachment{Filename: f.Filename, ContentType: "application/pdf", Data: f.Content})
			sum := sha256.Sum256(f.Content)
			digests = append(digests, map[string]any{"filename": f.Filename, "bytes": len(f.Content), "sha256": hex.EncodeToString(sum[:])})
		}
		owners, err := OwnerEmails(ctx, s.db, d.OrgID, false)
		if err != nil {
			return err
		}
		replyTo := ""
		if len(owners) > 0 {
			replyTo = owners[0]
		}
		msg := email.Message{
			To: j.To, Subject: j.Subject, ReplyTo: replyTo,
			TextBody: j.Body, HTMLBody: plainToHTML(j.Body), Attachments: atts,
		}
		if err := sender.Send(ctx, msg); err != nil {
			return fmt.Errorf("disputes: send the answer: %w", err)
		}
		bodySum := sha256.Sum256([]byte(j.Body))
		var actor Actor
		if id, perr := uuid.Parse(j.ActorID); perr == nil {
			actor.UserID = &id
		}
		if _, err := insertEvent(ctx, s.db, d, "email_sent", "", "", actor, map[string]any{
			"package_id": pkgID.String(), "to": j.To, "subject": j.Subject, "reply_to_set": replyTo != "",
			"body_sha256": hex.EncodeToString(bodySum[:]), "attachments": digests,
		}); err != nil {
			s.logger.Warn("disputes: the answer went out but was not journaled", "dispute_id", d.ID.String(), "error", err.Error())
		}
		// The copy for the owner; a failure here does not resend the answer.
		if replyTo != "" {
			cp := msg
			cp.To = replyTo
			cp.ReplyTo = ""
			cp.Subject = "[Copy] " + j.Subject
			cp.TextBody = "Copy of the chargeback answer sent to " + j.To + ".\n\n" + j.Body
			cp.HTMLBody = plainToHTML(cp.TextBody)
			if err := sender.Send(ctx, cp); err != nil {
				s.logger.Warn("disputes: copy of the answer not sent", "dispute_id", d.ID.String(), "error", err.Error())
			}
		}
		return nil
	}
}

func plainToHTML(text string) string {
	var b strings.Builder
	b.WriteString("<!doctype html><html><body style=\"font-family:Arial,sans-serif;font-size:14px;line-height:1.5\">")
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		b.WriteString("<p>")
		b.WriteString(strings.ReplaceAll(html.EscapeString(para), "\n", "<br>"))
		b.WriteString("</p>")
	}
	b.WriteString("</body></html>")
	return b.String()
}

// failStaleEmails reverts e-mail answers queued an hour ago that never left
// (the worker gave up): the dispute waits again and the organizer is told.
func (s *Service) failStaleEmails(ctx context.Context) ([]Dispute, error) {
	rows, err := s.db.Query(ctx, `SELECT d.id FROM payment_disputes d
		WHERE d.state = 'submitted' AND d.channel <> 'api'
		  AND EXISTS (SELECT 1 FROM payment_dispute_events e WHERE e.dispute_id = d.id AND e.kind = 'email_queued'
		              AND e.created_at < now() - interval '1 hour'
		              AND e.created_at = (SELECT max(created_at) FROM payment_dispute_events
		                                  WHERE dispute_id = d.id AND kind IN ('email_queued','email_sent','sent_by_organizer','email_failed')))
		  AND NOT EXISTS (SELECT 1 FROM worker_jobs w WHERE w.job_type = $1 AND w.status IN ('pending','claimed')
		                  AND w.payload->>'dispute_id' = d.id::text)`, JobSendEmail)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	var out []Dispute
	for _, id := range ids {
		tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return out, err
		}
		d, err := getDisputeAnyOrg(ctx, tx, id, true)
		if err == nil && d.State == StateSubmitted {
			_, err = tx.Exec(ctx, `UPDATE payment_disputes SET state = 'draft_ready', submitted_at = NULL, submitted_by = NULL,
				updated_at = now() WHERE id = $1`, id)
			if err == nil {
				_, err = insertEvent(ctx, tx, d, "email_failed", "", "", Actor{Label: "system"}, nil)
			}
			if err == nil {
				err = tx.Commit(ctx)
				out = append(out, d)
			}
		}
		_ = tx.Rollback(ctx)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
