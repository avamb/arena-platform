package eventwatch

// sitesync.go — which storefronts already show an announced change (owner
// decision 2026-10-14, migration 0152).
//
// A hosted page or widget channel reads arena live, so it shows a change the
// moment it is made. A WordPress site sells from its own copy of the catalog,
// refreshed through the Bil24-compatible gateway (GET_ALL_ACTIONS), which
// records the start of every successful fetch in channel_catalog_fetches. The
// announcement therefore ends with one line per storefront, and the watcher
// remembers every site that has not fetched since the change
// (event_watch_site_sync). On every later pass a site that has fetched since
// gets ONE short confirmation, and a site still silent after
// SiteSyncWarnAfter gets ONE warning (and still a confirmation when it finally
// fetches). A newer announcement of the same event replaces its rows, so an
// older change is never confirmed.
//
// Which channels count for an event:
//   - a SITE is a channel of the event's organization that carries a gateway
//     credential (settings.gateway.token_hash, or the legacy top-level
//     gateway_token_hash — the two shapes hbil24.parseGatewaySettings reads).
//     GET_ALL_ACTIONS lists EVERY published event of the organization,
//     whatever event_publications says, so a site sells the event without a
//     publication row. To keep a dead or never-connected site from warning on
//     every change, a site is tracked only when it fetched the catalog within
//     siteActiveWindow; a site the event is explicitly published to that has
//     not fetched in that time is listed as idle and not tracked.
//   - a LIVE channel is a channel without a gateway credential that the event
//     is published to through an active feed token (event_publications ->
//     agent_feed_tokens.sales_channel_id): the hosted page and the widget.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// SiteSyncWarnAfter is how long a site may go without fetching an announced
// change before the operator is warned once.
const SiteSyncWarnAfter = 30 * time.Minute

// siteActiveWindow is how recent a site's last catalog fetch must be for the
// site to be tracked at all.
const siteActiveWindow = 7 * 24 * time.Hour

// StoreState is what a storefront shows of the announced version of an event.
type StoreState int

const (
	// StateLive is a channel that reads arena live (hosted page, widget).
	StateLive StoreState = iota
	// StateSynced is a site that has fetched the catalog since the change.
	StateSynced
	// StateWaiting is a site that has not fetched the catalog since the change.
	StateWaiting
	// StateIdle is a site the event is published to that has not fetched the
	// catalog for siteActiveWindow; it is listed but not tracked.
	StateIdle
)

// Storefront is one sales channel of an event and its state.
type Storefront struct {
	ChannelID string
	Name      string
	State     StoreState
}

const storefrontsSQL = `
	SELECT ch.id::text, ch.name,
	       (COALESCE(ch.settings->'gateway'->>'token_hash', '') <> ''
	        OR COALESCE(ch.settings->>'gateway_token_hash', '') <> '') AS is_site,
	       f.last_fetched_at,
	       EXISTS (SELECT 1
	                 FROM event_publications ep
	                 JOIN agent_feed_tokens t ON t.id = ep.feed_token_id
	                WHERE ep.event_id = $1::uuid
	                  AND t.sales_channel_id = ch.id
	                  AND t.is_active AND t.revoked_at IS NULL) AS published
	  FROM sales_channels ch
	  LEFT JOIN channel_catalog_fetches f ON f.channel_id = ch.id
	 WHERE ch.org_id = $2::uuid AND ch.deleted_at IS NULL
	 ORDER BY ch.name, ch.id`

