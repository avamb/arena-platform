// Package hseller serves the owner's "Organization data" (HP-4a): the legal
// data the hosted sales page prints in its seller block, and the per-channel
// settings of that block (settings.hosted_page.show_seller and
// settings.hosted_page.public_contact).
//
//	GET   /v1/organizations/{org_id}/seller-profile
//	PATCH /v1/organizations/{org_id}/channels/{channel_id}/seller-display
//
// Both need org.update in the path organization (the owner; the manager
// does not hold it). The legal fields themselves are written through PATCH
// /v1/organizations/{id}, which owns their formats; this package reads them
// and decides whether a channel may show them. The rules (formats, what is
// required, the personal-mailbox hint) live in platform/sellerinfo.
package hseller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sellerinfo"
)

// Handler serves the seller-profile routes. The caller has already passed
// the permission gate and the membership check of {org_id}.
type Handler struct {
	pool    *pgxpool.Pool
	queries *gen.Queries
	audit   audit.Writer
	logger  *slog.Logger
}

// New builds a Handler. pool may be nil, in which case every route answers 503.
func New(pool *pgxpool.Pool, auditWriter audit.Writer, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{pool: pool, audit: auditWriter, logger: logger}
	if pool != nil {
		h.queries = gen.New(pool)
	}
	return h
}

// Contact is a channel's published contact. Unset keys are omitted.
type Contact struct {
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"`
}

// Channel is one hosted-page channel in the profile.
type Channel struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	ShowSeller         bool     `json:"show_seller"`
	PublicContact      *Contact `json:"public_contact"`
	EmailLooksPersonal bool     `json:"email_looks_personal"`
	// Missing lists what must be filled before ShowSeller may be turned on
	// (sellerinfo.Missing): the organization's fields plus public_email.
	Missing []string `json:"missing"`
}

