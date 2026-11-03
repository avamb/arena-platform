package disputes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// BuildResult says what a build did.
type BuildResult struct {
	Package    *Package
	DraftSaved bool
	// DraftError is why the provider draft could not be saved (the package
	// is stored all the same).
	DraftError string
}

// Build collects the facts, renders the package, stores it as a new version
// and, for an API channel dispute that still waits, saves the provider
// draft (submit=false). actor.UserID is nil for the automatic build.
func (s *Service) Build(ctx context.Context, orgID, disputeID uuid.UUID, actor Actor) (BuildResult, error) {
	var (
		d   Dispute
		err error
	)
	if orgID == uuid.Nil {
		d, err = getDisputeAnyOrg(ctx, s.db, disputeID, false)
	} else {
		d, err = getDispute(ctx, s.db, orgID, disputeID, false)
	}
	if err != nil {
		return BuildResult{}, err
	}
	resp, rerr := s.responder(ctx, d)
	if rerr != nil {
		s.logger.Warn("disputes: provider module unavailable for the package", slog.String("dispute_id", d.ID.String()), slog.String("error", rerr.Error()))
	}

	facts, err := CollectFacts(ctx, s.db, d)
	if err != nil {
		return BuildResult{}, err
	}
	if resp != nil && d.ProviderDisputeID != nil {
		cctx, cancel := context.WithTimeout(ctx, s.callTimeout)
		if pd, gerr := resp.GetDispute(cctx, *d.ProviderDisputeID); gerr == nil && pd.ChargeID != "" {
			if cf, cerr := resp.ChargeFacts(cctx, pd.ChargeID); cerr == nil {
				facts.Charge = &ChargeInfo{ThreeDSecure: cf.ThreeDSecure, CardBrand: cf.CardBrand, Last4: cf.Last4, StatementDescriptor: cf.StatementDescriptor}
			}
		}
		cancel()
	}

	var orgLocale string
	if err := s.db.QueryRow(ctx, `SELECT COALESCE(default_locale,'en') FROM organizations WHERE id = $1`, d.OrgID).Scan(&orgLocale); err != nil {
		return BuildResult{}, err
	}
	budget := EmailBudget
	if d.Channel == ChannelAPI {
		budget = StripeBudget
	}
	built, err := BuildPackage(d, facts, orgLocale, budget, s.respondTo(d))
	if err != nil {
		return BuildResult{}, err
	}

	pkg, err := s.storePackage(ctx, d, built, actor)
	if err != nil {
		return BuildResult{}, err
	}
	res := BuildResult{Package: pkg}
	if d.Channel != ChannelAPI || resp == nil || d.ProviderDisputeID == nil || !d.Open() {
		return res, nil
	}
	if _, err := s.saveDraft(ctx, d, pkg, resp, nil, false, ""); err != nil {
		res.DraftError = draftErrorCode(err)
		s.logger.Warn("disputes: provider draft not saved", slog.String("dispute_id", d.ID.String()), slog.String("error", err.Error()))
		_, _ = insertEvent(ctx, s.db, d, "draft_failed", "", "", actor, map[string]any{"code": res.DraftError, "version": pkg.Version})
		return res, nil
	}
	res.DraftSaved = true
	_, _ = insertEvent(ctx, s.db, d, "draft_saved", "", "", actor, map[string]any{"version": pkg.Version})
	return res, nil
}

// respondTo is where an e-mail answer goes: the address the organizer
// entered, else the provider's published one.
func (s *Service) respondTo(d Dispute) string {
	if strings.TrimSpace(d.RespondTo) != "" {
		return d.RespondTo
	}
	if s.modules != nil {
		if desc, ok := s.modules.Descriptor(d.Provider); ok {
			return desc.DisputeEmail
		}
	}
	return ""
}

