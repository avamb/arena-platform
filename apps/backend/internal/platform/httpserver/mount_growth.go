package httpserver

// mount_growth.go — the organizer's side of spec 51-1:
//
//   - GET/PUT /organizations/{org_id}/growth-settings: the cart reminder
//     switch, the marketing consent tick and the reward code. Read needs
//     promo.read (owner, manager), a change org.update (owner).
//   - GET /organizations/{org_id}/marketing-subscribers and the same as
//     .csv: the addresses that consented (confirmed by default). order.read,
//     and never for an organization API key: buyers' addresses go to people
//     of the organization, not to an integration.
//
// The public links (resume, confirm, withdraw) are mounted with the other
// public checkout routes in mount_catalog.go.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/csvexport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/marketing"
)

func (s *Server) mountGrowthRoutes(r chi.Router) {
	if !s.authEnabled() {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "promo.read", "promo_codes")
		pr.Get("/organizations/{org_id}/growth-settings", s.handleGetGrowthSettings)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "org.update", "organizations")
		pr.Put("/organizations/{org_id}/growth-settings", s.handlePutGrowthSettings)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "order.read", "orders")
		pr.Get("/organizations/{org_id}/marketing-subscribers", s.handleListMarketingSubscribers)
		pr.Get("/organizations/{org_id}/marketing-subscribers.csv", s.handleExportMarketingSubscribers)
	})
}

// growthSettingsResponse is the answer of both settings routes.
type growthSettingsResponse struct {
	CartReminderEnabled   bool    `json:"cart_reminder_enabled"`
	MarketingOptInEnabled bool    `json:"marketing_optin_enabled"`
	ConsentPromoCodeID    *string `json:"consent_promo_code_id"`
	UpdatedAt             *string `json:"updated_at"`
}

func growthSettingsJSON(st marketing.Settings) growthSettingsResponse {
	out := growthSettingsResponse{CartReminderEnabled: st.CartReminder, MarketingOptInEnabled: st.OptInEnabled}
	if st.ConsentPromoCodeID != nil {
		v := st.ConsentPromoCodeID.String()
		out.ConsentPromoCodeID = &v
	}
	if st.UpdatedAt != nil {
		v := st.UpdatedAt.UTC().Format(time.RFC3339)
		out.UpdatedAt = &v
	}
	return out
}

// growthStoreReady answers 503 when the server has no database pool.
func (s *Server) growthStoreReady(w http.ResponseWriter, r *http.Request) bool {
	if s.pgxPool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r))
		return false
	}
	return true
}

func (s *Server) handleGetGrowthSettings(w http.ResponseWriter, r *http.Request) {
	if !s.growthStoreReady(w, r) || !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	st, err := marketing.LoadSettings(r.Context(), s.pgxPool, orgID)
	if err != nil {
		s.logger.Error("growth settings: load failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"growth.load_failed", "We could not load the settings. Try again in a minute.", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, growthSettingsJSON(st))
}

// growthSettingsRequest: every key is required, the screen sends all three.
type growthSettingsRequest struct {
	CartReminderEnabled   *bool   `json:"cart_reminder_enabled"`
	MarketingOptInEnabled *bool   `json:"marketing_optin_enabled"`
	ConsentPromoCodeID    *string `json:"consent_promo_code_id"`
}

func (s *Server) handlePutGrowthSettings(w http.ResponseWriter, r *http.Request) {
	if !s.growthStoreReady(w, r) || !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	var req growthSettingsRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.CartReminderEnabled == nil || req.MarketingOptInEnabled == nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"growth.invalid_body", "Send cart_reminder_enabled and marketing_optin_enabled as true or false.", r))
		return
	}
	in := marketing.Settings{CartReminder: *req.CartReminderEnabled, OptInEnabled: *req.MarketingOptInEnabled}
	if req.ConsentPromoCodeID != nil && strings.TrimSpace(*req.ConsentPromoCodeID) != "" {
		id, err := uuid.Parse(strings.TrimSpace(*req.ConsentPromoCodeID))
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"growth.invalid_promo_code_id", "Send consent_promo_code_id as a promo code id, or null for no discount.", r))
			return
		}
		in.ConsentPromoCodeID = &id
	}
	var actor *uuid.UUID
	if a, ok := auth.ActorFromContext(r.Context()); ok && !a.IsService() {
		if id, err := uuid.Parse(a.ID); err == nil {
			actor = &id
		}
	}
	st, err := marketing.SaveSettings(r.Context(), s.pgxPool, orgID, in, actor)
	switch {
	case errors.Is(err, marketing.ErrCodeNotFound):
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"growth.promo_code_not_found", "This promo code is not one of the organization's live codes. Choose another code.", r))
		return
	case errors.Is(err, marketing.ErrCodeNeedsPerUser):
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"growth.promo_code_needs_one_use_per_buyer", "Set the code's limit to one use per buyer, then choose it again.", r))
		return
	case err != nil:
		s.logger.Error("growth settings: save failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"growth.save_failed", "We could not save the settings. Try again in a minute.", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, growthSettingsJSON(st))
}

type marketingSubscriberItem struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	Name        string  `json:"name"`
	Locale      string  `json:"locale"`
	Status      string  `json:"status"`
	GrantedAt   string  `json:"granted_at"`
	ConfirmedAt *string `json:"confirmed_at"`
	WithdrawnAt *string `json:"withdrawn_at"`
	WithdrawURL string  `json:"withdraw_url"`
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.UTC().Format(time.RFC3339)
	return &v
}

