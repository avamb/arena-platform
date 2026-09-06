// Package himports implements the operator-facing bulk import surface
// (feature #517, W1-C3c; spec §13.2):
//
//	POST /v1/organizations/{org_id}/imports/bil24-session
//
// The endpoint accepts a Bil24-shaped session payload — assembled by the
// site-side import module from raw GET_ALL_ACTIONS / GET_SEAT_LIST responses
// — and upserts the corresponding arena venue / event / session / ticket-tier
// graph in a single transaction. It is idempotent on
// actionEvent.actionEventId: a repeat call updates the same rows and answers
// created:false with identical identifiers.
//
// The camelCase Bil24 request shape lives in internal/adapters/bil24compat
// (the wire-adapter package allowlisted by the snake_case JSON guardrail);
// everything this package emits is snake_case.
//
// Scope of this slice: GENERAL-ADMISSION sessions only. The payload's
// seatList / svg blocks are decoded and acknowledged with a warning, but
// seating-plan import and seat materialisation (spec §13.2 step 6) land in
// the sibling seating slice — hence seating_plan_version_id:null and
// seats_materialized:0 in every response produced here.
package himports

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

// TxStarter is the narrow subset of the server's PoolDB that himports
// requires. *pgxpool.Pool and the server's PoolDB both satisfy it by
// structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Doer is the narrow HTTP client interface used to side-load the poster
// referenced by action.bigPosterUrl. Tests substitute a stub.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// posterFetchTimeout bounds the poster side-load so a slow or hostile
// upstream cannot hold the import transaction open indefinitely. The fetch
// deliberately happens BEFORE the transaction opens (see handleImport), so
// this is a belt-and-braces bound on total request time.
const posterFetchTimeout = 15 * time.Second

// maxPosterBytes caps the side-loaded poster. Bil24 posters are ~1-3 MB;
// 16 MB leaves generous headroom while bounding memory and storage abuse.
const maxPosterBytes int64 = 16 << 20

// Handler holds the shared dependencies for the import handlers.
type Handler struct {
	queries           *gen.Queries
	membershipQueries *gen.Queries // used by requireOrgMembership
	pool              TxStarter
	media             *mediastore.Repo
	http              Doer
	audit             audit.Writer
	logger            *slog.Logger
	// now is injectable so tests get deterministic timestamps.
	now func() time.Time
}

// New constructs a Handler. A nil queries handle or a nil pool is allowed;
// the handler self-gates with a 503 dependency.database_unavailable envelope,
// matching the *Server route-mount precedent used across the httpserver
// sub-packages.
func New(q *gen.Queries, pool TxStarter, auditWriter audit.Writer, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		queries: q,
		pool:    pool,
		audit:   auditWriter,
		logger:  logger,
		http:    &http.Client{Timeout: posterFetchTimeout},
		now:     func() time.Time { return time.Now().UTC() },
	}
}

// WithMembershipQueries attaches the *gen.Queries handle used for org
// membership checks. When it is nil the membership check is skipped, matching
// the hapikeys / hbankaccounts precedent for test wiring.
func (h *Handler) WithMembershipQueries(q *gen.Queries) *Handler {
	h.membershipQueries = q
	return h
}

// WithMedia attaches the media repository used to side-load event posters.
// When it is nil the poster side-load is skipped and the response carries an
// import.poster_skipped warning instead — a missing poster never fails an
// otherwise valid import.
func (h *Handler) WithMedia(m *mediastore.Repo) *Handler {
	h.media = m
	return h
}

// WithHTTPClient overrides the client used for the poster side-load.
func (h *Handler) WithHTTPClient(d Doer) *Handler {
	if d != nil {
		h.http = d
	}
	return h
}

// WithClock overrides the time source. Intended for tests.
func (h *Handler) WithClock(now func() time.Time) *Handler {
	if now != nil {
		h.now = now
	}
	return h
}
