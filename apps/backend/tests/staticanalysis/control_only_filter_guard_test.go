// control_only_filter_guard_test.go keeps the rule of MA-40 true: a session
// that exists only to control the door (sessions.sales_mode = 'control_only',
// migration 0228) is never shown to a buyer, sold, announced or exported to a
// partner by a query that forgot about it.
//
// Every query that reads the sessions table (FROM sessions / JOIN sessions)
// must either mention control_only (it filters, or handles, such a session) or
// be on an allowlist with a reason from a closed set. The guard has two
// scans and a stale check:
//
//   - the sqlc output (adapters/postgres/gen/*.go): each "-- name: X" query,
//     and each unnamed hand-written SQL string, is checked on its own;
//   - raw SQL in the other production Go files under internal and cmd: the
//     file as a whole must mention control_only or be on the file allowlist;
//   - an allowlist entry that no longer matches a query reading sessions
//     without control_only fails, so the list cannot rot.
//
// A new query is not forbidden to read sessions without a filter, it just has
// to join the allowlist on purpose, which is the moment somebody reads this
// comment and asks whether a buyer can reach it.
//
// A second test pins the queries every public surface goes through: they must
// carry control_only whatever the allowlist says.
package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The closed set of reasons an unfiltered query may stay unfiltered.
const (
	// reasonOrganizerOnly: an organization-authenticated organizer read,
	// report or export; the organizer sees the session together with its flag.
	reasonOrganizerOnly = "organizer-only"
	// reasonOrderBound: reached only through an existing order or ticket.
	reasonOrderBound = "order-bound"
	// reasonDoorControl: scanner or MACS purpose, it must see control sessions.
	reasonDoorControl = "door-control"
	// reasonWrite: insert, update or delete of the row itself.
	reasonWrite = "write"
	// reasonInternalKeyed: looked up by an id the caller already holds, gated
	// elsewhere.
	reasonInternalKeyed = "internal-keyed"
)

var closedReasons = map[string]bool{
	reasonOrganizerOnly: true,
	reasonOrderBound:    true,
	reasonDoorControl:   true,
	reasonWrite:         true,
	reasonInternalKeyed: true,
}

// allowEntry says why an unfiltered query is allowed. note is free text.
type allowEntry struct {
	reason string
	note   string
}

// sessionsReadSQL matches a read of the sessions table: the word boundary
// keeps session_seats, sessions_x and the like out.
var sessionsReadSQL = regexp.MustCompile(`(?i)\b(FROM|JOIN)\s+sessions\b`)

// sqlNameLine finds the sqlc marker that starts a generated query.
var sqlNameLine = regexp.MustCompile(`^-- name: (\w+)`)