// Profile is the answer of GET .../seller-profile.
type Profile struct {
	OrgID                  string    `json:"org_id"`
	OrgName                string    `json:"org_name"`
	Country                string    `json:"country"`
	LegalName              *string   `json:"legal_name"`
	LegalForm              *string   `json:"legal_form"`
	RegistrationNumber     *string   `json:"registration_number"`
	TaxIDScheme            *string   `json:"tax_id_scheme"`
	TaxID                  *string   `json:"tax_id"`
	LegalAddressLine1      *string   `json:"legal_address_line1"`
	LegalAddressLine2      *string   `json:"legal_address_line2"`
	LegalAddressPostalCode *string   `json:"legal_address_postal_code"`
	LegalAddressCity       *string   `json:"legal_address_city"`
	LegalAddressCountry    *string   `json:"legal_address_country"`
	Missing                []string  `json:"missing"`
	Channels               []Channel `json:"channels"`
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request) bool {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return false
	}
	return true
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// parseContact reads settings.hosted_page.public_contact the way the public
// page does: anything that is not an object of two strings is no contact.
func parseContact(raw []byte) *Contact {
	if len(raw) == 0 {
		return nil
	}
	var in struct {
		Email any `json:"email"`
		Phone any `json:"phone"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	s := func(v any) string {
		x, _ := v.(string)
		return strings.TrimSpace(x)
	}
	c := Contact{Email: s(in.Email), Phone: s(in.Phone)}
	if c.Email == "" && c.Phone == "" {
		return nil
	}
	return &c
}

// orgFacts is what the required-field check reads from the organization.
func orgFacts(o gen.OrganizationRow) sellerinfo.Profile {
	return sellerinfo.Profile{
		LegalName:   str(o.LegalName),
		TaxID:       str(o.TaxID),
		AddressLine: str(o.LegalAddressLine1),
		City:        str(o.LegalAddressCity),
		PostalCode:  str(o.LegalAddressPostalCode),
	}
}

// channelMissing is sellerinfo.Missing for the organization plus one
// channel's public e-mail.
func channelMissing(o gen.OrganizationRow, c *Contact) []string {
	p := orgFacts(o)
	if c != nil {
		p.PublicEmail = c.Email
	}
	return nonNil(sellerinfo.Missing(p))
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// loadProfile reads the organization, its legal form and its hosted-page
// channels.
func (h *Handler) loadProfile(ctx context.Context, orgID uuid.UUID) (Profile, gen.OrganizationRow, error) {
	o, err := h.queries.GetOrganizationByID(ctx, orgID)
	if err != nil {
		return Profile{}, o, err
	}
	form, err := h.queries.GetOrganizationLegalForm(ctx, orgID)
	if err != nil {
		return Profile{}, o, err
	}
	rows, err := h.queries.ListSellerChannels(ctx, orgID)
	if err != nil {
		return Profile{}, o, err
	}
	p := Profile{
		OrgID: o.ID.String(), OrgName: o.Name, Country: o.Country,
		LegalName: o.LegalName, LegalForm: form, RegistrationNumber: o.RegistrationNumber,
		TaxIDScheme: o.TaxIDScheme, TaxID: o.TaxID,
		LegalAddressLine1: o.LegalAddressLine1, LegalAddressLine2: o.LegalAddressLine2,
		LegalAddressPostalCode: o.LegalAddressPostalCode, LegalAddressCity: o.LegalAddressCity,
		LegalAddressCountry: o.LegalAddressCountry,
		Missing:             nonNil(sellerinfo.Missing(withEmailPlaceholder(orgFacts(o)))),
		Channels:            []Channel{},
	}
	for _, row := range rows {
		c := parseContact(row.PublicContact)
		ch := Channel{
			ID: row.ID.String(), Name: row.Name, ShowSeller: row.ShowSeller, PublicContact: c,
			Missing: channelMissing(o, c),
		}
		if c != nil && c.Email != "" {
			ch.EmailLooksPersonal = sellerinfo.LooksPersonalEmail(c.Email)
		}
		p.Channels = append(p.Channels, ch)
	}
	return p, o, nil
}

// withEmailPlaceholder makes the organization-level list ignore the public
// e-mail, which belongs to a channel.
func withEmailPlaceholder(p sellerinfo.Profile) sellerinfo.Profile {
	p.PublicEmail = "-"
	return p
}

// HandleGetProfile serves GET /v1/organizations/{org_id}/seller-profile.
func (h *Handler) HandleGetProfile(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	p, _, err := h.loadProfile(r.Context(), orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("org.not_found", "organization not found", r))
			return
		}
		h.logger.Error("seller: profile read failed", slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("seller.read_failed", "could not read the organization data, please try again", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"seller_profile": p})
}

// optional is a tri-state JSON member: absent, null, or a value.
type optional[T any] struct {
	Present bool
	Null    bool
	Value   T
}

func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Present = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

type contactPatch struct {
	Email optional[string] `json:"email"`
	Phone optional[string] `json:"phone"`
}

type displayPatch struct {
	ShowSeller    optional[bool]         `json:"show_seller"`
	PublicContact optional[contactPatch] `json:"public_contact"`
}

// HandlePatchDisplay serves PATCH
// /v1/organizations/{org_id}/channels/{channel_id}/seller-display.
//
// Body members are tri-state: an absent member keeps the stored value,
// public_contact null removes the whole contact, an inner email/phone null or
// "" clears that one. show_seller true is refused with 422 seller.incomplete
// (details.missing) while a required field is empty, and so is clearing the
// e-mail of a channel that keeps the block on.
func (h *Handler) HandlePatchDisplay(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) || h.pool == nil {
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	channelID, ok := httputil.UUIDPathParam(w, r, "channel_id")
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if err != nil || len(strings.TrimSpace(string(body))) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("seller.invalid_body", "the request body is empty; send show_seller or public_contact", r))
		return
	}
	var req displayPatch
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("seller.invalid_body", "the request body is not valid JSON for this route", r))
		return
	}
	if req.ShowSeller.Present && req.ShowSeller.Null {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails("seller.invalid_body", "show_seller must be true or false", r, map[string]any{"field": "show_seller"}))
		return
	}

	profile, org, err := h.loadProfile(ctx, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("org.not_found", "organization not found", r))
			return
		}
		h.logger.Error("seller: profile read failed", slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("seller.read_failed", "could not read the organization data, please try again", r))
		return
	}
	var cur *Channel
	for i := range profile.Channels {
		if profile.Channels[i].ID == channelID.String() {
			cur = &profile.Channels[i]
			break
		}
	}
	if cur == nil {
		// Another organization's channel, an unknown one, or one without a
		// hosted page: the route's own 404, never a hint which.
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("seller.channel_not_found", "this organization has no sales page channel with that id", r))
		return
	}

	next := Contact{}
	if cur.PublicContact != nil {
		next = *cur.PublicContact
	}
	contactChanged := false
	if req.PublicContact.Present {
		contactChanged = true
		if req.PublicContact.Null {
			next = Contact{}
		} else {
			cp := req.PublicContact.Value
			if cp.Email.Present {
				next.Email = ""
				if !cp.Email.Null && strings.TrimSpace(cp.Email.Value) != "" {
					email, ok := sellerinfo.NormalizeEmail(cp.Email.Value)
					if !ok {
						httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails("seller.invalid_email", "this does not look like an e-mail address; please check it, for example tickets@example.com", r, map[string]any{"field": "public_contact.email"}))
						return
					}
					next.Email = email
				}
			}
			if cp.Phone.Present {
				next.Phone = ""
				if !cp.Phone.Null && strings.TrimSpace(cp.Phone.Value) != "" {
					phone, ok := sellerinfo.NormalizePhone(cp.Phone.Value)
					if !ok {
						httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails("seller.invalid_phone", "this does not look like a phone number; please use digits with the country code, for example +34 600 000 000", r, map[string]any{"field": "public_contact.phone"}))
						return
					}
					next.Phone = phone
				}
			}
		}
	}
	show := cur.ShowSeller
	if req.ShowSeller.Present {
		show = req.ShowSeller.Value
	}
	missing := channelMissing(org, &next)
	// Refuse a request that asks for the block while data is missing, or that
	// takes the e-mail away from a channel that keeps the block on. An edit
	// of the phone alone on an older, incomplete channel is not blocked.
	askedOn := req.ShowSeller.Present && req.ShowSeller.Value
	losingEmail := show && contactChanged && next.Email == "" && cur.PublicContact != nil && cur.PublicContact.Email != ""
	if show && len(missing) > 0 && (askedOn || losingEmail) {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"seller.incomplete",
			"the seller data cannot be shown yet: please fill in the fields listed in details.missing first",
			r, map[string]any{"missing": missing},
		))
		return
	}

	var contactJSON []byte
	if next.Email != "" || next.Phone != "" {
		contactJSON, _ = json.Marshal(next)
	}
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.writeFailed(w, r, orgID, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	updated, err := h.queries.WithTx(tx).SetChannelSellerDisplay(ctx, channelID, orgID, show, contactJSON)
	if err != nil {
		h.writeFailed(w, r, orgID, err)
		return
	}
	if !updated {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("seller.channel_not_found", "this organization has no sales page channel with that id", r))
		return
	}
	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		// No address in the audit row: only what changed.
		if err := h.audit.WriteTx(ctx, tx, audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user", // rewritten to api_key by audit.WithServiceActor
			ActorID:      actor.ID,
			Action:       "v1.channel.seller_display_update",
			ResourceType: "sales_channel",
			ResourceID:   channelID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"org_id":          orgID.String(),
				"show_seller":     show,
				"contact_changed": contactChanged,
				"has_email":       next.Email != "",
				"has_phone":       next.Phone != "",
			},
		}); err != nil {
			h.writeFailed(w, r, orgID, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		h.writeFailed(w, r, orgID, err)
		return
	}
	after, _, err := h.loadProfile(ctx, orgID)
	if err != nil {
		h.writeFailed(w, r, orgID, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"seller_profile": after})
}

func (h *Handler) writeFailed(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, err error) {
	h.logger.Error("seller: display update failed", slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("seller.update_failed", "could not save the change, please try again", r))
}