func (s *Service) storePackage(ctx context.Context, d Dispute, b Built, actor Actor) (*Package, error) {
	fields, _ := json.Marshal(b.Fields)
	gaps, _ := json.Marshal(b.Gaps)
	var draft []byte
	if b.EmailDraft != nil {
		draft, _ = json.Marshal(b.EmailDraft)
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock the dispute so two builds number their versions one after another.
	cur, err := getDisputeAnyOrg(ctx, tx, d.ID, true)
	if err != nil {
		return nil, err
	}
	var pkgID uuid.UUID
	var version int
	if err := tx.QueryRow(ctx, `INSERT INTO payment_dispute_packages
			(dispute_id, org_id, version, locale, fields, gaps, email_draft, total_bytes, total_pages, built_by)
		VALUES ($1, $2, COALESCE((SELECT max(version) FROM payment_dispute_packages WHERE dispute_id = $1), 0) + 1,
		        $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, version`,
		d.ID, d.OrgID, b.Locale, fields, gaps, nullJSON(draft), b.TotalBytes, b.TotalPages, actor.UserID).Scan(&pkgID, &version); err != nil {
		return nil, fmt.Errorf("disputes: store package: %w", err)
	}
	for _, f := range b.Files {
		if _, err := tx.Exec(ctx, `INSERT INTO payment_dispute_files
				(package_id, kind, filename, content, bytes, pages, sha256, for_provider)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			pkgID, f.Kind, f.Filename, f.Content, len(f.Content), f.Pages, f.SHA256, f.ForProvider); err != nil {
			return nil, fmt.Errorf("disputes: store file %s: %w", f.Kind, err)
		}
	}
	if _, err := insertEvent(ctx, tx, cur, "package_built", "", "", actor, map[string]any{
		"version": version, "pages": b.TotalPages, "bytes": b.TotalBytes, "gaps": b.Gaps,
	}); err != nil {
		return nil, err
	}
	if cur.State == StateNeedsResponse {
		if _, err := tx.Exec(ctx, `UPDATE payment_disputes SET state = $2, updated_at = now() WHERE id = $1`, d.ID, StateDraftReady); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return latestPackage(ctx, s.db, d.OrgID, d.ID)
}

func nullJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// saveDraft uploads the provider files that have no provider id yet and
// writes the evidence. submit=true sends it to the bank.
func (s *Service) saveDraft(ctx context.Context, d Dispute, pkg *Package, resp payments.DisputeResponder, extra map[string]string, submit bool, keySuffix string) (payments.Dispute, error) {
	files, err := packageFiles(ctx, s.db, pkg.ID)
	if err != nil {
		return payments.Dispute{}, err
	}
	ids := map[string]string{}
	for k, v := range pkg.ProviderFileIDs {
		ids[k] = v
	}
	uploaded := false
	for kind, f := range files {
		if ids[kind] != "" {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, s.callTimeout)
		id, uerr := resp.UploadDisputeFile(cctx, payments.DisputeFile{Filename: f.Filename, Content: f.Content},
			fmt.Sprintf("arena-dispute-%s-v%d-%s", d.ID, pkg.Version, kind))
		cancel()
		if uerr != nil {
			return payments.Dispute{}, uerr
		}
		ids[kind] = id
		uploaded = true
	}
	if uploaded {
		body, _ := json.Marshal(ids)
		if _, err := s.db.Exec(ctx, `UPDATE payment_dispute_packages SET provider_file_ids = $2 WHERE id = $1`, pkg.ID, body); err != nil {
			return payments.Dispute{}, err
		}
		pkg.ProviderFileIDs = ids
	}
	evidence := map[string]string{}
	for k, v := range pkg.Fields {
		evidence[k] = v
	}
	for kind, id := range ids {
		evidence[kind] = id
	}
	if id := ids[FilePolicy]; id != "" && d.ReasonGroup == ReasonRefund {
		evidence["cancellation_policy"] = id
	}
	for k, v := range extra {
		evidence[k] = v
	}
	key := fmt.Sprintf("arena-dispute-%s-draft-v%d", d.ID, pkg.Version)
	if submit {
		key = fmt.Sprintf("arena-dispute-%s-submit-v%d%s", d.ID, pkg.Version, keySuffix)
	}
	cctx, cancel := context.WithTimeout(ctx, s.callTimeout)
	defer cancel()
	return resp.UpdateDisputeEvidence(cctx, *d.ProviderDisputeID, evidence, submit, key)
}

// draftErrorCode turns a provider error into a code the card explains.
func draftErrorCode(err error) string {
	switch {
	case errors.Is(err, payments.ErrDisputeNotWritable):
		return "key_not_writable"
	case errors.Is(err, payments.ErrDisputeClosedForEvidence):
		return "closed_for_evidence"
	}
	return "provider_unavailable"
}

// BuildJobHandler is the worker handler of dispute.build_package.
func (s *Service) BuildJobHandler() func(ctx context.Context, payload []byte) error {
	return func(ctx context.Context, payload []byte) error {
		var p struct {
			DisputeID string `json:"dispute_id"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("disputes: build payload: %w", err)
		}
		id, err := uuid.Parse(p.DisputeID)
		if err != nil {
			return fmt.Errorf("disputes: build payload: %w", err)
		}
		_, err = s.Build(ctx, uuid.Nil, id, Actor{Label: "system"})
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
}
