package hreferral

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	domain "github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// Why a channel cannot pay the cashback (channelJSON.Reason).
const (
	reasonSellerSite   = "seller_site"
	reasonNoModule     = "provider_unsupported"
	reasonNoPartialRef = "no_partial_refund"
)

// Warning codes of the capability block.
const (
	warnNoChannel       = "referral.warning.no_channel"
	warnSomeUnsupported = "referral.warning.some_channels_unsupported"
)

type channelJSON struct {
	ChannelID string `json:"channel_id"`
	Provider  string `json:"provider"`
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
}

type capabilityJSON struct {
	// Supported: at least one sales channel of the event can return a part of
	// a payment to the card (the cashback is exactly that), or the event sells
	// nowhere yet and nothing is known against it.
	Supported bool `json:"supported"`
	// Source says which channels were judged: "event" (the ones the event is
	// published into) or "organization" (its own channels, the event is not
	// published yet).
	Source   string        `json:"source"`
	Channels []channelJSON `json:"channels"`
}

type warningJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// channelCan judges one channel (pure given the registry).
func channelCan(reg *domain.Registry, c gen.ReferralChannel) channelJSON {
	out := channelJSON{ChannelID: c.ID.String(), Provider: c.Provider}
	if c.IsSite {
		out.Reason = reasonSellerSite
		return out
	}
	name := domain.NormalizeProviderName(c.Provider)
	entry, ok := reg.Get(name)
	if !ok || entry.New == nil {
		out.Reason = reasonNoModule
		return out
	}
	if !entry.Descriptor.Capabilities.PartialRefund {
		out.Reason = reasonNoPartialRef
		return out
	}
	out.Supported = true
	return out
}

// judge folds the channels into the capability and its warnings (pure).
func judge(reg *domain.Registry, source string, channels []gen.ReferralChannel) (capabilityJSON, []warningJSON) {
	out := capabilityJSON{Source: source, Channels: make([]channelJSON, 0, len(channels))}
	warnings := []warningJSON{}
	if len(channels) == 0 {
		out.Supported = true
		warnings = append(warnings, warningJSON{warnNoChannel,
			"This event sells through no channel yet; the programme works once it is published on a page with card payments."})
		return out, warnings
	}
	someBad := false
	for _, c := range channels {
		cj := channelCan(reg, c)
		out.Channels = append(out.Channels, cj)
		if cj.Supported {
			out.Supported = true
		} else {
			someBad = true
		}
	}
	if out.Supported && someBad {
		warnings = append(warnings, warningJSON{warnSomeUnsupported,
			"Some channels of this event cannot pay the cashback; friend codes work on the other channels only."})
	}
	return out, warnings
}

// capability reads the event's channels and judges them. Channels the event is
// published into win; an event published nowhere is judged by the
// organization's own storefront channels (the ones publishing would bind it
// to: a channel with a gateway credential is a WordPress site's and never is).
func (h *Handler) eventCapability(ctx context.Context, orgID, eventID uuid.UUID) (capabilityJSON, []warningJSON, error) {
	channels, err := h.queries.ListReferralEventChannels(ctx, eventID, orgID)
	if err != nil {
		return capabilityJSON{}, nil, err
	}
	source := "event"
	if len(channels) == 0 {
		source = "organization"
		all, err := h.queries.ListReferralOrgChannels(ctx, orgID)
		if err != nil {
			return capabilityJSON{}, nil, err
		}
		for _, c := range all {
			if !c.IsSite {
				channels = append(channels, c)
			}
		}
	}
	cap, warnings := judge(h.registry, source, channels)
	return cap, warnings, nil
}

// capability judges what the target sells through.
func (h *Handler) capability(ctx context.Context, t target) (capabilityJSON, []warningJSON, error) {
	if t.isShop() {
		return h.shopCapability(ctx, t.orgID)
	}
	return h.eventCapability(ctx, t.orgID, *t.eventID)
}

// shopCapability judges the sales channel the shop takes its money through:
// the one the shop settings name, else (the shop picks the first usable one at
// checkout) the organization's own non-site channels.
func (h *Handler) shopCapability(ctx context.Context, orgID uuid.UUID) (capabilityJSON, []warningJSON, error) {
	source := "shop"
	var channels []gen.ReferralChannel
	ch, err := h.queries.GetReferralShopSalesChannel(ctx, orgID)
	switch {
	case err == nil:
		channels = []gen.ReferralChannel{ch}
	case errors.Is(err, pgx.ErrNoRows):
		source = "organization"
		all, lerr := h.queries.ListReferralOrgChannels(ctx, orgID)
		if lerr != nil {
			return capabilityJSON{}, nil, lerr
		}
		for _, c := range all {
			if !c.IsSite {
				channels = append(channels, c)
			}
		}
	default:
		return capabilityJSON{}, nil, err
	}
	out, warnings := judge(h.registry, source, channels)
	for i := range warnings {
		switch warnings[i].Code {
		case warnNoChannel:
			warnings[i].Message = "The shop takes no card payments yet; the programme works once a payment channel is set up."
		case warnSomeUnsupported:
			warnings[i].Message = "Some payment channels of the organization cannot pay the cashback; friend codes work on the shop's own channel only."
		}
	}
	return out, warnings, nil
}
