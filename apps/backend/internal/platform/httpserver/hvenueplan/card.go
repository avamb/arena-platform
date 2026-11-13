package hvenueplan

// card.go — the intake card: one JSON shape for the organization and the
// administrators (the administrators' card adds the organization's name, the
// operator and the organizer's text corrections).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/intake"
)

// summaryJSON is the draft summary shared with the worker (intake.Summary).
type summaryJSON = intake.Summary

type sourceJSON struct {
	ID              string  `json:"id"`
	Kind            string  `json:"kind"`
	FileName        string  `json:"file_name"`
	ByteSize        int64   `json:"byte_size"`
	Pages           *int32  `json:"pages"`
	Width           *int32  `json:"width"`
	Height          *int32  `json:"height"`
	NeedsConversion bool    `json:"needs_conversion"`
	DownloadPath    string  `json:"download_path"`
	PNGCopyPath     *string `json:"png_copy_path"`
}

type questionJSON struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Target     json.RawMessage `json:"target"`
	Region     json.RawMessage `json:"region"`
	Options    json.RawMessage `json:"options"`
	Answer     json.RawMessage `json:"answer"`
	AnsweredAt *string         `json:"answered_at"`
}

type draftJSON struct {
	N         int32          `json:"n"`
	Author    string         `json:"author"`
	Note      *string        `json:"note"`
	Summary   summaryJSON    `json:"summary"`
	SVGPath   string         `json:"svg_path"`
	Questions []questionJSON `json:"questions"`
	CreatedAt string         `json:"created_at"`
}

