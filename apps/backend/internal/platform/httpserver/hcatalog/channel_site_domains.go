// channel_site_domains.go - the organizer's site hosts of a sales channel
// (spec 65 section 5, task E3).
//
//	GET /v1/organizations/{org_id}/channels/{id}/site-domains
//	PUT /v1/organizations/{org_id}/channels/{id}/site-domains
//
// The list is explicit because CORS_ALLOWED_ORIGINS is shared by every
// organization: the host of one organizer site must reach only that
// organization Stripe account. A PUT replaces the whole list and queues
// payment_domains.sync for the organization (best effort).
package hcatalog

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/paymentdomains"
)

// MaxChannelSiteDomains is the most hosts one channel may list.
const MaxChannelSiteDomains = 10

// NormalizeSiteDomain turns a bare host or a URL into a lower-case host with no
// port, scheme, path or trailing dot. It refuses an IP address, localhost, a
// name without a dot, and labels that are not LDH. The reason is returned for
// the error details; an empty reason means the host is valid.
func NormalizeSiteDomain(raw string) (host, reason string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "empty"
	}
	if !strings.Contains(s, "://") {
		s = "//" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", "not a host or URL"
	}
	host = strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return "", "no host"
	}
	if net.ParseIP(host) != nil {
		return "", "an IP address is not allowed"
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "", "localhost is not allowed"
	}
	if len(host) > 253 {
		return "", "host is too long"
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return "", "host must contain a dot"
	}
	for _, l := range labels {
		if !validHostLabel(l) {
			return "", "host has an invalid label"
		}
	}
	last := labels[len(labels)-1]
	if strings.Trim(last, "0123456789") == "" {
		return "", "top-level label must not be numeric"
	}
	return host, ""
}

func validHostLabel(l string) bool {
	if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for i := 0; i < len(l); i++ {
		c := l[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

type channelSiteDomainsRequest struct {
	Domains []string `json:"domains"`
}

// HandleGetChannelSiteDomains handles GET .../site-domains.
func (h *Handler) HandleGetChannelSiteDomains(w http.ResponseWriter, r *http.Request) {
	h.siteDomains(w, r, false)
}

// HandlePutChannelSiteDomains handles PUT .../site-domains.
func (h *Handler) HandlePutChannelSiteDomains(w http.ResponseWriter, r *http.Request) {
	h.siteDomains(w, r, true)
}

func (h *Handler) siteDomains(w http.ResponseWriter, r *http.Request, write bool) {
	if h.channelQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	chID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.channelQueries, orgID) {
		return
	}

	var domains []string
	if write {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("channel.invalid_body", "cannot read request body: "+err.Error(), r))
			return
		}
		var req channelSiteDomainsRequest
		if err := json.Unmarshal(body, &req); err != nil || req.Domains == nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("channel.invalid_json", "request body must be a JSON object with a domains array", r))
			return
		}
		seen := map[string]bool{}
		domains = []string{}
		for i, raw := range req.Domains {
			d, reason := NormalizeSiteDomain(raw)
			if reason != "" {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"channel.invalid_site_domain", "a site domain is not a valid public host", r,
					map[string]any{"field": "domains", "index": i, "value": raw, "reason": reason},
				))
				return
			}
			if !seen[d] {
				seen[d] = true
				domains = append(domains, d)
			}
		}
		if len(domains) > MaxChannelSiteDomains {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"channel.too_many_site_domains", "a channel may list at most 10 site domains", r,
				map[string]any{"field": "domains", "max": MaxChannelSiteDomains},
			))
			return
		}
	}

	if _, err := h.channelQueries.GetSalesChannelByID(ctx, chID, orgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("channel.not_found", "sales channel not found", r))
			return
		}
		h.logger.Error("channel: site-domains lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.get_failed", "failed to get sales channel", r,
		))
		return
	}

	if write {
		if err := h.channelQueries.ReplaceSalesChannelSiteDomains(ctx, chID, orgID, domains); err != nil {
			h.logger.Error("channel: site-domains replace failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"channel.update_failed", "failed to update site domains", r,
			))
			return
		}
		if h.audit != nil {
			actor, _ := auth.ActorFromContext(ctx)
			if err := h.audit.Write(ctx, audit.Event{
				OccurredAt:   time.Now().UTC(),
				ActorType:    "user",
				ActorID:      actor.ID,
				Action:       "v1.channel.site_domains.update",
				ResourceType: "sales_channel",
				ResourceID:   chID.String(),
				RequestID:    logging.RequestID(ctx),
				TraceID:      logging.TraceID(ctx),
				IP:           httputil.ExtractClientIP(r),
				Metadata: map[string]any{
					"org_id":  orgID.String(),
					"domains": domains,
				},
			}); err != nil {
				h.logger.Error("channel: site-domains audit write failed", slog.String("error", err.Error()))
			}
		}
		// Best effort: the daily sweep picks it up when this fails.
		if err := paymentdomains.EnqueueSync(ctx, h.channelQueries.DB(), orgID); err != nil {
			h.logger.Warn("channel: could not queue wallet domain sync", slog.String("error", err.Error()))
		}
	} else {
		var err error
		domains, err = h.channelQueries.ListSalesChannelSiteDomains(ctx, chID, orgID)
		if err != nil {
			h.logger.Error("channel: site-domains list failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"channel.get_failed", "failed to load site domains", r,
			))
			return
		}
		if domains == nil {
			domains = []string{}
		}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"domains": domains})
}