// subscribersFor runs the shared checks of both subscriber routes.
// subscribersFor reads the first page of the list. paged takes limit and
// offset from the query (the JSON list); the CSV export reads every row
// itself and ignores them.
func (s *Server) subscribersFor(w http.ResponseWriter, r *http.Request, limit int, paged bool) ([]marketing.Subscriber, int, bool) {
	if a, ok := auth.ActorFromContext(r.Context()); ok && a.IsService() {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"marketing.api_key_forbidden", "An API key cannot read the subscribers. A member of the organization reads them signed in.", r))
		return nil, 0, false
	}
	if !s.growthStoreReady(w, r) || !s.enforceOrgMembership(w, r, "org_id") {
		return nil, 0, false
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return nil, 0, false
	}
	q := r.URL.Query()
	status := q.Get("status")
	if !marketing.ValidStatusFilter(status) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"marketing.invalid_status", "Send status as confirmed, pending, withdrawn or all.", r))
		return nil, 0, false
	}
	offset := 0
	if paged {
		offset, _ = strconv.Atoi(q.Get("offset"))
		if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 && l <= limit {
			limit = l
		}
	}
	subs, total, err := marketing.List(r.Context(), s.pgxPool, orgID, marketing.ListFilter{Status: status, Limit: limit, Offset: offset})
	if err != nil {
		s.logger.Error("marketing subscribers: list failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"marketing.list_failed", "We could not load the subscribers. Try again in a minute.", r))
		return nil, 0, false
	}
	return subs, total, true
}

func (s *Server) withdrawURL(token string) string {
	return strings.TrimRight(s.apiPublicURL(), "/") + marketing.WithdrawPath(token)
}

func (s *Server) handleListMarketingSubscribers(w http.ResponseWriter, r *http.Request) {
	subs, total, ok := s.subscribersFor(w, r, 100, true)
	if !ok {
		return
	}
	items := make([]marketingSubscriberItem, 0, len(subs))
	for _, m := range subs {
		items = append(items, marketingSubscriberItem{
			ID: m.ID.String(), Email: m.Email, Name: m.Name, Locale: m.Locale, Status: m.Status,
			GrantedAt: m.GrantedAt.UTC().Format(time.RFC3339), ConfirmedAt: rfc3339Ptr(m.ConfirmedAt),
			WithdrawnAt: rfc3339Ptr(m.WithdrawnAt), WithdrawURL: s.withdrawURL(m.UnsubscribeToken),
		})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// subscriberCSVHeader: e-mail, name, language, status, consent date,
// confirmation date, link to stop the mailings.
var subscriberCSVHeader = map[string][]string{
	"en": {"E-mail", "Name", "Language", "Status", "Consent given", "Confirmed", "Link to stop the mailings"},
	"ru": {"E-mail", "Имя", "Язык", "Статус", "Согласие дано", "Подтверждено", "Ссылка для отказа от рассылки"},
	"es": {"Correo", "Nombre", "Idioma", "Estado", "Consentimiento", "Confirmado", "Enlace para dejar de recibir correos"},
}

// subscriberExportMaxRows is the platform's CSV ceiling (csvexport): above
// it the export is refused before the first byte rather than cut silently.
const subscriberExportMaxRows = 50000

// subscriberExportBatch is how many rows one query of the export reads.
const subscriberExportBatch = 1000

func (s *Server) handleExportMarketingSubscribers(w http.ResponseWriter, r *http.Request) {
	subs, total, ok := s.subscribersFor(w, r, subscriberExportBatch, false)
	if !ok {
		return
	}
	if total > subscriberExportMaxRows {
		httputil.WriteJSON(w, http.StatusRequestEntityTooLarge, httputil.ErrorEnvelopeWithDetails(
			"export.too_many_rows", "The list is too long for one file. Choose one status at a time.", r,
			map[string]any{"rows": total, "max_rows": subscriberExportMaxRows}))
		return
	}
	orgID, _ := uuid.Parse(chi.URLParam(r, "org_id"))
	status := r.URL.Query().Get("status")
	header, found := subscriberCSVHeader[csvexport.LocaleFromRequest(r)]
	if !found {
		header = subscriberCSVHeader["en"]
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="subscribers.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	cw := csvexport.NewWriter(w)
	if err := cw.WriteHeader(header); err != nil {
		return
	}
	// Every row, in batches: the first batch came with the count, the next
	// ones follow until a short batch. Once a byte is sent a failure can only
	// cut the connection (the csvexport rule).
	for offset := 0; ; {
		for _, m := range subs {
			if err := cw.WriteRow(csvexport.Text(m.Email), csvexport.Text(m.Name), csvexport.Text(m.Locale),
				csvexport.Text(m.Status), csvexport.DateTime(m.GrantedAt, time.UTC), csvexport.DateTimePtr(m.ConfirmedAt, time.UTC),
				csvexport.Text(s.withdrawURL(m.UnsubscribeToken))); err != nil {
				return
			}
		}
		if len(subs) < subscriberExportBatch {
			break
		}
		if err := cw.Flush(); err != nil {
			return
		}
		offset += len(subs)
		var err error
		subs, _, err = marketing.List(r.Context(), s.pgxPool, orgID,
			marketing.ListFilter{Status: status, Limit: subscriberExportBatch, Offset: offset})
		if err != nil {
			s.logger.Error("marketing subscribers: export batch failed", "error", err.Error())
			// allow:panic: net/http's documented way to abort a response whose
			// status is already committed, so a half-written file is never taken
			// for a complete one (the same rule as hexport).
			panic(http.ErrAbortHandler)
		}
	}
	_ = cw.Flush()
}