type draftHistoryJSON struct {
	N         int32  `json:"n"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
}

type correctionJSON struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Draft     *int32  `json:"draft"`
	Text      *string `json:"text"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"created_at"`
}

// intakeJSON is the card. OrgName, OperatorID, OperatorClaimedAt and
// Corrections are filled for the administrators only.
type intakeJSON struct {
	ID               string             `json:"id"`
	OrgID            string             `json:"org_id"`
	VenueID          string             `json:"venue_id"`
	VenueName        string             `json:"venue_name"`
	State            string             `json:"state"`
	Channel          string             `json:"channel"`
	EscalationReason *string            `json:"escalation_reason"`
	EscalationText   *string            `json:"escalation_text"`
	ReturnNote       *string            `json:"return_note"`
	RejectReason     *string            `json:"reject_reason"`
	OperatorClaimed  bool               `json:"operator_claimed"`
	CurrentDraft     *int32             `json:"current_draft"`
	PlanID           *string            `json:"plan_id"`
	PlanVersion      *int32             `json:"plan_version"`
	CreatedAt        string             `json:"created_at"`
	UpdatedAt        string             `json:"updated_at"`
	ClosedAt         *string            `json:"closed_at"`
	Sources          []sourceJSON       `json:"sources"`
	Draft            *draftJSON         `json:"draft"`
	Drafts           []draftHistoryJSON `json:"drafts"`
	ReviewRounds     int                `json:"review_rounds"`

	OrgName           *string          `json:"org_name,omitempty"`
	OperatorID        *string          `json:"operator_id,omitempty"`
	OperatorClaimedAt *string          `json:"operator_claimed_at,omitempty"`
	Corrections       []correctionJSON `json:"corrections,omitempty"`
}

func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func fmtTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := fmtTime(*t)
	return &s
}

func uuidStr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// basePath is the route prefix of an intake for the caller's side.
func basePath(in gen.VenuePlanIntakeRow, admin bool) string {
	if admin {
		return "/v1/admin/plan-intakes/" + in.ID.String()
	}
	return "/v1/organizations/" + in.OrgID.String() + "/plan-intakes/" + in.ID.String()
}

// summaryOf is intake.SummaryOf, shared with the worker.
var summaryOf = intake.SummaryOf

// summaryFromReport reads the summary stored in a draft's report.
func summaryFromReport(raw json.RawMessage) summaryJSON {
	s := summaryJSON{Warnings: []string{}}
	_ = json.Unmarshal(raw, &s)
	if s.Warnings == nil {
		s.Warnings = []string{}
	}
	return s
}

// venueName reads a venue's name, deleted venues included.
func (h *Handler) venueName(ctx context.Context, q *gen.Queries, id uuid.UUID) string {
	v, err := q.GetVenueByID(ctx, id)
	if err != nil {
		return ""
	}
	return v.Name
}

// buildCard reads everything the card shows.
func (h *Handler) buildCard(ctx context.Context, q *gen.Queries, in gen.VenuePlanIntakeRow, admin bool) (intakeJSON, error) {
	base := basePath(in, admin)
	card := intakeJSON{
		ID: in.ID.String(), OrgID: in.OrgID.String(), VenueID: in.VenueID.String(),
		VenueName: h.venueName(ctx, q, in.VenueID), State: in.State, Channel: in.Channel,
		EscalationReason: in.EscalationReason, EscalationText: in.EscalationText,
		ReturnNote: in.ReturnNote, RejectReason: in.RejectReason,
		OperatorClaimed: in.OperatorID != nil, CurrentDraft: in.CurrentDraft,
		PlanID: uuidStr(in.PlanID), PlanVersion: in.PlanVersion,
		CreatedAt: fmtTime(in.CreatedAt), UpdatedAt: fmtTime(in.UpdatedAt), ClosedAt: fmtTimePtr(in.ClosedAt),
		Sources: []sourceJSON{}, Drafts: []draftHistoryJSON{},
	}
	sources, err := q.ListVenuePlanIntakeSources(ctx, in.ID)
	if err != nil {
		return card, fmt.Errorf("sources: %w", err)
	}
	for _, s := range sources {
		sj := sourceJSON{
			ID: s.ID.String(), Kind: s.Kind, FileName: s.FileName, ByteSize: s.ByteSize,
			Pages: s.Pages, Width: s.Width, Height: s.Height, NeedsConversion: s.NeedsConversion,
			DownloadPath: base + "/sources/" + s.ID.String(),
		}
		if s.NormalizedMediaID != nil {
			sj.PNGCopyPath = strPtr(sj.DownloadPath + "?format=png")
		}
		card.Sources = append(card.Sources, sj)
	}
	drafts, err := q.ListVenuePlanDrafts(ctx, in.ID)
	if err != nil {
		return card, fmt.Errorf("drafts: %w", err)
	}
	draftN := map[uuid.UUID]int32{}
	for _, d := range drafts {
		draftN[d.ID] = d.N
		card.Drafts = append(card.Drafts, draftHistoryJSON{N: d.N, Author: d.Author, CreatedAt: fmtTime(d.CreatedAt)})
		if in.CurrentDraft == nil || d.N != *in.CurrentDraft {
			continue
		}
		card.Draft = &draftJSON{
			N: d.N, Author: d.Author, Note: d.Note, Summary: summaryFromReport(d.Report),
			SVGPath:   fmt.Sprintf("%s/drafts/%d/svg", base, d.N),
			Questions: []questionJSON{}, CreatedAt: fmtTime(d.CreatedAt),
		}
	}
	card.ReviewRounds = len(drafts)
	if card.Draft != nil {
		questions, err := q.ListVenuePlanQuestions(ctx, in.ID)
		if err != nil {
			return card, fmt.Errorf("questions: %w", err)
		}
		for _, qu := range questions {
			if qu.DraftN != card.Draft.N {
				continue
			}
			card.Draft.Questions = append(card.Draft.Questions, questionJSON{
				ID: qu.ID.String(), Kind: qu.Kind, Target: rawOr(qu.Target, "{}"), Region: rawOr(qu.Region, "null"),
				Options: rawOr(qu.Options, "[]"), Answer: rawOr(qu.Answer, "null"), AnsweredAt: fmtTimePtr(qu.AnsweredAt),
			})
		}
	}
	if !admin {
		return card, nil
	}
	if org, err := q.GetOrganizationByID(ctx, in.OrgID); err == nil {
		card.OrgName = strPtr(org.Name)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return card, fmt.Errorf("organization: %w", err)
	}
	card.OperatorID = uuidStr(in.OperatorID)
	card.OperatorClaimedAt = fmtTimePtr(in.OperatorClaimedAt)
	corrections, err := q.ListVenuePlanCorrections(ctx, in.ID)
	if err != nil {
		return card, fmt.Errorf("corrections: %w", err)
	}
	card.Corrections = []correctionJSON{}
	for _, c := range corrections {
		cj := correctionJSON{ID: c.ID.String(), Kind: c.Kind, Text: c.Text, Status: c.Status, CreatedAt: fmtTime(c.CreatedAt)}
		if c.DraftID != nil {
			if n, ok := draftN[*c.DraftID]; ok {
				cj.Draft = &n
			}
		}
		card.Corrections = append(card.Corrections, cj)
	}
	return card, nil
}

func rawOr(raw json.RawMessage, def string) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(def)
	}
	return raw
}
