// export_filter.go — ?tickets= export modes (owner decision 2026-09-18).
//
// MACS's file importer ignores holderStatus on import and stores every
// ticket it is handed as valid (0), so exporting refunded/cancelled/revoked
// tickets alongside active ones (the pre-existing "all" behaviour) lets an
// already-refunded buyer back in at the door. The MACS scanner is fed
// semi-manually for now: the operator exports a "valid tickets" file for
// MACS import and a separate "revoked" list to manually strike entries MACS
// already imported. This file is the filtering step shared by both modes;
// it runs on the neutral orderexport projection, BEFORE the MACS wire
// encoding, so the wire types never even see a ticket the caller did not ask
// for.
package macs

import (
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
)

// ExportMode selects which tickets a MACS export includes.
type ExportMode string

const (
	// ExportModeAll is the pre-existing behaviour: every completed ticket
	// regardless of status. This stays the API default (?tickets= omitted)
	// for backward compatibility with existing callers/tests/docs; the
	// admin UI defaults its primary button to ExportModeValid instead.
	ExportModeAll ExportMode = "all"
	// ExportModeValid includes only tickets in an active platform state.
	// An order left with zero valid tickets is omitted entirely.
	ExportModeValid ExportMode = "valid"
	// ExportModeRevoked includes only refunded/cancelled/revoked tickets —
	// the door "remove these entries" list. An order left with zero
	// matching tickets is omitted entirely.
	ExportModeRevoked ExportMode = "revoked"
)

// ParseExportMode validates the ?tickets= query value. An empty string maps
// to ExportModeAll (the documented API default). Returns ok=false for any
// other value.
func ParseExportMode(raw string) (ExportMode, bool) {
	switch ExportMode(raw) {
	case "":
		return ExportModeAll, true
	case ExportModeAll, ExportModeValid, ExportModeRevoked:
		return ExportMode(raw), true
	default:
		return "", false
	}
}

// filterOrders applies mode (and, for ExportModeRevoked, an optional
// revokedSince cutoff) to the projected orders. Orders left with no
// remaining tickets are dropped so an "orders with zero valid tickets" (or
// zero revoked tickets) never appears with an empty ticketList.
func filterOrders(orders []orderexport.Order, mode ExportMode, revokedSince *time.Time) []orderexport.Order {
	if mode == ExportModeAll {
		return orders
	}
	out := make([]orderexport.Order, 0, len(orders))
	for _, o := range orders {
		tickets := make([]orderexport.Ticket, 0, len(o.Tickets))
		for _, t := range o.Tickets {
			if ticketMatchesMode(t, mode, revokedSince) {
				tickets = append(tickets, t)
			}
		}
		if len(tickets) == 0 {
			continue
		}
		o.Tickets = tickets
		out = append(out, o)
	}
	return out
}

// ticketMatchesMode is the per-ticket predicate behind filterOrders.
func ticketMatchesMode(t orderexport.Ticket, mode ExportMode, revokedSince *time.Time) bool {
	isValid := t.PlatformStatus == "active"
	switch mode {
	case ExportModeValid:
		return isValid
	case ExportModeRevoked:
		if isValid {
			return false
		}
		if revokedSince == nil {
			return true
		}
		changed := revokedChangeTime(t)
		return changed != nil && !changed.Before(*revokedSince)
	default: // ExportModeAll never reaches here (short-circuited above)
		return true
	}
}

// revokedChangeTime is the timestamp a revoked-list consumer cares about:
// the refund date when one was stamped, otherwise the cancellation time (a
// "none" refund-mode cancellation has no refund_date but still changed).
func revokedChangeTime(t orderexport.Ticket) *time.Time {
	if t.RefundDate != nil {
		return t.RefundDate
	}
	return t.CancelledAt
}
