package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/brevo"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// handleAdminOrganizationSenderDNS exposes only DNS instructions to a
// superadmin. It never exposes Brevo credentials and is deliberately
// unavailable until the platform operator configures BREVO_API_KEY.
func (s *Server) handleAdminOrganizationSenderDNS(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdminReason(w, r); !ok {
		return
	}
	if s.orgQueries == nil || s.cfg == nil || s.cfg.BrevoAPIKey == "" {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("sender_identity.unavailable", "sender verification is not configured", r))
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	org, err := s.orgQueries.GetOrganizationByID(r.Context(), id)
	if err != nil {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("sender_identity.not_found", "organization not found", r))
		return
	}
	if org.SenderEmail == nil {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("sender_identity.not_configured", "organization has no sender email", r))
		return
	}
	sender, err := brevo.New(s.cfg.BrevoAPIKey, s.cfg.BrevoAPIBaseURL, nil).GetSender(r.Context(), *org.SenderEmail)
	if err != nil {
		s.logger.Warn("sender identity lookup failed", "error", err)
		httputil.WriteJSON(w, http.StatusBadGateway, httputil.ErrorEnvelope("sender_identity.lookup_failed", "could not fetch sender verification records", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"sender_email": *org.SenderEmail, "status": map[bool]string{true: "verified", false: "pending"}[sender.Active], "dns_records": sender.DNSRecords})
}
