package eventbot

// arena_client_plan_intake.go — the routes of the seating plan intake (spec
// 08_architecture/64): the organizer sends files, reads the draft an
// administrator made, asks for a fix or approves it. Every call runs under the
// person's own JWT; the notices are claimed under the service token.

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// planIntakeChannel is what the bot writes into the intake's channel field.
const planIntakeChannel = "telegram_bot"

// planIntakePostTimeout bounds the post of the files (up to 80 MB).
const planIntakePostTimeout = 5 * time.Minute

// multipartBody is a form built by the caller: do sends it as it is.
type multipartBody struct {
	contentType string
	data        []byte
}

// PlanIntakeFile is one file of an intake.
type PlanIntakeFile struct {
	Name string
	Data []byte
}

// planPartFileName makes a file name safe for a multipart header: no line
// breaks and no quotes (a name is only a label, the server sniffs the content).
func planPartFileName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', '"', '\'', '\\', 0:
			return -1
		}
		return r
	}, name)
	if name = strings.TrimSpace(name); name == "" {
		return "file"
	}
	return name
}

func planIntakePath(orgID uuid.UUID, intakeID string, tail string) string {
	return "/v1/organizations/" + orgID.String() + "/plan-intakes/" + url.PathEscape(intakeID) + tail
}

// CreatePlanIntake posts the files as ONE intake of the venue. The key makes a
// repeat of the same press send nothing twice.
func (c *ArenaClient) CreatePlanIntake(ctx context.Context, jwt string, orgID uuid.UUID, venueID, idempotencyKey string, files []PlanIntakeFile) (openapi.VenuePlanIntake, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range files {
		part, err := mw.CreateFormFile("files", planPartFileName(f.Name))
		if err != nil {
			return openapi.VenuePlanIntake{}, err
		}
		if _, err := part.Write(f.Data); err != nil {
			return openapi.VenuePlanIntake{}, err
		}
	}
	if err := mw.WriteField("channel", planIntakeChannel); err != nil {
		return openapi.VenuePlanIntake{}, err
	}
	if err := mw.Close(); err != nil {
		return openapi.VenuePlanIntake{}, err
	}
	if idempotencyKey != "" {
		ctx = withIdempotencyKey(ctx, idempotencyKey)
	}
	ctx = withCallTimeout(ctx, planIntakePostTimeout) // up to 80 MB
	var out openapi.VenuePlanIntakeEnvelope
	path := "/v1/organizations/" + orgID.String() + "/venues/" + url.PathEscape(venueID) + "/plan-intakes"
	if err := c.do(ctx, http.MethodPost, path, jwt, multipartBody{contentType: mw.FormDataContentType(), data: buf.Bytes()}, &out); err != nil {
		return openapi.VenuePlanIntake{}, err
	}
	return out.Intake, nil
}

// GetPlanIntake reads one intake of the organization.
func (c *ArenaClient) GetPlanIntake(ctx context.Context, jwt string, orgID uuid.UUID, intakeID string) (openapi.VenuePlanIntake, error) {
	var out openapi.VenuePlanIntakeEnvelope
	if err := c.do(ctx, http.MethodGet, planIntakePath(orgID, intakeID, ""), jwt, nil, &out); err != nil {
		return openapi.VenuePlanIntake{}, err
	}
	return out.Intake, nil
}

// ListPlanIntakes lists the organization's intakes of a venue, newest first.
// state may be empty (every state).
func (c *ArenaClient) ListPlanIntakes(ctx context.Context, jwt string, orgID uuid.UUID, venueID, state string) ([]openapi.VenuePlanIntakeListItem, error) {
	q := url.Values{}
	if venueID != "" {
		q.Set("venue_id", venueID)
	}
	if state != "" {
		q.Set("state", state)
	}
	var out openapi.VenuePlanIntakeList
	path := "/v1/organizations/" + orgID.String() + "/plan-intakes"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	if err := c.do(ctx, http.MethodGet, path, jwt, nil, &out); err != nil {
		return nil, err
	}
	return out.Intakes, nil
}