// genQueryAllowlist: sqlc query name -> why it reads sessions unfiltered.
var genQueryAllowlist = map[string]allowEntry{
	// Organizer reads, reports and exports: the organizer sees the session
	// together with its sales_mode flag.
	"AggregateComplimentaryForEvent":    {reasonOrganizerOnly, "event report"},
	"AggregateReferralCashbackForEvent": {reasonOrganizerOnly, "event report"},
	"AggregateRefundsForEvent":          {reasonOrganizerOnly, "event report"},
	"AggregateSalesForEvent":            {reasonOrganizerOnly, "event report"},
	"AggregateScansForEvent":            {reasonOrganizerOnly, "event report"},
	"ClaimInvitationPDFNotices":         {reasonOrganizerOnly, "invitation PDF notice to the organizer's bot"},
	"CountExportSalesRows":              {reasonOrganizerOnly, "export"},
	"CountForeignSessionsAtVenue":       {reasonOrganizerOnly, "venue sharing check"},
	"CountOverlappingSessions":          {reasonOrganizerOnly, "overlap check of the organizer's own session write"},
	"EventDeleteImpact":                 {reasonOrganizerOnly, "delete-impact preview, web and bot"},
	"GetAdminSessionSchema":             {reasonOrganizerOnly, "admin variant of the schema route; the public variant GetPublicSessionSchema filters"},
	"GetExportSessionHeader":            {reasonOrganizerOnly, "export header"},
	"GetInvitationSessionTarget":        {reasonOrganizerOnly, "staff invitation target; issue.go refuses a non-sellable session"},
	"GetOrderEventFacts":                {reasonOrganizerOnly, "organizer order card"},
	"GetSampleTicketPresentation":       {reasonOrganizerOnly, "sample e-ticket of the organizer's session"},
	"GetSessionByExternalRef":           {reasonOrganizerOnly, "event-bundle import"},
	"GetSessionImportContext":           {reasonOrganizerOnly, "event-bundle import"},
	"GetSessionSummaryHeader":           {reasonOrganizerOnly, "session sales summary"},
	"GetSessionVenueCountry":            {reasonOrganizerOnly, "platform billing of a complimentary ticket"},
	"ListAccountingTickets":             {reasonOrganizerOnly, "accounting export"},
	"ListCollectCancelledTickets":       {reasonOrganizerOnly, "billing.collect facts of existing tickets"},
	"ListCollectComplimentaryTickets":   {reasonOrganizerOnly, "billing.collect facts of existing tickets"},
	"ListCollectOrderItems":             {reasonOrganizerOnly, "billing.collect facts of existing orders"},
	"ListCollectRefunds":                {reasonOrganizerOnly, "billing.collect facts of existing refunds"},
	"ListEventChargesBySession":         {reasonOrganizerOnly, "organizer billing usage screen"},
	"ListEventSummarySessionIDs":        {reasonOrganizerOnly, "event sales summary"},
	"ListEventSummarySessions":          {reasonOrganizerOnly, "event sales summary"},
	"ListEventVenueCities":              {reasonOrganizerOnly, "map-point check of the organizer's event"},
	"ListEventVenueNames":               {reasonOrganizerOnly, "organizer events list; the public twin is ListPublicEventVenueNames"},
	"ListExportPromoRedemptions":        {reasonOrganizerOnly, "export"},
	"ListExportSalesRows":               {reasonOrganizerOnly, "export"},
	"ListInvitationOrderLines":          {reasonOrganizerOnly, "invitation letters and lists of the organizer"},
	"ListOrgInvitations":                {reasonOrganizerOnly, "invitation list, web and bot"},
	"ListOrgSummarySessions":            {reasonOrganizerOnly, "organization sales summary"},
	"ListSessionSummaryAdmissions":      {reasonOrganizerOnly, "session sales summary"},
	"ListSessionsByEvent":               {reasonOrganizerOnly, "organizer list, publish gate, import and publication; buyers use ListPublicSessionsByEvent"},
	"ListTicketsForReconciliation":      {reasonOrganizerOnly, "platform superadmin reconciliation"},
	"LockSmartSession":                  {reasonOrganizerOnly, "smart-price preset of the organizer's session"},
	"SmartSessionFingerprint":           {reasonOrganizerOnly, "smart-price preset of the organizer's session"},
	"SessionInEventAndOrg":              {reasonOrganizerOnly, "organizer scope check of an inventory route"},

	// Reached only through an existing order or ticket.
	"EventIDForTicket":               {reasonOrderBound, "white-label brand of a ticket letter"},
	"GetOrgBrandingByTicketID":       {reasonOrderBound, "letter branding"},
	"GetOrganizationAccentForTicket": {reasonOrderBound, "ticket accent colour"},
	"GetSenderIdentityByTicketID":    {reasonOrderBound, "letter sender"},
	"GetTicketContactRoute":          {reasonOrderBound, "ticket delivery contact"},
	"GetTicketOrgID":                 {reasonOrderBound, "organization of a ticket"},
	"GetTicketPDFFormatByTicketID":   {reasonOrderBound, "no production caller found; ticket PDF format"},
	"GetTicketPresentationByID":      {reasonOrderBound, "ticket delivery presentation"},
	"GetTicketSellerCountries":       {reasonOrderBound, "seller countries of an order letter"},

	// Scanner and MACS: the door must see control sessions.
	"ResolveMACSScanTicketByBarcode":   {reasonDoorControl, "MACS scan upload"},
	"ResolveMACSScanTicketBySystemID":  {reasonDoorControl, "MACS scan upload"},
	"ResolveScanCredentialByHumanCode": {reasonDoorControl, "no production caller found; scanner credential"},
	"ResolveScanCredentialByTicketQR":  {reasonDoorControl, "scanner callback"},

	// Writes of the row (or the lock a write takes).
	"SetSessionSaleTimes": {reasonWrite, "update of sales end and doors"},

	// Looked up by an id the caller already holds.
	"GetSessionCurrency":                {reasonInternalKeyed, "organizer seating and tier writes"},
	"GetSessionPlanID":                  {reasonInternalKeyed, "organizer seat layouts"},
	"GetSessionSalesEnd":                {reasonInternalKeyed, "no production caller found"},
	"GetSessionSeatingBinding":          {reasonInternalKeyed, "organizer seating bind and import"},
	"GetSessionSeatingBindingForUpdate": {reasonInternalKeyed, "organizer seating bind and import"},
	"LockSessionForSalesState":          {reasonInternalKeyed, "lock of the organizer's sales-state change: it selects sales_mode and salesstate.Change refuses control_only with ErrControlOnly (409 session.sales_mode_locked)"},
	"LockSessionSalesMode":              {reasonInternalKeyed, "the write guards read the mode through it and must see control_only"},
	"SessionExistsActive":               {reasonInternalKeyed, "no production caller found"},

	// Public, feed-token and Bil24 paths call these, but only with an id the
	// caller already holds; the note names the gate that covers the caller.
	"GetSalesStateSessionScope":          {reasonInternalKeyed, "Bil24 SALE_SUBSCRIBE: returns no session data, salesubscribe.Subscribe accepts only Soon/Open, ControlOnly is refused"},
	"GetSessionAdmissionModeByID":        {reasonInternalKeyed, "hold/quote/checkout paths: GetPublicCheckoutContext filters, then CheckSessionSellable; Bil24 seat list refuses a non-sellable session first"},
	"GetSessionByID":                     {reasonInternalKeyed, "Bil24 order create: keyed by the cart's session, a hold already passed CheckSessionSalesOpen"},
	"GetSessionEventID":                  {reasonInternalKeyed, "Bil24 order create and checkout_order: keyed by the cart's session, hold gate CheckSessionSalesOpen"},
	"GetSessionHoldPolicyProfile":        {reasonInternalKeyed, "holdpolicy.ForSession after the filtered GetPublicCheckoutContext / GetPublicSessionSeatStatusMeta"},
	"GetSessionOrgContext":               {reasonInternalKeyed, "Bil24 cart/tickets/promo and hcheckout: hold gate CheckSessionSalesOpen, or an order already placed; returns org ids only"},
	"GetSessionSalesState":               {reasonInternalKeyed, "category_gate is the gate itself; Bil24 seat list refuses a non-sellable session with SessionSellable on this value"},
	"ListAddonTierIDsByOrg":              {reasonInternalKeyed, "Bil24 catalog: keyed by loaded ids of ListActionEventsByOrg, which filters control_only"},
	"ListManualSalesStatesByOrg":         {reasonInternalKeyed, "Bil24 catalog: keyed by loaded ids of ListActionEventsByOrg, which filters control_only"},
	"ListPlacelessPlanTierIDsBySessions": {reasonInternalKeyed, "keyed by loaded ids of ListPublicSessionsByEvent / ListSeriesSessions (filtered), and by the hold gate"},
	"ListSessionAdmissionModesByEvent":   {reasonInternalKeyed, "hfeed public feed: read next to ListPublicSessionsByEvent, rows of a control session are never looked up"},
	"ListSessionSalesFacts":              {reasonInternalKeyed, "keyed by loaded ids of ListPublicSessionsByEvent / ListSeriesSessions (filtered); salesalert and salesubscribe read ControlOnly through Effective"},
	"ListTierChainByOrg":                 {reasonInternalKeyed, "Bil24 catalog: keyed by loaded ids of ListActionEventsByOrg, which filters control_only"},

	// Reached only through an order id the buyer already holds.
	"GetPublicOrderEvent": {reasonOrderBound, "hfeed public_checkout_status by checkout token of an existing order"},
}