// LoadStorefronts lists the storefronts of an event and what each shows of
// the version first seen at changedAt.
func LoadStorefronts(ctx context.Context, q Querier, eventID, orgID string, changedAt, now time.Time) ([]Storefront, error) {
	rs, err := q.Query(ctx, storefrontsSQL, eventID, orgID)
	if err != nil {
		return nil, fmt.Errorf("eventwatch: read storefronts: %w", err)
	}
	defer rs.Close()
	var out []Storefront
	for rs.Next() {
		var (
			sf        Storefront
			isSite    bool
			fetchedAt *time.Time
			published bool
		)
		if err := rs.Scan(&sf.ChannelID, &sf.Name, &isSite, &fetchedAt, &published); err != nil {
			return nil, fmt.Errorf("eventwatch: scan storefront: %w", err)
		}
		switch {
		case !isSite && published:
			sf.State = StateLive
		case !isSite:
			continue
		case fetchedAt != nil && fetchedAt.After(changedAt):
			sf.State = StateSynced
		case fetchedAt != nil && now.Sub(*fetchedAt) <= siteActiveWindow:
			sf.State = StateWaiting
		case published:
			sf.State = StateIdle
		default:
			continue
		}
		out = append(out, sf)
	}
	return out, rs.Err()
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// recordSiteSync replaces the event's tracked sites with the ones of this
// announcement: a waiting site is pending, a synced one is confirmed at once.
func recordSiteSync(ctx context.Context, db execer, eventID string, fronts []Storefront, changedAt, now time.Time) error {
	if _, err := db.Exec(ctx, `DELETE FROM event_watch_site_sync WHERE event_id = $1::uuid`, eventID); err != nil {
		return fmt.Errorf("eventwatch: clear site sync: %w", err)
	}
	for _, sf := range fronts {
		var confirmed *time.Time
		switch sf.State {
		case StateWaiting:
		case StateSynced:
			confirmed = &now
		default:
			continue
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO event_watch_site_sync (event_id, channel_id, changed_at, announced_at, confirmed_at)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5)`, eventID, sf.ChannelID, changedAt, now, confirmed); err != nil {
			return fmt.Errorf("eventwatch: record site sync: %w", err)
		}
	}
	return nil
}

// SiteDelay is one site that picked up a change and how long it took.
type SiteDelay struct {
	Name  string
	After time.Duration
}

type pendingSync struct {
	eventID, channelID string
	changedAt          time.Time
	warned             bool
	fetchedAt          *time.Time
	channelName        string
	channelAlive       bool
	eventName          string
	eventAlive         bool
}

// eventNotes are the messages one pass owes for one event.
type eventNotes struct {
	name      string
	confirmed []SiteDelay
	late      []string
}

// processSiteSync confirms the sites that have fetched since the change, warns
// once about the ones silent for SiteSyncWarnAfter and drops the rows of
// events or channels that are gone. It records before it sends, like the
// announcements. It returns how many messages it sent.
func (w *Watcher) processSiteSync(ctx context.Context, now time.Time) (int, error) {
	rs, err := w.pool.Query(ctx, `
		SELECT s.event_id::text, s.channel_id::text, s.changed_at, s.warned_at IS NOT NULL,
		       f.last_fetched_at,
		       COALESCE(ch.name, ''), COALESCE(ch.deleted_at IS NULL, false),
		       COALESCE(e.name, ''), COALESCE(e.status = 'published' AND e.deleted_at IS NULL, false)
		  FROM event_watch_site_sync s
		  LEFT JOIN channel_catalog_fetches f ON f.channel_id = s.channel_id
		  LEFT JOIN sales_channels ch ON ch.id = s.channel_id
		  LEFT JOIN events e ON e.id = s.event_id
		 WHERE s.confirmed_at IS NULL
		 ORDER BY s.changed_at, s.event_id, ch.name, s.channel_id`)
	if err != nil {
		return 0, fmt.Errorf("eventwatch: read site sync: %w", err)
	}
	var pending []pendingSync
	for rs.Next() {
		var p pendingSync
		if err := rs.Scan(&p.eventID, &p.channelID, &p.changedAt, &p.warned, &p.fetchedAt,
			&p.channelName, &p.channelAlive, &p.eventName, &p.eventAlive); err != nil {
			rs.Close()
			return 0, fmt.Errorf("eventwatch: scan site sync: %w", err)
		}
		pending = append(pending, p)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return 0, fmt.Errorf("eventwatch: read site sync: %w", err)
	}

	var order []string
	notes := map[string]*eventNotes{}
	note := func(p pendingSync) *eventNotes {
		n, ok := notes[p.eventID]
		if !ok {
			n = &eventNotes{name: p.eventName}
			notes[p.eventID] = n
			order = append(order, p.eventID)
		}
		return n
	}
	for _, p := range pending {
		switch {
		case !p.eventAlive || !p.channelAlive:
			if _, err := w.pool.Exec(ctx, `
				DELETE FROM event_watch_site_sync WHERE event_id = $1::uuid AND channel_id = $2::uuid`,
				p.eventID, p.channelID); err != nil {
				return 0, fmt.Errorf("eventwatch: drop site sync: %w", err)
			}
		case p.fetchedAt != nil && p.fetchedAt.After(p.changedAt):
			if _, err := w.pool.Exec(ctx, `
				UPDATE event_watch_site_sync SET confirmed_at = $3
				 WHERE event_id = $1::uuid AND channel_id = $2::uuid`,
				p.eventID, p.channelID, now); err != nil {
				return 0, fmt.Errorf("eventwatch: confirm site sync: %w", err)
			}
			n := note(p)
			n.confirmed = append(n.confirmed, SiteDelay{Name: p.channelName, After: p.fetchedAt.Sub(p.changedAt)})
		case !p.warned && now.Sub(p.changedAt) >= SiteSyncWarnAfter:
			if _, err := w.pool.Exec(ctx, `
				UPDATE event_watch_site_sync SET warned_at = $3
				 WHERE event_id = $1::uuid AND channel_id = $2::uuid`,
				p.eventID, p.channelID, now); err != nil {
				return 0, fmt.Errorf("eventwatch: warn site sync: %w", err)
			}
			n := note(p)
			n.late = append(n.late, p.channelName)
		}
	}

	if w.announcer == nil {
		return 0, nil
	}
	sent := 0
	for _, id := range order {
		n := notes[id]
		if len(n.confirmed) > 0 {
			w.announcer.AnnounceEventChange(ctx, SiteSyncedText(n.name, n.confirmed))
			sent++
		}
		if len(n.late) > 0 {
			w.announcer.AnnounceEventChange(ctx, SiteLateText(n.name, n.late))
			sent++
		}
	}
	return sent, nil
}