// PlanIntakeDraftSVG reads the SVG of draft n.
func (c *ArenaClient) PlanIntakeDraftSVG(ctx context.Context, jwt string, orgID uuid.UUID, intakeID string, n int) ([]byte, error) {
	var svg rawText
	if err := c.do(ctx, http.MethodGet, planIntakePath(orgID, intakeID, "/drafts/"+strconv.Itoa(n)+"/svg"), jwt, nil, &svg); err != nil {
		return nil, err
	}
	if len(svg) == 0 {
		return nil, fmt.Errorf("GET plan intake draft %d: empty answer", n)
	}
	return []byte(svg), nil
}

// CorrectPlanIntakeText sends what the organizer wants fixed in draft n.
func (c *ArenaClient) CorrectPlanIntakeText(ctx context.Context, jwt string, orgID uuid.UUID, intakeID string, draft int, text string) (openapi.VenuePlanIntake, error) {
	var out openapi.VenuePlanIntakeEnvelope
	body := map[string]any{"draft": draft, "text": text}
	if err := c.do(ctx, http.MethodPost, planIntakePath(orgID, intakeID, "/corrections-text"), jwt, body, &out); err != nil {
		return openapi.VenuePlanIntake{}, err
	}
	return out.Intake, nil
}

// EscalatePlanIntake calls a platform administrator.
func (c *ArenaClient) EscalatePlanIntake(ctx context.Context, jwt string, orgID uuid.UUID, intakeID string) (openapi.VenuePlanIntake, error) {
	var out openapi.VenuePlanIntakeEnvelope
	if err := c.do(ctx, http.MethodPost, planIntakePath(orgID, intakeID, "/escalate"), jwt, map[string]any{}, &out); err != nil {
		return openapi.VenuePlanIntake{}, err
	}
	return out.Intake, nil
}

// ApprovePlanIntake approves draft n as the venue's seating plan.
func (c *ArenaClient) ApprovePlanIntake(ctx context.Context, jwt string, orgID uuid.UUID, intakeID string, draft int) (openapi.VenuePlanIntake, error) {
	var out openapi.VenuePlanIntakeEnvelope
	if err := c.do(ctx, http.MethodPost, planIntakePath(orgID, intakeID, "/approve"), jwt, map[string]any{"draft": draft}, &out); err != nil {
		return openapi.VenuePlanIntake{}, err
	}
	return out.Intake, nil
}

// CancelPlanIntake withdraws an intake that is not final.
func (c *ArenaClient) CancelPlanIntake(ctx context.Context, jwt string, orgID uuid.UUID, intakeID string) error {
	return c.do(ctx, http.MethodPost, planIntakePath(orgID, intakeID, "/cancel"), jwt, map[string]any{}, nil)
}

// ClaimVenuePlanNotices takes the notices not told yet; each comes once.
func (c *ArenaClient) ClaimVenuePlanNotices(ctx context.Context) ([]openapi.BotVenuePlanNotice, error) {
	var out openapi.BotVenuePlanNoticesResponse
	if err := c.do(ctx, http.MethodPost, "/v1/bot/venue-plan-notices/claim", c.serviceToken, struct{}{}, &out); err != nil {
		return nil, err
	}
	return out.Notices, nil
}

// PlanRefByID reads a seating plan at its current version, as the wizard's plan
// question lists it. It is the plan an approved intake made.
func (c *ArenaClient) PlanRefByID(ctx context.Context, jwt string, planID uuid.UUID) (PlanRef, error) {
	var plan struct {
		Plan openapi.SeatingPlan `json:"seating_plan"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/seating-plans/"+planID.String(), jwt, nil, &plan); err != nil {
		return PlanRef{}, err
	}
	if plan.Plan.CurrentVersionId == nil {
		return PlanRef{}, errNoPlacesStored
	}
	var versions struct {
		Versions []openapi.SeatingPlanVersion `json:"seating_plan_versions"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/seating-plans/"+planID.String()+"/versions", jwt, nil, &versions); err != nil {
		return PlanRef{}, err
	}
	for _, v := range versions.Versions {
		if v.Id == *plan.Plan.CurrentVersionId {
			if ref, ok := planRefOf(plan.Plan, v); ok {
				return ref, nil
			}
		}
	}
	return PlanRef{}, errNoPlacesStored
}