// fileAllowlist: production Go file (relative to apps/backend, with / slashes)
// holding raw SQL -> why it reads sessions unfiltered. Unnamed SQL strings of
// gen/*.go are keyed the same way.
var fileAllowlist = map[string]allowEntry{
	"cmd/arena-e2e-seed/sale_times_fixture.go":     {reasonWrite, "e2e fixture tool, fixed session id"},
	"cmd/arena-e2e-seed/session_change_fixture.go": {reasonWrite, "e2e fixture tool, deletes its own sessions"},
	"cmd/arena-seed/main.go":                       {reasonWrite, "seed tool"},

	"internal/adapters/postgres/gen/complimentary_list.sql.go": {reasonOrganizerOnly, "organizer complimentary list"},
	"internal/adapters/postgres/gen/guest_list.sql.go":         {reasonOrganizerOnly, "guest list for the door team; a control session's list is wanted (migration 0228)"},
	"internal/adapters/postgres/gen/orders_search.sql.go":      {reasonOrganizerOnly, "organizer orders search"},

	"internal/platform/billing/reconcile/reconcile.go": {reasonOrderBound, "reconciliation of issued tickets"},
	"internal/platform/delivery/buyer_contact.go":      {reasonOrderBound, "ticket delivery"},
	"internal/platform/delivery/letter_status.go":      {reasonOrderBound, "letter status of an order"},
	"internal/platform/delivery/scanned_alert.go":      {reasonDoorControl, "alert about a scanned ticket"},
	"internal/platform/delivery/white_label.go":        {reasonOrderBound, "white-label brand of a ticket letter"},
	"internal/platform/disputes/card.go":               {reasonOrderBound, "dispute of an order"},
	"internal/platform/disputes/facts.go":              {reasonOrderBound, "dispute evidence of an order"},
	"internal/platform/disputes/watch.go":              {reasonOrderBound, "dispute of an order"},
	"internal/platform/emailstatus/retention.go":       {reasonOrderBound, "retention anchor of a letter"},
	"internal/platform/guestlistteam/team.go":          {reasonDoorControl, "door list auto-send; its comment says why control sessions stay"},
	"internal/platform/legacytickets/import.go":        {reasonWrite, "operator CLI import of legacy tickets"},
	"internal/platform/orderexport/query.go":           {reasonOrganizerOnly, "organizer order export"},
	"internal/platform/referral/wait.go":               {reasonOrderBound, "ends of the sessions of a friend's order"},
	"internal/platform/refunds/scan_exception.go":      {reasonOrderBound, "refund of a scanned ticket"},
	"internal/platform/refunds/scanned.go":             {reasonOrderBound, "refund of a scanned ticket"},
	"internal/platform/salesnotify/store.go":           {reasonOrderBound, "organizer sales notification of an order"},
	"internal/platform/ticketreissue/invitation.go":    {reasonOrderBound, "reissue of an existing invitation"},
	"internal/platform/ticketreissue/load.go":          {reasonOrderBound, "reissue of an existing order"},

	"internal/platform/conversions/event.go": {reasonInternalKeyed, "PGStore.SessionItem: hfeed cart_conversions passes the session of a cart whose hold already passed CheckSessionSalesOpen"},
}

