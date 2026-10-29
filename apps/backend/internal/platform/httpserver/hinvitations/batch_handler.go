package hinvitations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/csvexport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// batchBodyOverhead is what a multipart upload may add to the file itself
// (boundaries, the small text fields).
const batchBodyOverhead = 64 << 10

// batchWorkBudget bounds the batch work (the validation reads, the
// transaction and the read-back) and batchWriteSlack is what the response
// may take beyond it.
const (
	batchWorkBudget = 60 * time.Second
	batchWriteSlack = 15 * time.Second
)

// batchWorkCtx is the detached context of the whole upload. A file is one
// transaction that commits or rolls back whole, and it must not be cut by
// the router's REQUEST_TIMEOUT (chi cancels the request context at 30 s) or
// by a client that gives up: a caller that lost the answer repeats the
// request_id and reads the batch. The connection's write deadline moves
// past the budget too, because the server's own WriteTimeout
// (REQUEST_TIMEOUT + 5 s) would otherwise drop the answer of a batch that
// committed late. Every read after the commit uses this context as well.
func batchWorkCtx(w http.ResponseWriter, r *http.Request) (context.Context, context.CancelFunc) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(batchWorkBudget + batchWriteSlack))
	return context.WithTimeout(context.WithoutCancel(r.Context()), batchWorkBudget)
}

