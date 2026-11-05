package hshop

// storefront.go — the shop module's mode on the organization routes and the
// storefront switch (spec 08_architecture/61 sections 3.4 and 4.1).
//
// The server's module gate (httpserver/mount_shop.go) resolves the shop mode
// on the pool before any handler runs and parks it on the request context
// (WithShopMode); the settings answers report it. Opening the storefront is
// the organization's own decision, PUT .../shop/storefront or the enabled
// field of PUT .../shop/settings, under the same rules.

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hfeed"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orgmodules"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

type shopModeKey struct{}

// WithShopMode records the organization's shop mode for the handlers of the
// request (the module gate sets it).
func WithShopMode(ctx context.Context, mode orgmodules.Mode) context.Context {
	return context.WithValue(ctx, shopModeKey{}, mode)
}

// ShopModeFrom is the shop mode the module gate recorded; ok is false when no
// gate ran (a handler built without one).
func ShopModeFrom(ctx context.Context) (orgmodules.Mode, bool) {
	m, ok := ctx.Value(shopModeKey{}).(orgmodules.Mode)
	return m, ok
}

// paymentReady is the payment pre-flight of an OPENING: the sales channel the
// settings name (or the organization's live channels when they name none,
// the order the checkout tries them in) must lead to a usable provider
// config the provider has verified. It reads on the pool and must run before
// any transaction. A database failure is an error; a missing or unusable
// configuration is simply false.
func (h *Handler) paymentReady(ctx context.Context, orgID uuid.UUID, channel *uuid.UUID) (bool, error) {
	if h.o.Payments == nil {
		return false, nil
	}
	view := shop.SettingsView{}
	view.OrgID, view.SalesChannelID = orgID, channel
	candidates, err := h.o.Service.PaymentChannelCandidates(ctx, view)
	if err != nil {
		if _, refused := shop.AsError(err); refused {
			return false, nil
		}
		return false, err
	}
	check := h.o.Payments.CheckPaymentConfigured
	if v, ok := h.o.Payments.(hfeed.PaymentVerifier); ok {
		check = v.CheckPaymentVerified
	}
	for _, id := range candidates {
		err := check(ctx, orgID, id)
		if err == nil {
			return true, nil
		}
		if _, refused := hfeed.AsPaymentStartError(err); !refused {
			return false, err
		}
	}
	return false, nil
}

// settingsAnswer is the settings as the organization routes answer them,
// with the module's mode and what keeps the storefront closed.
func (h *Handler) settingsAnswer(ctx context.Context, st shop.SettingsView) (settingsJSON, error) {
	out := toSettingsJSON(st)
	mode, gated := ShopModeFrom(ctx)
	if !gated {
		// No gate ran (a handler wired without the server): judge on the pool.
		m, err := h.o.Service.ShopMode(ctx, st.OrgID)
		if err != nil {
			return out, err
		}
		mode = m
	}
	out.ModuleActive = mode == orgmodules.ModeFull
	if mode != orgmodules.ModeNone {
		m := string(mode)
		out.ShopMode = &m
	}
	ready, err := h.paymentReady(ctx, st.OrgID, st.SalesChannelID)
	if err != nil {
		return out, err
	}
	list, err := h.o.Service.OpenBlockers(ctx, st, ready)
	if err != nil {
		return out, err
	}
	out.OpenBlockers = list
	out.CanOpen = out.ModuleActive && len(list) == 0
	return out, nil
}

// writeSettings answers the settings with their mode and blockers.
func (h *Handler) writeSettings(w http.ResponseWriter, r *http.Request, st shop.SettingsView) {
	out, err := h.settingsAnswer(r.Context(), st)
	if err != nil {
		h.fail(w, r, "read settings", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}

type storefrontBody struct {
	Enabled *bool `json:"enabled"`
}

// HandlePutStorefront serves PUT .../shop/storefront {"enabled": bool}: opens
// or closes the storefront without rewriting the other settings. Any holder
// of shop.manage in the organization may do it while the module is connected
// (the gate checks that); opening is refused 422 shop.cannot_open with
// details.blockers, closing always succeeds.
func (h *Handler) HandlePutStorefront(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	var b storefrontBody
	if !readBody(w, r, &b, false) {
		return
	}
	if b.Enabled == nil {
		writeErr(w, r, http.StatusBadRequest, "shop.invalid_body", "enabled is required: true opens the storefront, false closes it")
		return
	}
	ctx := r.Context()
	before, err := h.o.Service.GetSettings(ctx, org)
	if err != nil {
		h.fail(w, r, "read settings", err)
		return
	}
	ready := false
	if *b.Enabled {
		ready, err = h.paymentReady(ctx, org, before.SalesChannelID)
		if err != nil {
			h.fail(w, r, "check payment", err)
			return
		}
	}
	st, err := h.o.Service.SetStorefront(ctx, org, *b.Enabled, ready)
	if err != nil {
		h.fail(w, r, "set storefront", err)
		return
	}
	if st.Enabled != before.Enabled {
		h.writeAudit(r, "v1.shop.storefront.update", "shop_settings", org.String(), org, map[string]any{
			"enabled_before": before.Enabled, "enabled": st.Enabled,
		})
	}
	h.writeSettings(w, r, st)
}