// controlOnlyMustFilter are the queries every public surface goes through, as
// sqlc name, found in any gen file. They must carry control_only, allowlist
// or not.
var controlOnlyMustFilter = []string{
	"ListPublicSessionsByEvent",
	"ListPublicEventVenueNames",
	"GetPublicCheckoutContext",
	"GetPublicSessionSchema",
	"GetPublicSessionSeatStatusMeta",
	"ListSeriesSessions",
	"ListActionEventsByOrg",
	"ListActionVenuesByOrg",
	"CheckSalesLinkTarget",
	"ListEventVenueDetails",
}

// sqlString is one backtick SQL string of a gen file.
type sqlString struct {
	name string // "" when the string has no sqlc name marker
	body string
}

// concatJoin finds "` + sessionColumns + `" inside a Go source: a raw string
// glued to a constant is one SQL string.
var concatJoin = regexp.MustCompile("`\\s*\\+\\s*[\\w.]+\\s*\\+\\s*`")

// backtickStrings returns the raw (backtick) strings of a Go source, a string
// glued to a constant counting as one.
func backtickStrings(src string) []string {
	src = concatJoin.ReplaceAllString(src, " ")
	parts := strings.Split(src, "`")
	var out []string
	// Odd indexes are inside backticks.
	for i := 1; i < len(parts); i += 2 {
		out = append(out, parts[i])
	}
	return out
}

// sqlStringName is the sqlc name of a string, "" when it has no marker.
func sqlStringName(body string) string {
	if m := sqlNameLine.FindStringSubmatch(strings.TrimLeft(body, " \t\r\n")); m != nil {
		return m[1]
	}
	return ""
}