type batchPreviewGuest struct {
	Guest    int    `json:"guest"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Delivery string `json:"delivery"`
	Places   int    `json:"places"`
}

// batchReport is the answer of a dry run, and the details of a refused
// upload.
type batchReport struct {
	DryRun      bool                `json:"dry_run"`
	Valid       bool                `json:"valid"`
	Delivery    string              `json:"delivery"`
	Rows        int                 `json:"rows"`
	Guests      int                 `json:"guests"`
	Places      int                 `json:"places"`
	Seats       int                 `json:"seats"`
	GAPlaces    int                 `json:"ga_places"`
	FromReserve int                 `json:"from_reserve"`
	Errors      []RowIssue          `json:"errors"`
	Preview     []batchPreviewGuest `json:"preview"`
	// Charge is the billing preview (spec 46, section 3.6): a CSV file is
	// always charged at the standard rate. Filled by INV-07.
	Charge any `json:"charge"`
}

type batchOrderResponse struct {
	Guest       int      `json:"guest"`
	OrderID     string   `json:"order_id"`
	SystemID    int64    `json:"system_id"`
	Status      string   `json:"status"`
	GuestName   *string  `json:"guest_name"`
	GuestEmail  *string  `json:"guest_email"`
	Delivery    string   `json:"delivery"`
	Places      int      `json:"places"`
	FromReserve int      `json:"from_reserve"`
	SeatKeys    []string `json:"seat_keys"`
}

type batchResponse struct {
	ID          string               `json:"id"`
	SessionID   string               `json:"session_id"`
	Status      string               `json:"status"`
	CreatedBy   *string              `json:"created_by"`
	CreatedAt   time.Time            `json:"created_at"`
	Rows        int32                `json:"rows"`
	Guests      int                  `json:"guests"`
	Places      int32                `json:"places"`
	FromReserve int32                `json:"from_reserve"`
	Orders      []batchOrderResponse `json:"orders"`
	// Charge is the billing preview (INV-07); null until it lands.
	Charge   any  `json:"charge"`
	Replayed bool `json:"replayed"`
}

// upload is what the route reads out of the request.
type upload struct {
	file      []byte
	requestID string
	delivery  string
}

// readUpload reads the multipart form of an upload: the CSV in the file
// part, request_id and delivery as text parts. (The router's content-type
// gate only lets JSON and multipart bodies through, so a bare text/csv
// body is not an option.)
func readUpload(w http.ResponseWriter, r *http.Request) (upload, *IssueError) {
	var up upload
	r.Body = http.MaxBytesReader(w, r.Body, MaxBatchFileBytes+batchBodyOverhead)
	tooLarge := func() *IssueError {
		return refuse(http.StatusRequestEntityTooLarge, "invitation_batch.file_too_large",
			"the file is larger than 2 MB", map[string]any{"max_bytes": MaxBatchFileBytes})
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "multipart/form-data" {
		return up, refuse(http.StatusBadRequest, "invitation_batch.invalid_body",
			"send a multipart/form-data form with the CSV in the file part", nil)
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return up, refuse(http.StatusBadRequest, "invitation_batch.invalid_body", "the request is not a valid multipart form", nil)
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return up, tooLarge()
			}
			return up, refuse(http.StatusBadRequest, "invitation_batch.invalid_body", "the request is not a valid multipart form", nil)
		}
		switch part.FormName() {
		case "file":
			raw, rerr := io.ReadAll(io.LimitReader(part, MaxBatchFileBytes+1))
			if rerr != nil || len(raw) > MaxBatchFileBytes {
				return up, tooLarge()
			}
			up.file = raw
		case "request_id", "delivery":
			raw, _ := io.ReadAll(io.LimitReader(part, 256))
			if part.FormName() == "request_id" {
				up.requestID = strings.TrimSpace(string(raw))
			} else {
				up.delivery = strings.TrimSpace(string(raw))
			}
		}
	}
	if up.file == nil {
		return up, refuse(http.StatusBadRequest, "invitation_batch.file_required", "the form has no file part", nil)
	}
	return up, nil
}

// HandleBatchCreate serves POST /v1/organizations/{org_id}/sessions/{session_id}/invitation-batches?dry_run=.
// With dry_run=true nothing is written: the answer is the validation report.
// Without it the file is issued in ONE transaction, or refused with the
// row-by-row report and nothing written.
func (h *Handler) HandleBatchCreate(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil || h.queries == nil {
		unavailable(w, r)
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}
	dry := false
	if v := strings.TrimSpace(r.URL.Query().Get("dry_run")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeErr(w, r, http.StatusBadRequest, "invitation_batch.invalid_dry_run", "dry_run must be true or false", nil)
			return
		}
		dry = b
	}
	up, uerr := readUpload(w, r)
	if uerr != nil {
		writeIssueErr(w, r, uerr)
		return
	}
	delivery := up.delivery
	if delivery == "" {
		delivery = DeliveryPDFOnly
	}
	switch delivery {
	case DeliveryGuestEmail, DeliveryMyEmail, DeliveryPDFOnly:
	default:
		writeErr(w, r, http.StatusBadRequest, "invitation_batch.invalid_delivery",
			"delivery must be guest_email, my_email or pdf_only", nil)
		return
	}
	var requestID uuid.UUID
	if !dry || up.requestID != "" {
		id, err := uuid.Parse(up.requestID)
		if err != nil || id == uuid.Nil {
			writeErr(w, r, http.StatusBadRequest, "invitation_batch.invalid_request_id", "request_id must be a UUID", nil)
			return
		}
		requestID = id
	}
	fileHash := RequestDigest(up.file, delivery)

	ctx, cancel := batchWorkCtx(w, r)
	defer cancel()
	issuedBy, actorLabel, auditType := requestIssuer(ctx)
	if !dry && delivery == DeliveryMyEmail && issuedBy == nil {
		writeErr(w, r, http.StatusUnprocessableEntity, "invitation.my_email_requires_user",
			"delivery my_email needs a signed-in user; an API key has no e-mail", nil)
		return
	}

	// A repeat of an upload that was already issued answers the first
	// batch, BEFORE the file is validated against seats it has since sold.
	if !dry {
		prev, err := h.queries.GetInvitationBatchByRequest(ctx, orgID, requestID)
		switch {
		case err == nil:
			if prev.SessionID != sessionID || prev.FileSHA256 != fileHash {
				writeErr(w, r, http.StatusConflict, "invitation_batch.request_id_reused",
					"this request_id was already used for another session or another file", nil)
				return
			}
			h.writeBatch(ctx, w, r, orgID, prev, true, http.StatusOK)
			return
		case !errors.Is(err, pgx.ErrNoRows):
			h.internal(w, r, "batch: request lookup", err)
			return
		}
	}

	target, err := h.queries.GetInvitationSessionTarget(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (target.Deleted || target.OrgID != orgID)) {
		writeErr(w, r, http.StatusNotFound, "session.not_found", "session not found", nil)
		return
	}
	if err != nil {
		h.internal(w, r, "batch: session lookup", err)
		return
	}
	if target.Status == "cancelled" {
		writeErr(w, r, http.StatusUnprocessableEntity, "invitation.session_cancelled",
			"the session is cancelled, no invitation can be issued for it", nil)
		return
	}

	parsed, perr := ParseCSV(up.file)
	if perr != nil {
		writeIssueErr(w, r, perr)
		return
	}
	plan, err := h.loadPlan(ctx, sessionID)
	if err != nil {
		h.internal(w, r, "batch: plan", err)
		return
	}
	res := ResolveBatch(parsed.Rows, plan, delivery)
	report := buildReport(res, delivery, dry)

	if dry {
		httputil.WriteJSON(w, http.StatusOK, report)
		return
	}
	if len(res.Issues) > 0 {
		writeErr(w, r, http.StatusUnprocessableEntity, "invitation_batch.invalid",
			"the file has errors, nothing was issued", map[string]any{"report": report})
		return
	}

	in := BatchInput{
		OrgID: orgID, SessionID: sessionID, RequestID: requestID, FileSHA256: fileHash,
		Rows: res.Rows, Guests: res.Guests, IssuedBy: issuedBy, Actor: actorLabel,
	}
	if h.audit != nil {
		actorID := ""
		if issuedBy != nil {
			actorID = issuedBy.String()
		}
		write := func(ctx context.Context, tx pgx.Tx, action, resourceType, resourceID string, meta map[string]any) error {
			return h.audit.WriteTx(ctx, tx, audit.Event{
				OccurredAt:   time.Now().UTC(),
				ActorType:    auditType,
				ActorID:      actorID,
				Action:       action,
				ResourceType: resourceType,
				ResourceID:   resourceID,
				RequestID:    logging.RequestID(ctx),
				TraceID:      logging.TraceID(ctx),
				Metadata:     meta,
			})
		}
		in.Audit = func(ctx context.Context, tx pgx.Tx, batch gen.InvitationBatchRow, meta map[string]any) error {
			return write(ctx, tx, "v1.invitation.batch_issue", "invitation_batch", batch.ID.String(), meta)
		}
		// Each order also gets the row a single invitation writes.
		in.AuditOrder = func(ctx context.Context, tx pgx.Tx, order gen.OrderRow, meta map[string]any) error {
			return write(ctx, tx, "v1.invitation.issue", "order", order.ID.String(), meta)
		}
	}
	out, err := IssueBatch(ctx, h.pool, h.queries, in)
	if err != nil {
		h.writeBatchRefusal(w, r, err, res, report)
		return
	}
	batch, err := h.queries.GetInvitationBatch(ctx, out.BatchID, orgID)
	if err != nil {
		h.internal(w, r, "batch: read back", err)
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
	}
	h.writeBatch(ctx, w, r, orgID, batch, out.Replayed, status)
}

// writeBatchRefusal answers a refused batch transaction: the underlying
// code and status, with the rows the refusal points at.
func (h *Handler) writeBatchRefusal(w http.ResponseWriter, r *http.Request, err error, res Resolution, report batchReport) {
	var ge *GuestError
	var ie *IssueError
	switch {
	case errors.As(err, &ge):
		ie = ge.Err
	case errors.As(err, &ie):
	default:
		if errors.Is(err, context.DeadlineExceeded) {
			// The deadline may have struck during COMMIT, so the outcome is
			// unknown: repeating the request_id answers the batch if it
			// committed and issues it if it did not.
			writeErr(w, r, http.StatusServiceUnavailable, "invitation_batch.timeout",
				"the file took too long and the result is unknown, repeat the request with the same request_id: it answers the batch if it was issued", nil)
			return
		}
		h.logger.Error("invitation.batch failed", "error", err.Error())
		writeErr(w, r, http.StatusInternalServerError, "invitation_batch.failed", "the invitations could not be issued, nothing was written", nil)
		return
	}
	details := map[string]any{}
	for k, v := range ie.Details {
		details[k] = v
	}
	var rows []RowIssue
	if keys, ok := ie.Details["seat_keys"].([]string); ok {
		for _, k := range keys {
			rows = append(rows, RowIssue{Row: res.SeatLines[k], Field: "seat", Code: ie.Code, Message: ie.Message + ": " + k})
		}
	} else if ge != nil && ge.Seq >= 1 && ge.Seq <= len(res.Guests) {
		lines := res.Guests[ge.Seq-1].Lines
		rows = append(rows, RowIssue{Row: lines[0], Code: ie.Code, Message: ie.Message})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Row < rows[j].Row })
	report.Errors = rows
	report.Valid = false
	details["report"] = report
	writeErr(w, r, ie.Status, ie.Code, ie.Message+", nothing was issued", details)
}

// loadPlan reads the session as the upload is resolved against it.
func (h *Handler) loadPlan(ctx context.Context, sessionID uuid.UUID) (Plan, error) {
	seats, err := h.queries.ListInvitationPlanSeats(ctx, sessionID)
	if err != nil {
		return Plan{}, err
	}
	tiers, err := h.queries.ListTicketTiersBySession(ctx, sessionID)
	if err != nil {
		return Plan{}, err
	}
	counts, err := h.queries.ListInvitationGACounts(ctx, sessionID)
	if err != nil {
		return Plan{}, err
	}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	var plan Plan
	for _, s := range seats {
		plan.Seats = append(plan.Seats, PlanSeat{
			ID: s.ID, Key: s.SeatKey, Sector: deref(s.SectorName), Row: deref(s.RowName),
			Seat: deref(s.SeatNumber), Status: s.Status, TierID: s.TierID,
		})
	}
	capTotal, used, err := h.queries.GetInvitationSessionCapacity(ctx, sessionID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		none := 0 // no ledger row: ReserveCapacity refuses every hold
		plan.CapacityLeft = &none
	case err != nil:
		return Plan{}, err
	case capTotal != nil:
		left := int(*capTotal) - int(used)
		plan.CapacityLeft = &left
	}
	ga := make(map[uuid.UUID]gen.InvitationGACount, len(counts))
	for _, c := range counts {
		ga[c.TierID] = c
	}
	for _, t := range tiers {
		pt := PlanTier{ID: t.ID, Name: t.Name}
		if c, ok := ga[t.ID]; ok {
			pt.GA, pt.Available, pt.Reserve = true, int(c.Available), int(c.Reserve)
		}
		plan.Tiers = append(plan.Tiers, pt)
	}
	return plan, nil
}

func buildReport(res Resolution, delivery string, dry bool) batchReport {
	rep := batchReport{
		DryRun: dry, Valid: len(res.Issues) == 0, Delivery: delivery,
		Rows: res.Rows, Guests: len(res.Guests), Places: res.Places(), Seats: res.Seats,
		GAPlaces: res.GAPlaces, FromReserve: res.FromReserve,
		Errors:  res.Issues,
		Preview: make([]batchPreviewGuest, 0, len(res.Guests)),
		Charge:  nil,
	}
	if rep.Errors == nil {
		rep.Errors = []RowIssue{}
	}
	for _, g := range res.Guests {
		places := len(g.SeatKeys)
		for _, l := range g.GA {
			places += int(l.Quantity)
		}
		rep.Preview = append(rep.Preview, batchPreviewGuest{
			Guest: g.Seq, Name: g.Name, Email: g.Email, Delivery: g.Delivery, Places: places,
		})
	}
	return rep
}

// requestIssuer reads who is uploading: the user, or nil for an
// organization API key.
func requestIssuer(ctx context.Context) (issuedBy *uuid.UUID, actor, auditType string) {
	actor, auditType = "system", "service"
	a, _ := auth.ActorFromContext(ctx)
	if a.Type == auth.ActorTypeUser || a.Type == auth.ActorTypeStubUser {
		if uid, err := uuid.Parse(a.ID); err == nil {
			return &uid, "user:" + uid.String(), "user"
		}
	}
	return nil, actor, auditType
}

// batchView is a batch with its orders, as both read routes answer it.
type batchView struct {
	batch  gen.InvitationBatchRow
	orders []batchOrderResponse
}

func (h *Handler) loadBatchView(ctx context.Context, orgID uuid.UUID, batch gen.InvitationBatchRow) (batchView, error) {
	orders, err := h.queries.ListInvitationBatchOrders(ctx, batch.ID, orgID)
	if err != nil {
		return batchView{}, err
	}
	ids := make([]uuid.UUID, 0, len(orders))
	for _, o := range orders {
		ids = append(ids, o.ID)
	}
	byOrder := map[uuid.UUID][]gen.InvitationOrderLineRow{}
	if len(ids) > 0 {
		lines, err := h.queries.ListInvitationOrderLines(ctx, ids)
		if err != nil {
			return batchView{}, err
		}
		for _, l := range lines {
			byOrder[l.OrderID] = append(byOrder[l.OrderID], l)
		}
	}
	v := batchView{batch: batch, orders: make([]batchOrderResponse, 0, len(orders))}
	for _, o := range orders {
		resp := batchOrderResponse{
			OrderID: o.ID.String(), SystemID: o.SystemID, Status: o.Status, GuestName: o.BuyerName,
			GuestEmail: o.BuyerEmail, Delivery: deliveryOf(o.Metadata), SeatKeys: []string{},
			Guest: batchSeqOf(o.Metadata),
		}
		for _, l := range byOrder[o.ID] {
			resp.Places++
			if l.SeatOrigin != nil && *l.SeatOrigin == originUnavailable {
				resp.FromReserve++
			}
			if l.SeatKey != nil {
				resp.SeatKeys = append(resp.SeatKeys, *l.SeatKey)
			}
		}
		v.orders = append(v.orders, resp)
	}
	return v, nil
}

func (h *Handler) writeBatch(ctx context.Context, w http.ResponseWriter, r *http.Request, orgID uuid.UUID, batch gen.InvitationBatchRow, replayed bool, status int) {
	v, err := h.loadBatchView(ctx, orgID, batch)
	if err != nil {
		h.internal(w, r, "batch: orders", err)
		return
	}
	resp := batchResponse{
		ID: batch.ID.String(), SessionID: batch.SessionID.String(), Status: batch.Status,
		CreatedAt: batch.CreatedAt, Rows: batch.Rows, Guests: len(v.orders), Places: batch.Seats,
		FromReserve: batch.FromReserve, Orders: v.orders, Replayed: replayed,
	}
	if batch.CreatedBy != nil {
		s := batch.CreatedBy.String()
		resp.CreatedBy = &s
	}
	httputil.WriteJSON(w, status, resp)
}

// batchSeqOf reads orders.metadata.invitation.batch_seq.
func batchSeqOf(raw []byte) int {
	var m struct {
		Invitation struct {
			Seq int `json:"batch_seq"`
		} `json:"invitation"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.Invitation.Seq
}

