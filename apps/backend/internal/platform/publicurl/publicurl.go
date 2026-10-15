// Package publicurl builds the buyers' address of an organization's or an
// event's hosted sales page. It is the ONE place that knows the shape
// <base>/<page slug>[/<event slug>], so the Telegram bot and the REST card
// of an event cannot drift. The base is a parameter on purpose: the
// organizer's own domain (plan decision D2) replaces it here and nowhere
// else. The package is pure: no database, no configuration.
package publicurl

import (
	"net/url"
	"strings"
)

// Page returns the buyers' page address, or "" when it cannot be built.
//
//   - base is the tickets origin (PUBLIC_TICKETS_BASE_URL); a trailing slash
//     is ignored. An empty base gives "".
//   - A promoter with a page of its own is preferred over the organization
//     (owner decision 2026-09-29: the organization's slug is a technical
//     detail nobody outside sees). An empty promoterSlug means the
//     organization itself is the page.
//   - An empty page slug (no promoter slug and no organization slug) gives "".
//   - With eventSlug the address is that event's page, without it the page of
//     the promoter or organization.
//
// The address says where a page would be, not whether it is reachable: it
// does not look at the event's publication status.
func Page(base, orgSlug, promoterSlug, eventSlug string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	page := strings.TrimSpace(promoterSlug)
	if page == "" {
		page = strings.TrimSpace(orgSlug)
	}
	if page == "" {
		return ""
	}
	out := base + "/" + url.PathEscape(page)
	if ev := strings.TrimSpace(eventSlug); ev != "" {
		out += "/" + url.PathEscape(ev)
	}
	return out
}