// genSQLStrings returns the backtick strings of a gen file that read the
// sessions table, named by their "-- name:" marker when they have one.
func genSQLStrings(src string) []sqlString {
	var out []sqlString
	for _, body := range backtickStrings(src) {
		if !sessionsReadSQL.MatchString(body) {
			continue
		}
		out = append(out, sqlString{name: sqlStringName(body), body: body})
	}
	return out
}

// goProductionFiles lists non-test .go files under internal and cmd, with the
// path relative to apps/backend (slash separated).
func goProductionFiles(t *testing.T) map[string]string {
	t.Helper()
	backend := backendDir(t)
	files := map[string]string{}
	for _, root := range []string{"internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(backend, root), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(backend, path)
			src, rerr := os.ReadFile(path) // #nosec G304 — repo-local test scan
			if rerr != nil {
				t.Fatalf("read %s: %v", path, rerr)
			}
			files[filepath.ToSlash(rel)] = string(src)
			return nil
		})
	}
	return files
}

const genDirSlash = "internal/adapters/postgres/gen/"

func TestControlOnlyFilterGuard_EveryReadOfSessionsFiltersOrIsAllowListed(t *testing.T) {
	files := goProductionFiles(t)
	var violations []string
	seenGen := map[string]bool{}
	seenFile := map[string]bool{}

	for rel, src := range files {
		if strings.HasPrefix(rel, genDirSlash) {
			for _, s := range genSQLStrings(src) {
				if strings.Contains(s.body, "control_only") {
					continue
				}
				if s.name != "" {
					seenGen[s.name] = true
					if _, ok := genQueryAllowlist[s.name]; !ok {
						violations = append(violations, rel+": query "+s.name+" reads sessions without control_only and is not allow-listed")
					}
					continue
				}
				seenFile[rel] = true
				if _, ok := fileAllowlist[rel]; !ok {
					violations = append(violations, rel+": hand-written SQL reads sessions without control_only and the file is not allow-listed")
				}
			}
			continue
		}
		code := stripBlockAndLineComments(src)
		if !sessionsReadSQL.MatchString(code) || strings.Contains(code, "control_only") {
			continue
		}
		seenFile[rel] = true
		if _, ok := fileAllowlist[rel]; !ok {
			violations = append(violations, rel+": raw SQL reads sessions, the file never mentions control_only and is not allow-listed")
		}
	}

	// Stale half: every entry must still match.
	for name := range genQueryAllowlist {
		if !seenGen[name] {
			violations = append(violations, "genQueryAllowlist entry "+name+" no longer matches a query that reads sessions without control_only: remove it")
		}
	}
	for rel := range fileAllowlist {
		if !seenFile[rel] {
			violations = append(violations, "fileAllowlist entry "+rel+" no longer reads sessions without control_only: remove it")
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Errorf("every query reading sessions must filter sales_mode='control_only' or be allow-listed with a reason:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

func TestControlOnlyFilterGuard_AllowlistReasonsAreClosed(t *testing.T) {
	check := func(kind, key string, e allowEntry) {
		if !closedReasons[e.reason] {
			t.Errorf("%s %s has reason %q outside the closed set", kind, key, e.reason)
		}
		if strings.TrimSpace(e.note) == "" {
			t.Errorf("%s %s has no note: say who calls it and what covers the caller", kind, key)
		}
	}
	for k, e := range genQueryAllowlist {
		check("genQueryAllowlist", k, e)
	}
	for k, e := range fileAllowlist {
		check("fileAllowlist", k, e)
	}
}

func TestControlOnlyFilterGuard_PublicQueriesMustFilter(t *testing.T) {
	files := goProductionFiles(t)
	bodies := map[string]string{} // sqlc name -> SQL text
	where := map[string]string{}
	for rel, src := range files {
		if !strings.HasPrefix(rel, genDirSlash) {
			continue
		}
		for _, body := range backtickStrings(src) {
			if name := sqlStringName(body); name != "" {
				bodies[name] = body
				where[name] = rel
			}
		}
	}
	for _, name := range controlOnlyMustFilter {
		body, ok := bodies[name]
		if !ok {
			t.Errorf("must-filter query %s is gone from gen/: update controlOnlyMustFilter on purpose", name)
			continue
		}
		if !strings.Contains(body, "control_only") {
			t.Errorf("%s: query %s is a public surface and must filter control_only", where[name], name)
		}
	}
}