// HandleBatchGet serves GET /v1/organizations/{org_id}/invitation-batches/{id}.
func (h *Handler) HandleBatchGet(w http.ResponseWriter, r *http.Request) {
	batch, ok := h.batchFromPath(w, r)
	if !ok {
		return
	}
	h.writeBatch(r.Context(), w, r, batch.OrgID, batch, false, http.StatusOK)
}

// HandleBatchResult serves GET /v1/organizations/{org_id}/invitation-batches/{id}/result.csv:
// one line per guest with the order number the guest's tickets belong to.
// Written by csvexport: UTF-8 with a BOM and `;`, so a spreadsheet opens it
// as columns in a European locale.
func (h *Handler) HandleBatchResult(w http.ResponseWriter, r *http.Request) {
	batch, ok := h.batchFromPath(w, r)
	if !ok {
		return
	}
	v, err := h.loadBatchView(r.Context(), batch.OrgID, batch)
	if err != nil {
		h.internal(w, r, "batch: result", err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="invitation-batch-%s.csv"`, batch.ID.String()[:8]))
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	cw := csvexport.NewWriter(w)
	_ = cw.WriteHeader([]string{"guest", "name", "email", "delivery", "order_number", "order_id", "places", "from_reserve", "seat_keys"})
	for _, o := range v.orders {
		_ = cw.WriteRow(
			csvexport.Number(int64(o.Guest)), csvexport.Text(deref(o.GuestName)), csvexport.Text(deref(o.GuestEmail)),
			csvexport.Text(o.Delivery), csvexport.Text(strconv.FormatInt(o.SystemID, 10)), csvexport.Text(o.OrderID),
			csvexport.Number(int64(o.Places)), csvexport.Number(int64(o.FromReserve)),
			csvexport.Text(strings.Join(o.SeatKeys, " ")),
		)
	}
	_ = cw.Flush()
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (h *Handler) batchFromPath(w http.ResponseWriter, r *http.Request) (gen.InvitationBatchRow, bool) {
	if h.queries == nil {
		unavailable(w, r)
		return gen.InvitationBatchRow{}, false
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return gen.InvitationBatchRow{}, false
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return gen.InvitationBatchRow{}, false
	}
	batch, err := h.queries.GetInvitationBatch(r.Context(), id, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, r, http.StatusNotFound, "invitation_batch.not_found", "invitation batch not found", nil)
		return gen.InvitationBatchRow{}, false
	}
	if err != nil {
		h.internal(w, r, "batch: lookup", err)
		return gen.InvitationBatchRow{}, false
	}
	return batch, true
}
